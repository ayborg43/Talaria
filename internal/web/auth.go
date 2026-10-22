package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	sessionDays   = 30
	minPassword   = 12
	ipFailLimit   = 5
	ipLockout     = 15 * time.Minute
	globalLimit   = 40
	globalWindow  = time.Hour
	maxSessions   = 20
	failureDelay  = 400 * time.Millisecond
	cookieName    = "tal_session"
	cookieNameSec = "__Host-tal_session"
)

// LockedError says sign-in is refused for now, and for how long.
type LockedError struct{ RetryAfter time.Duration }

func (e LockedError) Error() string {
	return fmt.Sprintf("too many wrong passwords: try again in %d minutes", int(e.RetryAfter.Minutes())+1)
}

var errWrongPassword = errors.New("wrong password")

// Auth signs one person in with a password and remembers them with a cookie.
//
// The password is compared without revealing, by timing, how much of it was right. Wrong guesses are
// limited for each address and overall, so a long password cannot be found by trying. What the cookie
// holds is a random value that means nothing by itself; only its hash is kept, so a copy of the
// sessions file cannot be used to sign in.
type Auth struct {
	Password string
	Path     string // where sessions are kept between runs; "" keeps them in memory only
	Secure   bool   // the page is served over https, so the cookie is for https only
	Now      func() time.Time
	// Delay is how long a wrong password waits before it is answered.
	Delay time.Duration

	mu       sync.Mutex
	loaded   bool
	sessions map[string]time.Time // hash of the cookie's value -> when it ends
	ipFails  map[string][]time.Time
	allFails []time.Time
}

// ValidatePassword says whether a password is long enough to be the only thing between the internet and an agent.
func ValidatePassword(p string) error {
	if len([]rune(p)) < minPassword {
		return fmt.Errorf("the web password must be at least %d characters: it is all that stands between the internet and your agent (a few random words make a good one)", minPassword)
	}
	return nil
}

func (a *Auth) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Auth) load() {
	if a.loaded {
		return
	}
	a.loaded = true
	a.sessions = map[string]time.Time{}
	a.ipFails = map[string][]time.Time{}
	if a.Path == "" {
		return
	}
	if raw, err := os.ReadFile(a.Path); err == nil {
		json.Unmarshal(raw, &a.sessions)
	}
	a.prune()
}

func (a *Auth) prune() {
	now := a.now()
	for k, end := range a.sessions {
		if now.After(end) {
			delete(a.sessions, k)
		}
	}
}

func (a *Auth) save() {
	if a.Path == "" {
		return
	}
	if raw, err := json.Marshal(a.sessions); err == nil {
		writeFile(a.Path, raw)
	}
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func (a *Auth) passwordOK(candidate string) bool {
	want, got := sha256.Sum256([]byte(a.Password)), sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

// Login checks a password from an address and, if right, starts a session and returns the cookie's value.
func (a *Auth) Login(ip, password string) (string, error) {
	a.mu.Lock()
	a.load()
	now := a.now()
	if wait := a.lockedFor(ip, now); wait > 0 {
		a.mu.Unlock()
		return "", LockedError{wait}
	}
	a.mu.Unlock()

	ok := a.Password != "" && a.passwordOK(password)

	a.mu.Lock()
	defer a.mu.Unlock()
	if !ok {
		a.ipFails[ip] = append(a.ipFails[ip], now)
		a.allFails = append(a.allFails, now)
		a.mu.Unlock()
		d := a.Delay
		if d == 0 {
			d = failureDelay
		}
		time.Sleep(d) // makes each guess cost time even where the limit is not reached
		a.mu.Lock()
		return "", errWrongPassword
	}
	delete(a.ipFails, ip)
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("no randomness: " + err.Error())
	}
	token := base64.RawURLEncoding.EncodeToString(b[:])
	a.prune()
	if len(a.sessions) >= maxSessions { // forget the oldest rather than grow without end
		var oldest string
		for k, end := range a.sessions {
			if oldest == "" || end.Before(a.sessions[oldest]) {
				oldest = k
			}
		}
		delete(a.sessions, oldest)
	}
	a.sessions[hashToken(token)] = now.Add(sessionDays * 24 * time.Hour)
	a.save()
	return token, nil
}

// lockedFor is how long sign-in is refused for an address: after a few wrong passwords from it, or
// after a great many from anywhere.
func (a *Auth) lockedFor(ip string, now time.Time) time.Duration {
	recent := func(list []time.Time, window time.Duration) []time.Time {
		out := list[:0:0]
		for _, t := range list {
			if now.Sub(t) < window {
				out = append(out, t)
			}
		}
		return out
	}
	a.ipFails[ip] = recent(a.ipFails[ip], ipLockout)
	if len(a.ipFails[ip]) == 0 {
		delete(a.ipFails, ip)
	}
	a.allFails = recent(a.allFails, globalWindow)
	if fails := a.ipFails[ip]; len(fails) >= ipFailLimit {
		return ipLockout - now.Sub(fails[len(fails)-1])
	}
	if len(a.allFails) >= globalLimit {
		return ipLockout - now.Sub(a.allFails[len(a.allFails)-1])
	}
	return 0
}

// Valid reports whether a request carries a live session.
func (a *Auth) Valid(r *http.Request) bool {
	token := a.tokenOf(r)
	if token == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load()
	end, ok := a.sessions[hashToken(token)]
	if !ok || a.now().After(end) {
		return false
	}
	// A session in use does not run out: it is renewed once a day.
	if renewed := a.now().Add(sessionDays * 24 * time.Hour); renewed.Sub(end) > 24*time.Hour {
		a.sessions[hashToken(token)] = renewed
		a.save()
	}
	return true
}

// Logout ends the session a request carries.
func (a *Auth) Logout(r *http.Request) {
	token := a.tokenOf(r)
	if token == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load()
	delete(a.sessions, hashToken(token))
	a.save()
}

// LogoutAll ends every session.
func (a *Auth) LogoutAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load()
	a.sessions = map[string]time.Time{}
	a.save()
}

func (a *Auth) name() string {
	if a.Secure {
		return cookieNameSec
	}
	return cookieName
}

func (a *Auth) tokenOf(r *http.Request) string {
	c, err := r.Cookie(a.name())
	if err != nil || len(c.Value) < 20 || len(c.Value) > 100 {
		return ""
	}
	return c.Value
}

// Cookie is the cookie that holds a session. It cannot be read by the page's scripts, is not sent from
// other sites, and with https is bound to this host alone.
func (a *Auth) Cookie(token string) *http.Cookie {
	c := &http.Cookie{Name: a.name(), Value: token, Path: "/", HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteStrictMode,
		MaxAge: sessionDays * 24 * 3600}
	if token == "" {
		c.MaxAge = -1
	}
	return c
}

// Origin checks that a request that changes something came from this app and not from another site.
// A page on another site can make the browser send a request here, with the cookie, if the cookie
// allowed it; these checks do not rely on the cookie alone.
func SameOrigin(r *http.Request, allowed string) bool {
	if r.Header.Get("X-Talaria") != "1" { // a header a page on another site cannot add without being asked first
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		return strings.EqualFold(o, allowed) || (allowed == "" && sameHost(o, r.Host))
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	return true
}

func sameHost(origin, host string) bool {
	for _, p := range []string{"https://", "http://"} {
		if rest, ok := strings.CutPrefix(strings.ToLower(origin), p); ok {
			return rest == strings.ToLower(host)
		}
	}
	return false
}
