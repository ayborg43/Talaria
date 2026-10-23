// Package vault keeps the logins and keys the agent may use, encrypted on disk.
//
// The agent never reads them. It names a credential, and a tool puts the value
// into a web page's field itself, but only on a site the credential was saved
// for. Credentials are added at the command line by the user, never through a
// chat: whatever is typed into a chat is read by the model and its provider.
package vault

import (
	"cmp"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	formatVersion = 1
	kdfName       = "pbkdf2-sha256"
	// iterationsDefault is high enough that guessing a passphrase is slow, and low
	// enough that unlocking takes a fraction of a second.
	iterationsDefault = 600_000
	// MinPassphrase is the shortest passphrase `tal vault init` accepts.
	MinPassphrase = 12
	maxSecret     = 4096
	maxSites      = 10
)

// iterations is how many rounds new keys are derived with; tests lower it.
var iterations = iterationsDefault

// Credential kinds.
const (
	KindLogin  = "login"  // a username and a password
	KindSecret = "secret" // a single value, such as an API key
	// KindOAuth is a sign-in to an MCP server made by `tal connect`: access and
	// refresh tokens, and what is needed to renew them. Only Talaria's own MCP
	// client uses these; they cannot be typed into a page.
	KindOAuth = "oauth"
)

// OAuthData is an OAuth sign-in.
type OAuthData struct {
	Issuer             string    `json:"issuer,omitempty"`
	TokenEndpoint      string    `json:"token_endpoint"`
	RevocationEndpoint string    `json:"revocation_endpoint,omitempty"`
	ClientID           string    `json:"client_id"`
	ClientSecret       string    `json:"client_secret,omitempty"`
	Resource           string    `json:"resource,omitempty"` // the server the tokens are for
	Scopes             []string  `json:"scopes,omitempty"`
	AccessToken        string    `json:"access_token,omitempty"`
	RefreshToken       string    `json:"refresh_token,omitempty"`
	Expiry             time.Time `json:"expiry,omitzero"`
}

// A Credential is one saved login or key.
type Credential struct {
	Name     string     `json:"name"`
	Kind     string     `json:"kind"`
	Username string     `json:"username,omitempty"`
	Secret   string     `json:"secret,omitempty"` // the password, or the value
	OAuth    *OAuthData `json:"oauth,omitempty"`
	Sites    []string   `json:"sites"` // where it may be used: see AllowsURL
	Added    time.Time  `json:"added"`
}

// Info is what may be shown of a credential: everything but its secret.
type Info struct {
	Name  string
	Kind  string
	Sites []string
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,39}$`)

// ErrWrongPassphrase is returned when a vault will not open.
var ErrWrongPassphrase = errors.New("the passphrase is wrong, or the vault file is damaged")

// ErrNoVault is returned when there is no vault file.
var ErrNoVault = errors.New("there is no vault yet: create one with `tal vault init`")

type fileFormat struct {
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Data       string `json:"data"`
}

type payload struct {
	Credentials []Credential `json:"credentials"`
}

// Vault is an open vault. It rereads the file when another process changes it.
type Vault struct {
	Path string
	// OnLoad is given every secret whenever the vault is read; see Open.
	OnLoad func(secrets []string)

	mu    sync.Mutex
	key   []byte
	salt  []byte
	iter  int
	creds map[string]Credential
	mtime time.Time
	size  int64
}

func (f fileFormat) aad() []byte {
	return []byte(fmt.Sprintf("talaria-vault|%d|%s|%d|%s", f.Version, f.KDF, f.Iterations, f.Salt))
}

func deriveKey(passphrase string, salt []byte, iter int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, passphrase, salt, iter, 32)
}

// Create makes a new, empty vault. It refuses to overwrite one.
func Create(path, passphrase string) (*Vault, error) {
	if len([]rune(passphrase)) < MinPassphrase {
		return nil, fmt.Errorf("the passphrase must be at least %d characters", MinPassphrase)
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%s already exists", path)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := deriveKey(passphrase, salt, iterations)
	if err != nil {
		return nil, err
	}
	v := &Vault{Path: path, key: key, salt: salt, iter: iterations, creds: map[string]Credential{}}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v, v.saveLocked()
}

// Open unlocks the vault at path. onLoad, if not nil, is called with every
// secret in it now, and again whenever the vault changes, so that they can be
// kept out of anything the model sees or that is stored.
func Open(path, passphrase string, onLoad func(secrets []string)) (*Vault, error) {
	v := &Vault{Path: path, OnLoad: onLoad}
	v.mu.Lock()
	defer v.mu.Unlock()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, ErrNoVault
	}
	if err != nil {
		return nil, err
	}
	f, err := parse(raw)
	if err != nil {
		return nil, err
	}
	salt, _ := base64.StdEncoding.DecodeString(f.Salt)
	key, err := deriveKey(passphrase, salt, f.Iterations)
	if err != nil {
		return nil, err
	}
	v.key, v.salt, v.iter = key, salt, f.Iterations
	if err := v.decryptLocked(f); err != nil {
		return nil, err
	}
	v.statLocked()
	v.announceLocked()
	return v, nil
}

func parse(raw []byte) (fileFormat, error) {
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("the vault file is not readable: %w", err)
	}
	if f.Version != formatVersion || f.KDF != kdfName || f.Iterations < 100_000 || f.Iterations > 10_000_000 {
		return f, fmt.Errorf("the vault file is in a format this Talaria does not know (version %d, %s)", f.Version, f.KDF)
	}
	return f, nil
}

func (v *Vault) decryptLocked(f fileFormat) error {
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce, err1 := base64.StdEncoding.DecodeString(f.Nonce)
	data, err2 := base64.StdEncoding.DecodeString(f.Data)
	if err1 != nil || err2 != nil || len(nonce) != gcm.NonceSize() {
		return ErrWrongPassphrase
	}
	plain, err := gcm.Open(nil, nonce, data, f.aad())
	if err != nil {
		return ErrWrongPassphrase
	}
	var p payload
	if err := json.Unmarshal(plain, &p); err != nil {
		return ErrWrongPassphrase
	}
	v.creds = make(map[string]Credential, len(p.Credentials))
	for _, c := range p.Credentials {
		v.creds[c.Name] = c
	}
	return nil
}

func (v *Vault) statLocked() {
	if fi, err := os.Stat(v.Path); err == nil {
		v.mtime, v.size = fi.ModTime(), fi.Size()
	}
}

func (v *Vault) announceLocked() {
	if v.OnLoad == nil {
		return
	}
	var secrets []string
	for _, c := range v.creds {
		secrets = append(secrets, c.Secret)
		if o := c.OAuth; o != nil {
			secrets = append(secrets, o.AccessToken, o.RefreshToken, o.ClientSecret)
		}
	}
	v.OnLoad(secrets)
}

// refreshLocked rereads the file if it changed, so a credential added while the
// gateway runs is there at once. A file that no longer opens leaves what was
// loaded alone.
func (v *Vault) refreshLocked() {
	fi, err := os.Stat(v.Path)
	if err != nil || (fi.ModTime().Equal(v.mtime) && fi.Size() == v.size) {
		return
	}
	raw, err := os.ReadFile(v.Path)
	if err != nil {
		return
	}
	f, err := parse(raw)
	if err != nil {
		return
	}
	if salt, _ := base64.StdEncoding.DecodeString(f.Salt); string(salt) != string(v.salt) || f.Iterations != v.iter {
		return // re-keyed under another passphrase: this process cannot read it
	}
	if v.decryptLocked(f) == nil {
		v.mtime, v.size = fi.ModTime(), fi.Size()
		v.announceLocked()
	}
}

func (v *Vault) saveLocked() error {
	p := payload{}
	for _, c := range v.creds {
		p.Credentials = append(p.Credentials, c)
	}
	slices.SortFunc(p.Credentials, func(a, b Credential) int { return cmp.Compare(a.Name, b.Name) })
	plain, err := json.Marshal(p)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	f := fileFormat{Version: formatVersion, KDF: kdfName, Iterations: v.iter, Salt: base64.StdEncoding.EncodeToString(v.salt),
		Nonce: base64.StdEncoding.EncodeToString(nonce)}
	f.Data = base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, plain, f.aad()))
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(v.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "vault-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), v.Path); err != nil {
		return err
	}
	v.statLocked()
	return nil
}

// Validate checks a credential before it is stored.
func (c *Credential) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	if !nameRE.MatchString(c.Name) {
		return errors.New("a credential's name uses letters, digits, dots, hyphens and underscores (up to 40)")
	}
	if c.Kind == "" {
		c.Kind = KindLogin
	}
	switch c.Kind {
	case KindLogin:
		if strings.TrimSpace(c.Username) == "" {
			return errors.New("a login needs a username")
		}
	case KindSecret:
		if c.Username != "" {
			return errors.New("a secret is a single value and has no username")
		}
	case KindOAuth:
		if c.OAuth == nil || c.OAuth.TokenEndpoint == "" || c.OAuth.ClientID == "" || (c.OAuth.AccessToken == "" && c.OAuth.RefreshToken == "") {
			return errors.New("an OAuth sign-in needs a token endpoint, a client and a token")
		}
		if c.Secret != "" || c.Username != "" {
			return errors.New("an OAuth sign-in has no username or password")
		}
	default:
		return fmt.Errorf("the kind must be %q, %q or %q", KindLogin, KindSecret, KindOAuth)
	}
	if c.Kind != KindOAuth {
		if c.Secret == "" {
			return errors.New("there is no password or value to keep")
		}
		if len(c.Secret) > maxSecret {
			return fmt.Errorf("the value is too long (%d bytes at most)", maxSecret)
		}
		if c.OAuth != nil {
			return errors.New("only an OAuth sign-in carries OAuth data")
		}
	}
	if len(c.Sites) == 0 {
		return errors.New("say where it may be used (a site such as github.com): a credential with no site would work anywhere, including on a page made to steal it")
	}
	if len(c.Sites) > maxSites {
		return fmt.Errorf("at most %d sites", maxSites)
	}
	for i, s := range c.Sites {
		norm, err := normalizeSite(s)
		if err != nil {
			return err
		}
		c.Sites[i] = norm
	}
	return nil
}

// normalizeSite accepts a bare host (github.com: https only, and its
// subdomains) or a full origin (http://127.0.0.1:8000: exactly that).
func normalizeSite(s string) (string, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
			return "", fmt.Errorf("%q is not a site: give a host such as github.com, or an origin such as https://example.com:8443", s)
		}
		return u.Scheme + "://" + u.Host, nil
	}
	if s == "" || strings.ContainsAny(s, "/:@? *") || !strings.Contains(s, ".") {
		return "", fmt.Errorf("%q is not a site: give a host such as github.com (no path, no wildcard), or an origin such as http://localhost:3000", s)
	}
	return s, nil
}

// AllowsURL says whether the credential may be used on a page at rawURL, and
// if not, why. A bare host means https on that host or any subdomain of it;
// an origin means exactly that scheme, host and port.
func (c Credential) AllowsURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("the page %q is not a site this credential can be used on", rawURL)
	}
	host := strings.ToLower(u.Hostname())
	for _, site := range c.Sites {
		if strings.Contains(site, "://") {
			su, _ := url.Parse(site)
			if u.Scheme == su.Scheme && host == su.Hostname() && portOf(u) == portOf(su) {
				return nil
			}
			continue
		}
		if u.Scheme == "https" && portOf(u) == "443" && (host == site || strings.HasSuffix(host, "."+site)) {
			return nil
		}
	}
	return fmt.Errorf("the credential %q may only be used on %s, not on %s", c.Name, strings.Join(c.Sites, ", "), u.Scheme+"://"+u.Host)
}

func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "http" {
		return "80"
	}
	return "443"
}

// Add stores a credential, replacing one of the same name.
func (v *Vault) Add(c Credential) error {
	if err := c.Validate(); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	unlock, err := v.lockFile()
	if err != nil {
		return err
	}
	defer unlock()
	v.refreshLocked()
	c.Added = time.Now().UTC()
	v.creds[c.Name] = c
	if err := v.saveLocked(); err != nil {
		return err
	}
	v.announceLocked()
	return nil
}

// Update changes a credential in place and saves it, under the vault's lock (a
// file lock too, so another process is held off), after rereading the file, so two
// processes renewing a token do not overwrite each other. change may do slow work,
// such as asking a server for a new token: nothing else changes the vault meanwhile.
// It may not rename the credential. It reports an error if there is none
// by that name.
func (v *Vault) Update(name string, change func(*Credential) error) (Credential, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	unlock, err := v.lockFile()
	if err != nil {
		return Credential{}, err
	}
	defer unlock()
	v.refreshLocked()
	c, ok := v.creds[name]
	if !ok {
		return Credential{}, fmt.Errorf("no credential named %q", name)
	}
	if c.OAuth != nil { // work on a copy, so a failed change leaves the stored one alone
		o := *c.OAuth
		c.OAuth = &o
	}
	if err := change(&c); err != nil {
		return Credential{}, err
	}
	if c.Name != name {
		return Credential{}, errors.New("a credential cannot be renamed")
	}
	if err := c.Validate(); err != nil {
		return Credential{}, err
	}
	v.creds[name] = c
	if err := v.saveLocked(); err != nil {
		return Credential{}, err
	}
	v.announceLocked()
	return c, nil
}

// Remove deletes a credential.
func (v *Vault) Remove(name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	unlock, err := v.lockFile()
	if err != nil {
		return err
	}
	defer unlock()
	v.refreshLocked()
	if _, ok := v.creds[name]; !ok {
		return fmt.Errorf("no credential named %q", name)
	}
	delete(v.creds, name)
	return v.saveLocked()
}

// List returns what may be shown of every credential, by name.
func (v *Vault) List() []Info {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.refreshLocked()
	out := make([]Info, 0, len(v.creds))
	for _, c := range v.creds {
		out = append(out, Info{Name: c.Name, Kind: c.Kind, Sites: append([]string(nil), c.Sites...)})
	}
	slices.SortFunc(out, func(a, b Info) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// Get returns a credential, secret included. Only a tool that puts the secret
// where it belongs should call it.
func (v *Vault) Get(name string) (Credential, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.refreshLocked()
	c, ok := v.creds[name]
	return c, ok
}

// Usernames returns what the vault holds as usernames, for display at the
// command line.
func (v *Vault) Usernames() map[string]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := map[string]string{}
	for n, c := range v.creds {
		out[n] = c.Username
	}
	return out
}

// ChangePassphrase re-encrypts the vault under a new passphrase.
func (v *Vault) ChangePassphrase(passphrase string) error {
	if len([]rune(passphrase)) < MinPassphrase {
		return fmt.Errorf("the passphrase must be at least %d characters", MinPassphrase)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	unlock, err := v.lockFile()
	if err != nil {
		return err
	}
	defer unlock()
	v.refreshLocked()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key, err := deriveKey(passphrase, salt, iterations)
	if err != nil {
		return err
	}
	v.key, v.salt, v.iter = key, salt, iterations
	return v.saveLocked()
}

// Passphrase finds the vault's passphrase without asking: the environment
// variable TALARIA_VAULT_PASSPHRASE, else the contents of keyFile.
func Passphrase(getenv func(string) string, keyFile string) (string, error) {
	if p := getenv("TALARIA_VAULT_PASSPHRASE"); p != "" {
		return p, nil
	}
	if keyFile != "" {
		if fi, err := os.Stat(keyFile); err == nil && fi.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("the vault key file %s can be read by other users: run chmod 600 %s", keyFile, keyFile)
		}
		raw, err := os.ReadFile(keyFile)
		if err != nil {
			return "", fmt.Errorf("cannot read the vault key file: %w", err)
		}
		if p := strings.TrimSpace(string(raw)); p != "" {
			return p, nil
		}
		return "", fmt.Errorf("the vault key file %s is empty", keyFile)
	}
	return "", errors.New("no passphrase: set TALARIA_VAULT_PASSPHRASE, or vault.key_file in the config file")
}

// ForTests makes key derivation as quick as a file may declare (100,000
// rounds), for tests that create and open many vaults. It undoes itself when the
// test ends. Production code must never call it.
func ForTests(t interface{ Cleanup(func()) }) {
	old := iterations
	iterations = 100_000
	t.Cleanup(func() { iterations = old })
}
