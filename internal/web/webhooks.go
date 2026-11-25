package web

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"talaria/internal/cron"
	"talaria/internal/webhook"
)

// failWindow limits how often one address may guess at webhook secrets.
type failWindow struct {
	mu sync.Mutex
	m  map[string]*failCount
}

type failCount struct {
	n     int
	since time.Time
}

const (
	hookFailsPerMinute = 20
	hookFailTable      = 2000
)

// blocked says whether ip has failed too often lately; fail records one failure.
func (f *failWindow) blocked(ip string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.m[ip]
	return c != nil && time.Since(c.since) < time.Minute && c.n >= hookFailsPerMinute
}

func (f *failWindow) fail(ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil || len(f.m) > hookFailTable {
		f.m = map[string]*failCount{} // a bounded table: when full it starts over
	}
	c := f.m[ip]
	if c == nil || time.Since(c.since) >= time.Minute {
		c = &failCount{since: time.Now()}
		f.m[ip] = c
	}
	c.n++
}

// hookCall is a webhook being called by something outside. It is not behind the sign-in: the secret in the address (or
// in a bearer header) is what lets it in, it is checked in constant time, and it only ever starts a background task the
// owner described when making the webhook, with what was sent quoted to that task as untrusted data.
func (s *Server) hookCall(w http.ResponseWriter, r *http.Request) {
	if s.Hooks == nil || s.Sources.Webhook == nil {
		http.NotFound(w, r)
		return
	}
	ip := s.ip(r)
	if s.hookFails.blocked(ip) {
		w.Header().Set("Retry-After", "60")
		fail(w, http.StatusTooManyRequests, "too many wrong tries: wait a minute")
		return
	}
	id, secret := r.PathValue("id"), r.PathValue("secret")
	if secret == "" {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			secret = strings.TrimSpace(a[len("Bearer "):])
		}
	}
	h, out := s.Hooks.Call(id, secret)
	switch out {
	case webhook.Refused:
		s.hookFails.fail(ip)
		http.NotFound(w, r)
		return
	case webhook.Paused:
		fail(w, http.StatusServiceUnavailable, "this webhook is paused")
		return
	case webhook.TooMany:
		w.Header().Set("Retry-After", "3600")
		fail(w, http.StatusTooManyRequests, "this webhook was called too often this hour")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, webhook.MaxBody))
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, "that is more than a webhook takes (64 KB)")
		return
	}
	if q := r.URL.RawQuery; len(body) == 0 && q != "" && !strings.Contains(q, secret) { // a sender that can only put things in the address
		body = []byte(q)
	}
	if err := s.Sources.Webhook(h, webhook.Prompt(h, r.Header.Get("Content-Type"), body)); err != nil {
		if cron.IsBusy(err) {
			w.Header().Set("Retry-After", "60")
			fail(w, http.StatusTooManyRequests, "Talaria is busy with as many tasks as it may run at once: try again in a minute")
			return
		}
		s.logf("web: webhook %s could not start its task: %v", h.ID, err)
		fail(w, http.StatusInternalServerError, "it could not be started")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

type hookRow struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Prompt  string    `json:"prompt"`
	Agent   string    `json:"agent,omitempty"`
	Profile string    `json:"profile,omitempty"`
	Paused  bool      `json:"paused,omitempty"`
	Calls   int       `json:"calls"`
	Last    time.Time `json:"last,omitzero"`
	// URL is the address to call with the secret as a bearer token; the address with the secret in it is only given when
	// a webhook is made or its secret changed.
	URL string `json:"url"`
}

type hooksView struct {
	Available bool      `json:"available"`
	Hooks     []hookRow `json:"hooks"`
	Max       int       `json:"max"`
	Agents    []string  `json:"agents"`
	// New is set only on the answer to making or re-keying a webhook: the one time its secret is shown.
	New *hookSecret `json:"new,omitempty"`
	// Public says the address is one the outside can reach (https), so a service can call it.
	Public bool `json:"public"`
}

type hookSecret struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
	URL    string `json:"url"` // with the secret in it, for a sender that cannot send a header
}

func (s *Server) hookBase(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) hooksView(r *http.Request) hooksView {
	v := hooksView{Hooks: []hookRow{}, Max: webhook.Max, Agents: s.savedAgentNames()}
	if s.Hooks == nil || s.Sources.Webhook == nil {
		return v
	}
	v.Available, v.Public = true, strings.HasPrefix(s.hookBase(r), "https://")
	for _, h := range s.Hooks.List() {
		v.Hooks = append(v.Hooks, hookRow{ID: h.ID, Name: h.Name, Prompt: h.Prompt, Agent: h.Agent, Profile: h.Profile, Paused: h.Paused, Calls: h.Calls, Last: h.Last,
			URL: s.hookBase(r) + "/hooks/" + h.ID})
	}
	return v
}

func (s *Server) hooksGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.hooksView(r))
}

// hooksPost makes, re-keys, pauses and removes webhooks. Only the signed-in owner can: the agent has no tool for it,
// because a webhook is a door into Talaria and the person decides who gets a key.
func (s *Server) hooksPost(w http.ResponseWriter, r *http.Request) {
	if s.Hooks == nil || s.Sources.Webhook == nil {
		fail(w, http.StatusNotImplemented, "webhooks are not set up here")
		return
	}
	var in struct {
		Create *struct {
			Name    string `json:"name"`
			Prompt  string `json:"prompt"`
			Agent   string `json:"agent"`
			Profile string `json:"profile"`
		} `json:"create"`
		Rotate string `json:"rotate"`
		Delete string `json:"delete"`
		Pause  string `json:"pause"`
		On     bool   `json:"on"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	v := func() hooksView { return s.hooksView(r) }
	switch {
	case in.Create != nil:
		agent := in.Create.Agent
		if agent != "" && !containsFold(s.savedAgentNames(), agent) {
			fail(w, http.StatusBadRequest, "that is not one of your saved agents")
			return
		}
		h, secret, err := s.Hooks.Create(in.Create.Name, in.Create.Prompt, agent, in.Create.Profile)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		out := v()
		out.New = &hookSecret{ID: h.ID, Secret: secret, URL: s.hookBase(r) + "/hooks/" + h.ID + "/" + secret}
		writeJSON(w, http.StatusOK, out)
	case in.Rotate != "":
		secret, ok, err := s.Hooks.Rotate(in.Rotate)
		switch {
		case !ok:
			fail(w, http.StatusNotFound, "that webhook is gone")
		case err != nil:
			fail(w, http.StatusInternalServerError, "the new secret could not be saved")
		default:
			out := v()
			out.New = &hookSecret{ID: in.Rotate, Secret: secret, URL: s.hookBase(r) + "/hooks/" + in.Rotate + "/" + secret}
			writeJSON(w, http.StatusOK, out)
		}
	case in.Delete != "":
		if !s.Hooks.Delete(in.Delete) {
			fail(w, http.StatusNotFound, "that webhook is already gone")
			return
		}
		writeJSON(w, http.StatusOK, v())
	case in.Pause != "":
		if !s.Hooks.Pause(in.Pause, in.On) {
			fail(w, http.StatusNotFound, "that webhook is gone")
			return
		}
		writeJSON(w, http.StatusOK, v())
	default:
		fail(w, http.StatusBadRequest, "nothing to do")
	}
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// safePath is a request path as it may be written to a log: a webhook's secret, when it is in the address, is left out.
func safePath(p string) string {
	if rest, ok := strings.CutPrefix(p, "/hooks/"); ok {
		id, _, hasSecret := strings.Cut(rest, "/")
		if hasSecret {
			return "/hooks/" + id + "/[secret]"
		}
	}
	return p
}
