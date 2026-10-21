package browser

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/vault"
)

const (
	thePassword = "correct-horse-battery-staple"
	theAPIKey   = "sk-live-0123456789abcdef"
)

// vaultRig is a browser rig with a real, encrypted vault behind the tools.
func vaultRig(t *testing.T) (*rig, *vault.Vault) {
	t.Helper()
	r := newRig(t, Options{})
	v, err := vault.Create(filepath.Join(t.TempDir(), "vault.enc"), "a long enough passphrase")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []vault.Credential{
		{Name: "login-test", Username: "ada", Secret: thePassword, Sites: []string{"http://login.test"}},
		{Name: "api", Kind: vault.KindSecret, Secret: theAPIKey, Sites: []string{"http://login.test"}},
	} {
		if err := v.Add(c); err != nil {
			t.Fatal(err)
		}
	}
	RegisterVault(r.reg, r.m, r.owner, v)
	return r, v
}

func (r *rig) typedTexts() []string {
	var out []string
	for _, c := range r.sent("Input.insertText") {
		out = append(out, c.Params["text"].(string))
	}
	return out
}

func TestLoggingInWithoutTheModelSeeingTheSecret(t *testing.T) {
	r, _ := vaultRig(t)
	var seen []string
	note := func(out string) string { seen = append(seen, out); return out }

	note(r.ok("browser_open", `{"url":"http://login.test/"}`))
	note(r.ok("vault_list", `{}`))
	user := note(r.ok("browser_fill", `{"ref":1,"credential":"login-test","field":"username"}`))
	pw := note(r.ok("browser_fill", `{"ref":2,"credential":"login-test","field":"password"}`))
	home := note(r.ok("browser_click", `{"ref":3}`))

	if !strings.Contains(user, "Filled the username of login-test") || !strings.Contains(pw, "Filled the password of login-test") {
		t.Fatalf("%s\n%s", user, pw)
	}
	if !strings.Contains(pw, `password field "Password" (filled in; the value is hidden)`) {
		t.Fatalf("the model learns the field is filled, not with what:\n%s", pw)
	}
	if !strings.Contains(home, "Page: Home") || !strings.Contains(home, "Welcome back.") {
		t.Fatalf("%s", home)
	}
	// The browser was given the real values...
	if got := strings.Join(r.typedTexts(), "|"); got != "ada|"+thePassword {
		t.Fatalf("what the browser received: %q", got)
	}
	// ...and nothing the model was shown contains the password.
	for _, out := range seen {
		if strings.Contains(out, thePassword) || strings.Contains(out, theAPIKey) {
			t.Fatalf("a secret reached the model:\n%s", out)
		}
	}
}

func TestVaultListNamesThingsButShowsNoValues(t *testing.T) {
	r, _ := vaultRig(t)
	out := r.ok("vault_list", `{}`)
	for _, want := range []string{"api | secret | for use on http://login.test", "login-test | login | for use on http://login.test"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, thePassword) || strings.Contains(out, theAPIKey) || strings.Contains(out, "ada") {
		t.Fatalf("%s", out)
	}
	empty := newRig(t, Options{})
	v, _ := vault.Create(filepath.Join(t.TempDir(), "v"), "a long enough passphrase")
	RegisterVault(empty.reg, empty.m, "o", v)
	if out := empty.ok("vault_list", `{}`); !strings.Contains(out, "tal vault add") {
		t.Fatal(out)
	}
}

// The page a credential is used on is checked, in the browser, at the time.
func TestACredentialIsNotUsedOnTheWrongSite(t *testing.T) {
	r, _ := vaultRig(t)
	r.ok("browser_open", `{"url":"http://evil.test/"}`)
	for _, field := range []string{"username", "password"} {
		_, err := r.call("browser_fill", `{"ref":2,"credential":"login-test","field":"`+field+`"}`)
		if err == nil || !strings.Contains(err.Error(), "may only be used on http://login.test") || !strings.Contains(err.Error(), "evil.test") {
			t.Fatalf("%s: %v", field, err)
		}
		if strings.Contains(err.Error(), thePassword) {
			t.Fatal("the refusal leaks the secret")
		}
	}
	if got := r.typedTexts(); len(got) != 0 {
		t.Fatalf("nothing may be typed on the wrong site: %q", got)
	}
}

func TestOnlyAPasswordFieldReceivesAPassword(t *testing.T) {
	r, _ := vaultRig(t)
	r.ok("browser_open", `{"url":"http://login.test/"}`)
	_, err := r.call("browser_fill", `{"ref":4,"credential":"login-test","field":"password"}`) // a plain text field
	if err == nil || !strings.Contains(err.Error(), "not a password field") {
		t.Fatalf("%v", err)
	}
	if _, err := r.call("browser_fill", `{"ref":4,"credential":"api","field":"value"}`); err == nil {
		t.Fatal("a saved key is a secret too")
	}
	if got := r.typedTexts(); len(got) != 0 {
		t.Fatalf("%q", got)
	}
	// A username may go anywhere that takes text.
	if _, err := r.call("browser_fill", `{"ref":4,"credential":"login-test","field":"username"}`); err != nil {
		t.Fatal(err)
	}
	// Into a password field, a saved key is fine.
	r.ok("browser_fill", `{"ref":2,"credential":"api","field":"value"}`)
}

// The listing said "password", but by the time the text goes in the page has
// made it a plain field, which would show it.
func TestAFieldThatChangedTypeIsCaughtAtTheLastMoment(t *testing.T) {
	r, _ := vaultRig(t)
	r.ok("browser_open", `{"url":"http://login.test/shifty"}`)
	_, err := r.call("browser_fill", `{"ref":1,"credential":"login-test","field":"password"}`)
	if err == nil || !strings.Contains(err.Error(), "not a password field now") {
		t.Fatalf("%v", err)
	}
	if got := r.typedTexts(); len(got) != 0 {
		t.Fatalf("%q", got)
	}
}

func TestFillArgumentsAreChecked(t *testing.T) {
	r, _ := vaultRig(t)
	r.ok("browser_open", `{"url":"http://login.test/"}`)
	for name, args := range map[string]string{
		"unknown credential": `{"ref":2,"credential":"nope","field":"password"}`,
		"value of a login":   `{"ref":2,"credential":"login-test","field":"value"}`,
		"password of a key":  `{"ref":2,"credential":"api","field":"password"}`,
		"username of a key":  `{"ref":1,"credential":"api","field":"username"}`,
		"stale element":      `{"ref":99,"credential":"login-test","field":"username"}`,
		"no field":           `{"ref":1,"credential":"login-test"}`,
	} {
		if _, err := r.call("browser_fill", args); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	_, err := r.call("browser_fill", `{"ref":2,"credential":"nope","field":"password"}`)
	if !strings.Contains(err.Error(), "saved: api, login-test") {
		t.Fatalf("a wrong name lists the right ones: %v", err)
	}
	if got := r.typedTexts(); len(got) != 0 {
		t.Fatalf("%q", got)
	}
	// And before any page is open there is nothing to fill.
	fresh, _ := vaultRig(t)
	if _, err := fresh.call("browser_fill", `{"ref":1,"credential":"login-test","field":"username"}`); err == nil || !strings.Contains(err.Error(), "Open a page first") {
		t.Fatalf("%v", err)
	}
}

func TestFillAsksFirstAndSaysWhatItWillDo(t *testing.T) {
	r, _ := vaultRig(t)
	tl, _ := r.reg.Get("browser_fill")
	if !tl.NeedsApproval || tl.Describe == nil {
		t.Fatal("filling a login is asked about")
	}
	r.ok("browser_open", `{"url":"http://login.test/"}`)
	d := tl.Describe([]byte(`{"ref":2,"credential":"login-test","field":"password"}`))
	for _, want := range []string{`fill the password of the saved credential "login-test"`, `password field "Password"`, "http://login.test/", "a site it is saved for"} {
		if !strings.Contains(d, want) {
			t.Errorf("missing %q in %q", want, d)
		}
	}
	if strings.Contains(d, thePassword) {
		t.Fatal("the prompt shows the secret")
	}
	r.ok("browser_open", `{"url":"http://evil.test/"}`)
	d = tl.Describe([]byte(`{"ref":2,"credential":"login-test","field":"password"}`))
	if !strings.Contains(d, "REFUSED") || !strings.Contains(d, "evil.test") {
		t.Fatalf("the user is warned before they are asked: %q", d)
	}
}

func TestVaultToolsComeOnlyWithAVault(t *testing.T) {
	plain := newRig(t, Options{})
	for _, name := range []string{"vault_list", "browser_fill"} {
		if _, ok := plain.reg.Get(name); ok {
			t.Errorf("%s without a vault", name)
		}
	}
	r, _ := vaultRig(t)
	for _, name := range []string{"vault_list", "browser_fill"} {
		if _, ok := r.reg.Get(name); !ok {
			t.Errorf("%s with a vault", name)
		}
	}
	if tl, _ := r.reg.Get("browser_fill"); !strings.HasPrefix(tl.Name, Prefix) {
		t.Fatal("browser_fill is covered by a browser_* rule, and kept from subagents")
	}
}

// A credential saved while Talaria runs is usable at once; one removed is gone.
func TestTheVaultIsReadAtUseTime(t *testing.T) {
	r, v := vaultRig(t)
	r.ok("browser_open", `{"url":"http://login.test/"}`)
	other, _ := vault.Open(v.Path, "a long enough passphrase", nil)
	other.Add(vault.Credential{Name: "second", Username: "grace", Secret: "grace-secret-value", Sites: []string{"http://login.test"}})
	r.ok("browser_fill", `{"ref":1,"credential":"second","field":"username"}`)
	other.Remove("second")
	if _, err := r.call("browser_fill", `{"ref":1,"credential":"second","field":"username"}`); err == nil {
		t.Fatal("removed credentials stop working")
	}
}

// A page can show a password it was given: a "show password" button turns the dots into text.
func TestNoScreenshotOfAPageWhereAPasswordWasFilled(t *testing.T) {
	r, _ := vaultRig(t)
	r.ok("browser_open", `{"url":"http://login.test/"}`)
	if _, err := r.call("browser_screenshot", `{}`); err != nil {
		t.Fatalf("a screenshot is fine before anything secret is on the page: %v", err)
	}
	r.ok("browser_fill", `{"ref":1,"credential":"login-test","field":"username"}`)
	if _, err := r.call("browser_screenshot", `{}`); err != nil {
		t.Fatalf("a username is not a secret: %v", err)
	}
	r.ok("browser_fill", `{"ref":2,"credential":"login-test","field":"password"}`)
	if _, err := r.call("browser_screenshot", `{}`); err == nil || !strings.Contains(err.Error(), "could show it") {
		t.Fatalf("%v", err)
	}
	if got := r.sent("Page.captureScreenshot"); len(got) != 2 {
		t.Fatalf("only the two before the password were taken: %d", len(got))
	}
	// Once the page changes, pictures are fine again.
	r.ok("browser_click", `{"ref":3}`)
	if _, err := r.call("browser_screenshot", `{}`); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestBrowserToolsMarkTheConversationAsHavingReadOutsideContent(t *testing.T) {
	r, _ := vaultRig(t)
	for _, name := range []string{"browser_open", "browser_snapshot", "browser_click", "browser_type", "browser_screenshot", "browser_fill"} {
		tl, ok := r.reg.Get(name)
		if !ok || !tl.Untrusted {
			t.Errorf("%s: %+v", name, tl.Untrusted)
		}
	}
	if tl, _ := r.reg.Get("vault_list"); tl.Untrusted {
		t.Error("the list of saved names is Talaria's own")
	}
}

func TestOpeningASiteNotVisitedBeforeIsAnEgressCall(t *testing.T) {
	r := newRig(t, Options{})
	open, _ := r.reg.Get("browser_open")
	if open.Egress == nil {
		t.Fatal("no egress check")
	}
	arg := func(u string) json.RawMessage { return json.RawMessage(`{"url":"` + u + `"}`) }
	if !open.Egress(arg("http://shop.test/")) {
		t.Fatal("a first visit is somewhere new")
	}
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	if open.Egress(arg("http://SHOP.test/other?page=2")) {
		t.Fatal("a page on a site already opened is not")
	}
	if !open.Egress(arg("http://evil.test/?stolen=data")) || !open.Egress(json.RawMessage(`{}`)) || !open.Egress(json.RawMessage(`nonsense`)) {
		t.Fatal("another site, or something unreadable, asks")
	}
}
