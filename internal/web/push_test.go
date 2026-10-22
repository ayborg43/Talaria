package web

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var b64 = base64.RawURLEncoding

// The example from RFC 8291, section 5 and appendix A: with the same keys and salt, the same bytes must come out.
func TestEncryptionMatchesTheRFC8291Example(t *testing.T) {
	s := Subscription{}
	s.Keys.P256dh = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	s.Keys.Auth = "BTBZMqHH6r4Tts7J_aSIgg"
	rawPriv, _ := b64.DecodeString("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")
	priv, err := ecdh.P256().NewPrivateKey(rawPriv)
	if err != nil {
		t.Fatal(err)
	}
	salt, _ := b64.DecodeString("DGv6ra1nlYgDCS1FRnbzlw")
	got, err := encrypt(s, []byte("When I grow up, I want to be a watermelon"), priv, salt)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64.EncodeToString(got) != want {
		t.Fatalf("not the RFC's bytes:\n got %s\nwant %s", b64.EncodeToString(got), want)
	}
}

// phone is the receiving end of a push: it holds the private half of the key it subscribed with and
// decrypts the way RFC 8291 says a user agent does, written separately from the sender's code.
type phone struct {
	priv *ecdh.PrivateKey
	auth []byte
	sub  Subscription
}

func newPhone(t *testing.T, endpoint string) *phone {
	t.Helper()
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	p := &phone{priv: priv, auth: auth}
	p.sub.Endpoint = endpoint
	p.sub.Keys.P256dh = b64.EncodeToString(priv.PublicKey().Bytes())
	p.sub.Keys.Auth = b64.EncodeToString(auth)
	return p
}

func (p *phone) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	asPub := body[21 : 21+idlen]
	ct := body[21+idlen:]
	if rs < 18 || int(rs) < len(ct) {
		t.Fatalf("record size %d for %d bytes", rs, len(ct))
	}
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := p.priv.ECDH(as)
	info := append(append([]byte("WebPush: info\x00"), p.priv.PublicKey().Bytes()...), asPub...)
	ikm, _ := hkdf.Key(sha256.New, shared, p.auth, string(info), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	blk, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(blk)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("the phone could not decrypt it: %v", err)
	}
	if plain[len(plain)-1] != 2 {
		t.Fatalf("the last record is not marked as the last: %x", plain[len(plain)-1])
	}
	return plain[:len(plain)-1]
}

type pushServer struct {
	*httptest.Server
	mu     sync.Mutex
	got    []*http.Request
	bodies [][]byte
	status int
}

func newPushServer(t *testing.T) (*pushServer, *http.Client) {
	t.Helper()
	ps := &pushServer{status: 201}
	ps.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ps.mu.Lock()
		ps.got = append(ps.got, r)
		ps.bodies = append(ps.bodies, b)
		st := ps.status
		ps.mu.Unlock()
		w.WriteHeader(st)
	}))
	t.Cleanup(ps.Close)
	pool := x509.NewCertPool()
	pool.AddCert(ps.Certificate())
	return ps, &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
}

func TestANotificationIsEncryptedSignedAndReadableOnlyByThePhone(t *testing.T) {
	ps, client := newPushServer(t)
	dir := t.TempDir()
	p := &Pusher{Path: filepath.Join(dir, "push.json"), Subject: "https://talaria.example.com", Client: client, AllowAny: true}
	ph := newPhone(t, ps.URL+"/push/abc")
	if err := p.Subscribe(ph.sub); err != nil {
		t.Fatal(err)
	}
	p.Notify("Needs your OK", "Allow shell?", "approval", "/#needs")

	if len(ps.got) != 1 {
		t.Fatalf("%d requests", len(ps.got))
	}
	r := ps.got[0]
	if r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("TTL") == "" || r.Header.Get("Urgency") != "high" {
		t.Fatalf("headers: %v", r.Header)
	}
	var m map[string]string
	if err := json.Unmarshal(ph.decrypt(t, ps.bodies[0]), &m); err != nil || m["title"] != "Needs your OK" || m["body"] != "Allow shell?" || m["tag"] != "approval" || m["url"] != "/#needs" {
		t.Fatalf("%v %v", m, err)
	}
	if strings.Contains(string(ps.bodies[0]), "Allow shell") {
		t.Fatal("the push service could read the message")
	}

	// The signature: ES256 over header.claims, by the key whose public half is in k=.
	auth := r.Header.Get("Authorization")
	tok, key, ok := strings.Cut(strings.TrimPrefix(auth, "vapid t="), ", k=")
	if !ok || !strings.HasPrefix(auth, "vapid t=") {
		t.Fatalf("authorization: %s", auth)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatal(tok)
	}
	pub, _ := b64.DecodeString(key)
	px, py := new(big.Int).SetBytes(pub[1:33]), new(big.Int).SetBytes(pub[33:65])
	sig, _ := b64.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: px, Y: py}, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the VAPID signature does not verify")
	}
	var claims struct {
		Aud, Sub string
		Exp      int64
	}
	raw, _ := b64.DecodeString(parts[1])
	json.Unmarshal(raw, &claims)
	if claims.Aud != "https://"+strings.TrimPrefix(ps.URL, "https://") || claims.Sub != "https://talaria.example.com" || claims.Exp < 1 {
		t.Fatalf("%+v", claims)
	}
	// The key a browser subscribes with is the one that signed.
	if want, _ := p.PublicKey(); want != key {
		t.Fatal("the public key given to browsers is not the signing key")
	}
}

func TestAPhoneThePushServiceSaysIsGoneIsForgottenAndOthersAreKept(t *testing.T) {
	ps, client := newPushServer(t)
	p := &Pusher{Path: filepath.Join(t.TempDir(), "push.json"), Client: client, AllowAny: true}
	a, b := newPhone(t, ps.URL+"/a"), newPhone(t, ps.URL+"/b")
	p.Subscribe(a.sub)
	p.Subscribe(b.sub)
	ps.mu.Lock()
	ps.status = 410
	ps.mu.Unlock()
	p.Notify("t", "b", "x", "/")
	if p.Count() != 0 {
		t.Fatalf("%d phones are still subscribed after the service said they are gone", p.Count())
	}
	p.Subscribe(a.sub)
	ps.mu.Lock()
	ps.status = 500
	ps.mu.Unlock()
	p.Notify("t", "b", "x", "/")
	if p.Count() != 1 {
		t.Fatal("a server error must not forget a phone")
	}
}

func TestSubscriptionsAreCheckedBeforeTheServerWillSendToThem(t *testing.T) {
	p := &Pusher{Path: filepath.Join(t.TempDir(), "push.json")}
	ph := newPhone(t, "")
	for _, ep := range []string{
		"http://fcm.googleapis.com/send/x", "https://evil.example.com/push", "https://127.0.0.1/push", "https://169.254.169.254/latest",
		"https://fcm.googleapis.com.evil.example/x", "https://user@fcm.googleapis.com/x", "https://fcm.googleapis.com:8443/x", "file:///etc/passwd", "",
		"https://notfcm.googleapis.com.example/x", "https://xpush.apple.com/x",
	} {
		s := ph.sub
		s.Endpoint = ep
		if err := p.Subscribe(s); err == nil {
			t.Errorf("%q was accepted", ep)
		}
	}
	for _, ep := range []string{"https://fcm.googleapis.com/fcm/send/abc", "https://updates.push.services.mozilla.com/wpush/v2/x", "https://web.push.apple.com/Qx", "https://wns2-par02p.notify.windows.com/w/?token=x"} {
		s := ph.sub
		s.Endpoint = ep
		if err := p.Subscribe(s); err != nil {
			t.Errorf("%q: %v", ep, err)
		}
	}
	bad := ph.sub
	bad.Endpoint = "https://fcm.googleapis.com/x"
	bad.Keys.P256dh = "AAAA"
	if p.Subscribe(bad) == nil {
		t.Error("a short key was accepted")
	}
	bad = ph.sub
	bad.Endpoint = "https://fcm.googleapis.com/x"
	bad.Keys.Auth = "AAAA"
	if p.Subscribe(bad) == nil {
		t.Error("a short secret was accepted")
	}
}

func TestPushKeysAndPhonesSurviveARestartAndAreKeptPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "push.json")
	p := &Pusher{Path: path}
	key1, err := p.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	s := newPhone(t, "https://fcm.googleapis.com/fcm/send/1").sub
	if err := p.Subscribe(s); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	q := &Pusher{Path: path}
	if key2, _ := q.PublicKey(); key2 != key1 || q.Count() != 1 {
		t.Fatalf("after a restart: key same %v, %d phones", key2 == key1, q.Count())
	}
	// The same phone again is not a second phone.
	q.Subscribe(s)
	if q.Count() != 1 {
		t.Fatal("a phone was counted twice")
	}
	for i := 0; i < 15; i++ {
		x := newPhone(t, "").sub
		x.Endpoint = "https://fcm.googleapis.com/fcm/send/n" + string(rune('a'+i))
		q.Subscribe(x)
	}
	if q.Count() > maxSubscriptions {
		t.Fatalf("%d phones", q.Count())
	}
	q.Unsubscribe("https://fcm.googleapis.com/fcm/send/na")
}

func TestALongMessageIsCutRatherThanRefused(t *testing.T) {
	ph := newPhone(t, "")
	body, err := encrypt(ph.sub, []byte(strings.Repeat("x", 10000)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ph.decrypt(t, body); len(got) != maxPayload {
		t.Fatalf("%d bytes", len(got))
	}
}
