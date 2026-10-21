package vault

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func init() { iterations = 100_000 } // the smallest a file may declare; fast enough for tests

const pass = "correct horse battery"

func newVault(t *testing.T) (*Vault, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vault.enc")
	v, err := Create(path, pass)
	if err != nil {
		t.Fatal(err)
	}
	return v, path
}

var github = Credential{Name: "github", Username: "ada", Secret: "hunter2-very-secret", Sites: []string{"github.com"}}

func TestRoundTrip(t *testing.T) {
	v, path := newVault(t)
	if err := v.Add(github); err != nil {
		t.Fatal(err)
	}
	v.Add(Credential{Name: "api", Kind: KindSecret, Secret: "sk-abcdef123456", Sites: []string{"https://api.example.com:8443"}})

	w, err := Open(path, pass, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := w.Get("github")
	if !ok || c.Secret != "hunter2-very-secret" || c.Username != "ada" || c.Kind != KindLogin || c.Added.IsZero() {
		t.Fatalf("%+v %v", c, ok)
	}
	infos := w.List()
	if len(infos) != 2 || infos[0].Name != "api" || infos[1].Name != "github" || infos[0].Kind != KindSecret {
		t.Fatalf("%+v", infos)
	}
	if _, ok := w.Get("nope"); ok {
		t.Fatal("no such credential")
	}
}

func TestFileHoldsNothingReadable(t *testing.T) {
	v, path := newVault(t)
	v.Add(github)
	raw, _ := os.ReadFile(path)
	for _, leak := range []string{"hunter2", "github", "ada", "credentials", "secret"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("%q is readable in the vault file:\n%s", leak, raw)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	// Each save uses a fresh nonce, so two saves of the same contents differ.
	before := string(raw)
	v.Add(github)
	after, _ := os.ReadFile(path)
	if before == string(after) {
		t.Fatal("the same nonce was used twice")
	}
}

func TestWrongPassphraseAndDamage(t *testing.T) {
	v, path := newVault(t)
	v.Add(github)
	if _, err := Open(path, "the wrong passphrase", nil); err != ErrWrongPassphrase {
		t.Fatalf("%v", err)
	}
	raw, _ := os.ReadFile(path)
	var f fileFormat
	json.Unmarshal(raw, &f)

	tamper := map[string]func(*fileFormat){
		"ciphertext": func(f *fileFormat) {
			b, _ := base64.StdEncoding.DecodeString(f.Data)
			b[0] ^= 1
			f.Data = base64.StdEncoding.EncodeToString(b)
		},
		"nonce": func(f *fileFormat) {
			b, _ := base64.StdEncoding.DecodeString(f.Nonce)
			b[0] ^= 1
			f.Nonce = base64.StdEncoding.EncodeToString(b)
		},
		"salt":       func(f *fileFormat) { f.Salt = base64.StdEncoding.EncodeToString([]byte("another salt!!!!")) },
		"iterations": func(f *fileFormat) { f.Iterations++ },
		"truncated":  func(f *fileFormat) { f.Data = f.Data[:len(f.Data)/2] },
	}
	for name, change := range tamper {
		g := f
		change(&g)
		out, _ := json.Marshal(g)
		p := filepath.Join(t.TempDir(), "v")
		os.WriteFile(p, out, 0o600)
		if _, err := Open(p, pass, nil); err == nil {
			t.Errorf("%s: a changed vault opened", name)
		}
	}

	os.WriteFile(path, []byte("not json"), 0o600)
	if _, err := Open(path, pass, nil); err == nil || err == ErrWrongPassphrase {
		t.Fatalf("%v", err)
	}
	os.WriteFile(path, []byte(`{"version":9,"kdf":"pbkdf2-sha256","iterations":100000}`), 0o600)
	if _, err := Open(path, pass, nil); err == nil || !strings.Contains(err.Error(), "does not know") {
		t.Fatalf("%v", err)
	}
	os.WriteFile(path, []byte(`{"version":1,"kdf":"pbkdf2-sha256","iterations":3}`), 0o600)
	if _, err := Open(path, pass, nil); err == nil {
		t.Fatal("a file must not be able to ask for a trivial key derivation")
	}
	if _, err := Open(filepath.Join(t.TempDir(), "absent"), pass, nil); err != ErrNoVault {
		t.Fatalf("%v", err)
	}
}

func TestCreateRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v")
	if _, err := Create(path, "short"); err == nil {
		t.Fatal("a short passphrase")
	}
	if _, err := Create(path, pass); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(path, pass); err == nil {
		t.Fatal("must not overwrite an existing vault")
	}
}

func TestCredentialValidation(t *testing.T) {
	ok := func(c Credential) Credential {
		t.Helper()
		if err := c.Validate(); err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		return c
	}
	if c := ok(Credential{Name: "a", Username: "u", Secret: "s", Sites: []string{" GitHub.COM "}}); c.Kind != KindLogin || c.Sites[0] != "github.com" {
		t.Fatalf("%+v", c)
	}
	if c := ok(Credential{Name: "k", Kind: KindSecret, Secret: "s", Sites: []string{"http://LOCALHOST:3000/"}}); c.Sites[0] != "http://localhost:3000" {
		t.Fatalf("%+v", c)
	}
	for name, c := range map[string]Credential{
		"bad name":            {Name: "has space", Username: "u", Secret: "s", Sites: []string{"a.com"}},
		"empty name":          {Username: "u", Secret: "s", Sites: []string{"a.com"}},
		"no username":         {Name: "a", Secret: "s", Sites: []string{"a.com"}},
		"secret with user":    {Name: "a", Kind: KindSecret, Username: "u", Secret: "s", Sites: []string{"a.com"}},
		"unknown kind":        {Name: "a", Kind: "token", Secret: "s", Sites: []string{"a.com"}},
		"no secret":           {Name: "a", Username: "u", Sites: []string{"a.com"}},
		"huge secret":         {Name: "a", Username: "u", Secret: strings.Repeat("x", maxSecret+1), Sites: []string{"a.com"}},
		"no sites":            {Name: "a", Username: "u", Secret: "s"},
		"too many sites":      {Name: "a", Username: "u", Secret: "s", Sites: strings.Split(strings.Repeat("a.com,", 11), ",")[:11]},
		"wildcard site":       {Name: "a", Username: "u", Secret: "s", Sites: []string{"*.a.com"}},
		"path in site":        {Name: "a", Username: "u", Secret: "s", Sites: []string{"a.com/login"}},
		"no dot":              {Name: "a", Username: "u", Secret: "s", Sites: []string{"intranet"}},
		"scheme ftp":          {Name: "a", Username: "u", Secret: "s", Sites: []string{"ftp://a.com"}},
		"login in site":       {Name: "a", Username: "u", Secret: "s", Sites: []string{"https://u:p@a.com"}},
		"port without scheme": {Name: "a", Username: "u", Secret: "s", Sites: []string{"a.com:8443"}},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
}

func TestAllowsURL(t *testing.T) {
	gh := Credential{Name: "gh", Sites: []string{"github.com"}}
	local := Credential{Name: "local", Sites: []string{"http://127.0.0.1:8000", "https://login.example.com:8443"}}
	for _, tc := range []struct {
		c    Credential
		url  string
		want bool
	}{
		{gh, "https://github.com/login", true},
		{gh, "https://GITHUB.com/", true},
		{gh, "https://gist.github.com/x", true},
		{gh, "https://github.com:443/x", true},
		{gh, "http://github.com/login", false},          // https only for a bare host
		{gh, "http://github.com:443/login", false},      // whatever port it is on
		{gh, "https://github.com:8443/", false},         // and the ordinary port
		{gh, "https://evilgithub.com/", false},          // a suffix is not a subdomain
		{gh, "https://github.com.evil.example/", false}, // nor a prefix
		{gh, "https://evil.example/?u=github.com", false},
		{gh, "https://evil.example/github.com", false},
		{gh, "https://user@github.com@evil.example/", false},
		{gh, "javascript:alert(1)", false},
		{gh, "about:blank", false},
		{gh, "", false},
		{local, "http://127.0.0.1:8000/login", true},
		{local, "http://127.0.0.1:8001/login", false},
		{local, "https://127.0.0.1:8000/login", false},
		{local, "http://localhost:8000/", false},
		{local, "https://login.example.com:8443/", true},
		{local, "https://login.example.com/", false},
		{local, "https://sub.login.example.com:8443/", false}, // an origin means exactly that host
	} {
		err := tc.c.AllowsURL(tc.url)
		if (err == nil) != tc.want {
			t.Errorf("%v on %q: %v, want allowed=%v", tc.c.Sites, tc.url, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), tc.c.Secret) && tc.c.Secret != "" {
			t.Error("the refusal must not mention the secret")
		}
	}
	if err := gh.AllowsURL("https://evil.example/"); err == nil || !strings.Contains(err.Error(), "github.com") || !strings.Contains(err.Error(), "evil.example") {
		t.Fatalf("the refusal says where it may be used, and where it was asked: %v", err)
	}
}

func TestChangesByAnotherProcessAreNoticed(t *testing.T) {
	v, path := newVault(t)
	v.Add(github)
	other, err := Open(path, pass, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Add(Credential{Name: "later", Username: "u", Secret: "added-later-secret", Sites: []string{"later.example"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := v.Get("later"); !ok {
		t.Fatal("a running gateway should see a credential added at the command line")
	}
	other.Remove("github")
	if _, ok := v.Get("github"); ok {
		t.Fatal("and one removed")
	}
	// Merging: adding in the first handle after the other's changes keeps them.
	v.Add(Credential{Name: "third", Username: "u", Secret: "third-secret-123", Sites: []string{"third.example"}})
	if got := len(other.List()); got != 2 {
		t.Fatalf("both handles' changes survive: %d", got)
	}
}

func TestOnLoadHearsEverySecretWhenTheVaultIsReadOrChanges(t *testing.T) {
	v, path := newVault(t)
	v.Add(github)
	var mu sync.Mutex
	var heard [][]string
	hook := func(s []string) { mu.Lock(); heard = append(heard, append([]string(nil), s...)); mu.Unlock() }

	opened, err := Open(path, pass, hook)
	if err != nil {
		t.Fatal(err)
	}
	if len(heard) != 1 || len(heard[0]) != 1 || heard[0][0] != "hunter2-very-secret" {
		t.Fatalf("opening announces the secrets: %v", heard)
	}
	opened.Add(Credential{Name: "x", Username: "u", Secret: "another-secret-1", Sites: []string{"x.example"}})
	if last := heard[len(heard)-1]; len(last) != 2 {
		t.Fatalf("adding announces them again: %v", heard)
	}
	// And a change made elsewhere is announced when it is noticed.
	v.Add(Credential{Name: "y", Username: "u", Secret: "elsewhere-secret-1", Sites: []string{"y.example"}})
	opened.List()
	found := false
	for _, h := range heard {
		for _, s := range h {
			found = found || s == "elsewhere-secret-1"
		}
	}
	if !found {
		t.Fatalf("%v", heard)
	}
}

func TestChangePassphrase(t *testing.T) {
	v, path := newVault(t)
	v.Add(github)
	if err := v.ChangePassphrase("short"); err == nil {
		t.Fatal("too short")
	}
	if err := v.ChangePassphrase("a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, pass, nil); err != ErrWrongPassphrase {
		t.Fatalf("the old passphrase no longer works: %v", err)
	}
	w, err := Open(path, "a brand new passphrase", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := w.Get("github"); c.Secret != "hunter2-very-secret" {
		t.Fatal("the credentials survive")
	}
}

func TestPassphraseSources(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	os.WriteFile(keyFile, []byte("  from the key file \n"), 0o600)

	if p, err := Passphrase(env("TALARIA_VAULT_PASSPHRASE", "from env"), keyFile); err != nil || p != "from env" {
		t.Fatalf("the environment comes first: %q %v", p, err)
	}
	if p, err := Passphrase(env(), keyFile); err != nil || p != "from the key file" {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := Passphrase(env(), ""); err == nil || !strings.Contains(err.Error(), "TALARIA_VAULT_PASSPHRASE") {
		t.Fatalf("%v", err)
	}
	loose := filepath.Join(dir, "loose")
	os.WriteFile(loose, []byte("secret"), 0o644)
	if _, err := Passphrase(env(), loose); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("a key file anyone can read is refused: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, []byte("\n"), 0o600)
	if _, err := Passphrase(env(), empty); err == nil {
		t.Fatal("empty key file")
	}
	if _, err := Passphrase(env(), filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing key file")
	}
}

func oauthCred(name string) Credential {
	return Credential{Name: name, Kind: KindOAuth, Sites: []string{"https://mail.example.com"},
		OAuth: &OAuthData{TokenEndpoint: "https://auth.example.com/token", ClientID: "client-1", ClientSecret: "client-secret-value",
			AccessToken: "access-token-value-1", RefreshToken: "refresh-token-value-1"}}
}

func TestOAuthSignInsAreStoredEncryptedAndKeptApartFromLogins(t *testing.T) {
	v, path := newVault(t)
	if err := v.Add(oauthCred("oauth.mail")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for _, leak := range []string{"access-token-value", "refresh-token-value", "client-secret-value", "auth.example.com", "mail.example.com"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("%q is readable in the file", leak)
		}
	}
	w, _ := Open(path, pass, nil)
	c, ok := w.Get("oauth.mail")
	if !ok || c.Kind != KindOAuth || c.OAuth.RefreshToken != "refresh-token-value-1" || c.OAuth.ClientID != "client-1" {
		t.Fatalf("%+v %v", c, ok)
	}
	if c.Secret != "" {
		t.Fatal("a sign-in has no password")
	}
	for name, mutate := range map[string]func(*Credential){
		"no oauth data":     func(c *Credential) { c.OAuth = nil },
		"no token endpoint": func(c *Credential) { c.OAuth.TokenEndpoint = "" },
		"no client":         func(c *Credential) { c.OAuth.ClientID = "" },
		"no tokens":         func(c *Credential) { c.OAuth.AccessToken, c.OAuth.RefreshToken = "", "" },
		"with a password":   func(c *Credential) { c.Secret = "x" },
		"with a username":   func(c *Credential) { c.Username = "x" },
	} {
		bad := oauthCred("x")
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
	// And OAuth data does not belong on a login.
	login := github
	login.OAuth = &OAuthData{TokenEndpoint: "x", ClientID: "y", AccessToken: "z"}
	if err := login.Validate(); err == nil {
		t.Fatal("a login with OAuth data")
	}
}

func TestOAuthTokensAreAnnouncedToTheRedactor(t *testing.T) {
	v, path := newVault(t)
	v.Add(oauthCred("oauth.mail"))
	var heard []string
	if _, err := Open(path, pass, func(s []string) { heard = append(heard, s...) }); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(heard, "|")
	for _, want := range []string{"access-token-value-1", "refresh-token-value-1", "client-secret-value"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s was not announced: %v", want, heard)
		}
	}
}

func TestUpdateChangesATokenAtomically(t *testing.T) {
	v, path := newVault(t)
	v.Add(oauthCred("oauth.mail"))
	var announced [][]string
	v.OnLoad = func(s []string) { announced = append(announced, s) }
	c, err := v.Update("oauth.mail", func(c *Credential) error {
		c.OAuth.AccessToken, c.OAuth.RefreshToken = "access-token-value-2", "refresh-token-value-2"
		return nil
	})
	if err != nil || c.OAuth.AccessToken != "access-token-value-2" {
		t.Fatalf("%+v %v", c, err)
	}
	w, _ := Open(path, pass, nil)
	if got, _ := w.Get("oauth.mail"); got.OAuth.RefreshToken != "refresh-token-value-2" {
		t.Fatalf("saved: %+v", got)
	}
	if len(announced) == 0 || !strings.Contains(strings.Join(announced[len(announced)-1], "|"), "access-token-value-2") {
		t.Fatalf("a renewed token is announced at once: %v", announced)
	}
	// A change that fails, or that breaks the credential, changes nothing.
	if _, err := v.Update("oauth.mail", func(c *Credential) error {
		c.OAuth.AccessToken = "never-saved-token"
		return errors.New("no")
	}); err == nil {
		t.Fatal("the error is passed on")
	}
	if _, err := v.Update("oauth.mail", func(c *Credential) error { c.OAuth.TokenEndpoint = ""; return nil }); err == nil {
		t.Fatal("an invalid result is refused")
	}
	if _, err := v.Update("oauth.mail", func(c *Credential) error { c.Name = "other"; return nil }); err == nil {
		t.Fatal("no renaming")
	}
	if got, _ := v.Get("oauth.mail"); got.OAuth.AccessToken != "access-token-value-2" || got.OAuth.TokenEndpoint == "" {
		t.Fatalf("%+v", got)
	}
	if _, err := v.Update("nope", func(*Credential) error { return nil }); err == nil {
		t.Fatal("no such credential")
	}
}

// Two processes renewing at once must not undo each other's change.
func TestUpdateRereadsBeforeChanging(t *testing.T) {
	v, path := newVault(t)
	v.Add(oauthCred("oauth.mail"))
	other, _ := Open(path, pass, nil)
	other.Add(Credential{Name: "later", Username: "u", Secret: "added-by-other-1", Sites: []string{"later.example"}})
	if _, err := v.Update("oauth.mail", func(c *Credential) error { c.OAuth.AccessToken = "renewed-token-1"; return nil }); err != nil {
		t.Fatal(err)
	}
	w, _ := Open(path, pass, nil)
	if _, ok := w.Get("later"); !ok {
		t.Fatal("the credential the other process added was lost")
	}
}
