package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodPassword = "correct horse battery staple"

func newAuth(t *testing.T) (*Auth, *time.Time) {
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	return &Auth{Password: goodPassword, Path: filepath.Join(t.TempDir(), "sessions.json"), Now: func() time.Time { return now }, Delay: time.Millisecond}, &now
}

func reqWith(a *Auth, token string) *http.Request {
	r := httptest.NewRequest("GET", "/api/state", nil)
	if token != "" {
		r.AddCookie(a.Cookie(token))
	}
	return r
}

func TestOnlyTheRightPasswordSignsInAndTheCookieMeansNothingByItself(t *testing.T) {
	a, _ := newAuth(t)
	for _, bad := range []string{"", "wrong", goodPassword + " ", strings.ToUpper(goodPassword), goodPassword[:len(goodPassword)-1]} {
		if _, err := a.Login("1.1.1.1", bad); err == nil {
			t.Fatalf("%q signed in", bad)
		}
	}
	tok, err := a.Login("2.2.2.2", goodPassword)
	if err != nil || len(tok) < 40 {
		t.Fatalf("%q %v", tok, err)
	}
	if !a.Valid(reqWith(a, tok)) {
		t.Fatal("a fresh session is not valid")
	}
	for _, other := range []string{"", "x", tok + "x", tok[:len(tok)-1], strings.Repeat("A", 43)} {
		if a.Valid(reqWith(a, other)) {
			t.Fatalf("%q is valid", other)
		}
	}
	// Only a hash is kept: a copy of the file cannot sign anyone in.
	raw, _ := os.ReadFile(a.Path)
	if strings.Contains(string(raw), tok) || strings.Contains(string(raw), goodPassword) {
		t.Fatal("the sessions file holds the cookie's value or the password")
	}
	if st, _ := os.Stat(a.Path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	// And an empty password in the settings never lets an empty guess in.
	empty := &Auth{Password: "", Delay: time.Millisecond}
	if _, err := empty.Login("3.3.3.3", ""); err == nil {
		t.Fatal("no password set, and an empty one signed in")
	}
}

func TestGuessingIsLimitedPerAddressAndOverall(t *testing.T) {
	a, now := newAuth(t)
	for i := 0; i < ipFailLimit; i++ {
		a.Login("9.9.9.9", "nope")
	}
	_, err := a.Login("9.9.9.9", goodPassword) // even the right password is refused while locked
	var locked LockedError
	if e, ok := err.(LockedError); !ok || e.RetryAfter <= 0 || e.RetryAfter > ipLockout {
		t.Fatalf("%v", err)
	} else {
		locked = e
	}
	_ = locked
	if _, err := a.Login("8.8.8.8", goodPassword); err != nil {
		t.Fatalf("another address is locked out too: %v", err)
	}
	*now = now.Add(ipLockout + time.Second)
	if _, err := a.Login("9.9.9.9", goodPassword); err != nil {
		t.Fatalf("still locked after the time is up: %v", err)
	}

	// Many addresses, each a few tries: the total is limited too.
	b, _ := newAuth(t)
	for i := 0; i < globalLimit; i++ {
		b.Login("10.0.0."+string(rune('a'+i%26))+string(rune('a'+i/26)), "nope")
	}
	if _, err := b.Login("77.7.7.7", goodPassword); err == nil {
		t.Fatal("a flood of wrong guesses from many addresses did not slow anything")
	}
}

func TestSessionsLastEndAndSurviveARestart(t *testing.T) {
	a, now := newAuth(t)
	tok, _ := a.Login("1.1.1.1", goodPassword)
	b := &Auth{Password: goodPassword, Path: a.Path, Now: a.Now}
	if !b.Valid(reqWith(b, tok)) {
		t.Fatal("signed out by a restart")
	}
	*now = now.Add(20 * 24 * time.Hour)
	if !b.Valid(reqWith(b, tok)) { // this use renews it
		t.Fatal("a session in use ended")
	}
	*now = now.Add(29 * 24 * time.Hour)
	if !b.Valid(reqWith(b, tok)) {
		t.Fatal("the renewal did not happen")
	}
	*now = now.Add(31 * 24 * time.Hour)
	if b.Valid(reqWith(b, tok)) {
		t.Fatal("a session that was not used for a month is still valid")
	}

	tok2, _ := a.Login("1.1.1.1", goodPassword)
	r := reqWith(a, tok2)
	a.Logout(r)
	if a.Valid(reqWith(a, tok2)) {
		t.Fatal("still valid after signing out")
	}
	t3, _ := a.Login("1.1.1.1", goodPassword)
	t4, _ := a.Login("1.1.1.1", goodPassword)
	a.LogoutAll()
	if a.Valid(reqWith(a, t3)) || a.Valid(reqWith(a, t4)) {
		t.Fatal("sign out everywhere left a session")
	}
	for i := 0; i < maxSessions+5; i++ {
		a.Login("1.1.1.1", goodPassword)
	}
	if len(a.sessions) > maxSessions {
		t.Fatalf("%d sessions", len(a.sessions))
	}
}

func TestTheCookieIsLockedDown(t *testing.T) {
	a := &Auth{Secure: true}
	c := a.Cookie("abc")
	if c.Name != "__Host-tal_session" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.MaxAge <= 0 {
		t.Fatalf("%+v", c)
	}
	plain := (&Auth{}).Cookie("abc")
	if plain.Name != "tal_session" || !plain.HttpOnly || plain.SameSite != http.SameSiteStrictMode || plain.Secure {
		t.Fatalf("%+v", plain)
	}
	if gone := a.Cookie(""); gone.MaxAge >= 0 {
		t.Fatal("signing out does not remove the cookie")
	}
}

func TestPasswordsMustBeLongEnoughToStandAlone(t *testing.T) {
	for _, bad := range []string{"", "short", "elevenchars"} {
		if ValidatePassword(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := ValidatePassword("twelve chars"); err != nil {
		t.Error(err)
	}
}

func TestARequestMustComeFromTheAppItself(t *testing.T) {
	mk := func(hdr map[string]string) *http.Request {
		r := httptest.NewRequest("POST", "https://talaria.example.com/api/send", nil)
		r.Host = "talaria.example.com"
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	allowed := "https://talaria.example.com"
	cases := []struct {
		name string
		hdr  map[string]string
		ok   bool
	}{
		{"from the app", map[string]string{"X-Talaria": "1", "Origin": allowed}, true},
		{"the app, no origin sent", map[string]string{"X-Talaria": "1"}, true},
		{"another site", map[string]string{"X-Talaria": "1", "Origin": "https://evil.example"}, false},
		{"a lookalike", map[string]string{"X-Talaria": "1", "Origin": "https://talaria.example.com.evil.example"}, false},
		{"a plain form post", map[string]string{"Origin": allowed}, false},
		{"a form post from another site", map[string]string{"Origin": "https://evil.example"}, false},
		{"no markers at all", nil, false},
		{"the wrong marker", map[string]string{"X-Talaria": "0", "Origin": allowed}, false},
		{"the browser says cross-site", map[string]string{"X-Talaria": "1", "Sec-Fetch-Site": "cross-site"}, false},
		{"the browser says same-site but not same-origin", map[string]string{"X-Talaria": "1", "Sec-Fetch-Site": "same-site"}, false},
		{"the browser says same-origin", map[string]string{"X-Talaria": "1", "Sec-Fetch-Site": "same-origin"}, true},
		{"null origin", map[string]string{"X-Talaria": "1", "Origin": "null"}, false},
	}
	for _, c := range cases {
		if got := SameOrigin(mk(c.hdr), allowed); got != c.ok {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	// With no public address set, the host the request went to is the one that counts.
	r := mk(map[string]string{"X-Talaria": "1", "Origin": "http://talaria.example.com"})
	if !SameOrigin(r, "") {
		t.Error("same host, no public address configured")
	}
	r = mk(map[string]string{"X-Talaria": "1", "Origin": "http://evil.example"})
	if SameOrigin(r, "") {
		t.Error("another host accepted")
	}
}
