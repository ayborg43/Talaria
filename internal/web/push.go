package web

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	maxSubscriptions = 10
	pushTTL          = 4 * 60 * 60 // seconds a push service holds a message for a phone that is off
	maxPayload       = 3000
)

// Subscription is one phone's address for notifications, as the browser gives it.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Added time.Time `json:"added,omitzero"`
}

// pushHosts are the push services browsers use. A subscription for anywhere else is refused: its address
// comes from the browser, and Talaria will send a request to it.
var pushHosts = []string{
	"fcm.googleapis.com", "updates.push.services.mozilla.com", "push.apple.com",
	"notify.windows.com", "push.services.mozilla.com",
}

// ValidEndpoint says whether an address is a push service's.
func ValidEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return errors.New("not a push address")
	}
	host := strings.ToLower(u.Hostname())
	for _, ok := range pushHosts {
		if host == ok || strings.HasSuffix(host, "."+ok) {
			return nil
		}
	}
	return errors.New("not a known push service")
}

// Pusher sends notifications to the phones that asked for them, with Web Push: the message is encrypted
// so that only the phone can read it, and signed (VAPID) so the push service knows who sent it.
type Pusher struct {
	Path    string // keys and subscriptions are kept here, readable by the owner only
	Subject string // who is sending, as a web address or mailto: address, as VAPID asks
	Client  *http.Client
	Logf    func(string, ...any)
	// AllowAny lifts the list of push services, for tests.
	AllowAny bool

	mu     sync.Mutex
	loaded bool
	key    *ecdsa.PrivateKey
	subs   []Subscription
}

type pushFile struct {
	Key  string         `json:"key"` // the private key, base64url
	Subs []Subscription `json:"subs"`
}

func (p *Pusher) logf(f string, a ...any) {
	if p.Logf != nil {
		p.Logf(f, a...)
	}
}

func (p *Pusher) load() error {
	if p.loaded {
		return nil
	}
	if p.Path != "" {
		if raw, err := os.ReadFile(p.Path); err == nil {
			var f pushFile
			if json.Unmarshal(raw, &f) == nil && f.Key != "" {
				if d, err := base64.RawURLEncoding.DecodeString(f.Key); err == nil && len(d) == 32 {
					if k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d); err == nil {
						p.key, p.subs = k, f.Subs
					}
				}
			}
		}
	}
	if p.key == nil {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		p.key = k
		if err := p.save(); err != nil {
			return err
		}
	}
	p.loaded = true
	return nil
}

func (p *Pusher) save() error {
	if p.Path == "" {
		return nil
	}
	d, err := p.key.Bytes()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(pushFile{Key: base64.RawURLEncoding.EncodeToString(d), Subs: p.subs})
	if err != nil {
		return err
	}
	return writeFile(p.Path, raw)
}

// PublicKey is what a browser needs to subscribe, base64url of the uncompressed point.
func (p *Pusher) PublicKey() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.load(); err != nil {
		return "", err
	}
	pub, err := p.key.PublicKey.Bytes()
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(pub), nil
}

// Subscribe remembers a phone. The same phone subscribing again replaces its earlier entry.
func (p *Pusher) Subscribe(s Subscription) error {
	if !p.AllowAny {
		if err := ValidEndpoint(s.Endpoint); err != nil {
			return err
		}
	}
	if k, err := base64.RawURLEncoding.DecodeString(s.Keys.P256dh); err != nil || len(k) != 65 {
		return errors.New("the subscription's key is not valid")
	} else if _, err := ecdh.P256().NewPublicKey(k); err != nil {
		return errors.New("the subscription's key is not valid")
	}
	if a, err := base64.RawURLEncoding.DecodeString(s.Keys.Auth); err != nil || len(a) != 16 {
		return errors.New("the subscription's secret is not valid")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.load(); err != nil {
		return err
	}
	s.Added = time.Now()
	subs := p.subs[:0:0]
	for _, old := range p.subs {
		if old.Endpoint != s.Endpoint {
			subs = append(subs, old)
		}
	}
	subs = append(subs, s)
	if len(subs) > maxSubscriptions {
		subs = subs[len(subs)-maxSubscriptions:]
	}
	p.subs = subs
	return p.save()
}

// Unsubscribe forgets a phone.
func (p *Pusher) Unsubscribe(endpoint string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.load(); err != nil {
		return err
	}
	subs := p.subs[:0:0]
	for _, old := range p.subs {
		if old.Endpoint != endpoint {
			subs = append(subs, old)
		}
	}
	p.subs = subs
	return p.save()
}

// Count is how many phones are subscribed.
func (p *Pusher) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.load()
	return len(p.subs)
}

// Notify sends a notification to every subscribed phone. A phone whose subscription the push service says
// is gone is forgotten.
func (p *Pusher) Notify(title, body, tag, path string) {
	payload, _ := json.Marshal(map[string]string{"title": clipRunes(title, 80), "body": clipRunes(body, 200), "tag": tag, "url": path})
	p.mu.Lock()
	if p.load() != nil {
		p.mu.Unlock()
		return
	}
	subs := append([]Subscription(nil), p.subs...)
	p.mu.Unlock()
	for _, s := range subs {
		status, err := p.send(s, payload)
		switch {
		case err != nil:
			p.logf("web: push failed: %v", err)
		case status == http.StatusGone || status == http.StatusNotFound:
			p.Unsubscribe(s.Endpoint)
		case status >= 400:
			p.logf("web: the push service answered %d", status)
		}
	}
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (p *Pusher) send(s Subscription, payload []byte) (int, error) {
	if !p.AllowAny {
		if err := ValidEndpoint(s.Endpoint); err != nil {
			return 0, err
		}
	}
	body, err := encrypt(s, payload, nil, nil)
	if err != nil {
		return 0, err
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil {
		return 0, err
	}
	auth, err := p.vapid(u.Scheme + "://" + u.Host)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", fmt.Sprint(pushTTL))
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", auth)
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, errors.New("could not reach the push service")
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

// vapid is the Authorization header that signs a request as from this server (RFC 8292).
func (p *Pusher) vapid(audience string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.load(); err != nil {
		return "", err
	}
	subject := p.Subject
	if subject == "" {
		subject = "mailto:admin@localhost.invalid"
	}
	enc := base64.RawURLEncoding.EncodeToString
	header, _ := json.Marshal(map[string]string{"typ": "JWT", "alg": "ES256"})
	claims, _ := json.Marshal(map[string]any{"aud": audience, "exp": time.Now().Add(12 * time.Hour).Unix(), "sub": subject})
	signing := enc(header) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, p.key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	pub, err := p.key.PublicKey.Bytes()
	if err != nil {
		return "", err
	}
	return "vapid t=" + signing + "." + enc(sig) + ", k=" + enc(pub), nil
}

// encrypt makes the body of a push (RFC 8291, aes128gcm): only the phone, which holds the private half of
// the key it subscribed with, can read it. priv and salt are random unless a test fixes them.
func encrypt(s Subscription, plaintext []byte, priv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > maxPayload {
		plaintext = plaintext[:maxPayload]
	}
	uaPubBytes, err := base64.RawURLEncoding.DecodeString(s.Keys.P256dh)
	if err != nil {
		return nil, err
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(s.Keys.Auth)
	if err != nil {
		return nil, err
	}
	uaPub, err := ecdh.P256().NewPublicKey(uaPubBytes)
	if err != nil {
		return nil, err
	}
	if priv == nil {
		if priv, err = ecdh.P256().GenerateKey(rand.Reader); err != nil {
			return nil, err
		}
	}
	if salt == nil {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
	}
	shared, err := priv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPub := priv.PublicKey().Bytes()

	keyInfo := append([]byte("WebPush: info\x00"), uaPubBytes...)
	keyInfo = append(keyInfo, asPub...)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	record := append(append([]byte(nil), plaintext...), 0x02) // 0x02 ends the last (here: only) record
	sealed := gcm.Seal(nil, nonce, record, nil)

	var out bytes.Buffer
	out.Write(salt)
	binary.Write(&out, binary.BigEndian, uint32(4096))
	out.WriteByte(byte(len(asPub)))
	out.Write(asPub)
	out.Write(sealed)
	return out.Bytes(), nil
}
