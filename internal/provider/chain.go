package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// How long a model that failed is left alone before it is tried again. A rate limit or an outage usually passes
// within minutes; a refused key, a missing model or an unpaid account will not pass by itself, so it is tried less often.
var (
	SoftCooldown = 90 * time.Second
	HardCooldown = 10 * time.Minute
)

// Link is one model in a chain: the profile it came from, what it is called to the API, and how to reach it.
type Link struct {
	Name     string
	Model    string
	Provider Provider
}

// State is what is known about one profile's model right now.
type State struct {
	Name    string    `json:"name"`
	Working bool      `json:"working"`
	Reason  string    `json:"reason,omitempty"` // why it failed last, in a line
	Hard    bool      `json:"hard,omitempty"`   // a failure that will not clear by itself: a key, a model name, billing
	Since   time.Time `json:"since,omitempty"`  // when it started failing
	Until   time.Time `json:"until,omitempty"`  // when it will be tried again
	LastOK  time.Time `json:"lastOk,omitempty"`
}

// Served is the model of a chain that is answering, or answered last.
type Served struct{ Name, Model string }

// Health is what the chains learn about the models, shared so Settings and the agent can read it and so a model
// that has just failed is not tried again by the next call.
type Health struct {
	mu     sync.Mutex
	state  map[string]*State
	served map[string]Served // by the chain's first profile
	now    func() time.Time
}

// NewHealth makes an empty record.
func NewHealth() *Health {
	return &Health{state: map[string]*State{}, served: map[string]Served{}, now: time.Now}
}

// attempt notes which model of the chain that starts at primary is being asked now. The page names the model that
// answered from this, so a reply that moved on to a fallback says so.
func (h *Health) attempt(primary string, l Link) {
	h.mu.Lock()
	h.served[primary] = Served{Name: l.Name, Model: l.Model}
	h.mu.Unlock()
}

// ServedBy is the model of the chain starting at primary that answered last, and whether any has.
func (h *Health) ServedBy(primary string) (Served, bool) {
	if h == nil {
		return Served{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.served[primary]
	return s, ok
}

func (h *Health) fail(name, reason string, hard bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.state[name]
	if s == nil {
		s = &State{Name: name}
		h.state[name] = s
	}
	if s.Working || s.Since.IsZero() {
		s.Since = h.now()
	}
	s.Working, s.Reason, s.Hard = false, reason, hard
	cool := SoftCooldown
	if hard {
		cool = HardCooldown
	}
	s.Until = h.now().Add(cool)
}

func (h *Health) ok(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.state[name]
	if s == nil {
		s = &State{Name: name}
		h.state[name] = s
	}
	s.Working, s.Reason, s.Hard, s.Since, s.Until, s.LastOK = true, "", false, time.Time{}, time.Time{}, h.now()
}

// resting says whether a model failed recently enough to be left alone for now.
func (h *Health) resting(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.state[name]
	return s != nil && !s.Working && h.now().Before(s.Until)
}

// Get is what is known about one profile; a profile never used is working as far as anyone knows.
func (h *Health) Get(name string) State {
	if h == nil {
		return State{Name: name, Working: true}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.state[name]; s != nil {
		return *s
	}
	return State{Name: name, Working: true}
}

// Forget drops what is known about a profile, for one that was just edited: its new key may fix it.
func (h *Health) Forget(name string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	delete(h.state, name)
	h.mu.Unlock()
}

// Chain tries its models in order and moves to the next when one is not working: down, overloaded, out of quota, with a
// key that was refused, or a model name the service does not know. The first model is the one asked for; the rest
// are what the person set as the fallbacks. A model that has just failed is skipped for a while rather than tried
// first on every request, unless every model in the chain is resting, and then they are tried in order anyway.
//
// A failure that is about the request, not the model (a bad argument, a conversation too long), is returned as it is:
// the next model would meet the same thing.
type Chain struct {
	Links  []Link
	Health *Health
	// OnSwitch runs when a model is given up on for the next one, with why.
	OnSwitch func(from, to Link, err error)
}

func (c Chain) Chat(ctx context.Context, req Request) (Message, error) {
	if len(c.Links) == 0 {
		return Message{}, errors.New("no model is set up")
	}
	order := make([]Link, 0, len(c.Links))
	for _, l := range c.Links {
		if c.Health == nil || !c.Health.resting(l.Name) {
			order = append(order, l)
		}
	}
	if len(order) == 0 {
		order = c.Links
	}
	var tried []string
	for i, l := range order {
		r := req
		if l.Model != "" {
			r.Model = l.Model // each model in the chain is asked for by its own name
		}
		spoke := false
		if req.OnText != nil {
			r.OnText = func(d string) { spoke = true; req.OnText(d) }
		}
		if c.Health != nil {
			c.Health.attempt(c.Links[0].Name, l)
		}
		msg, err := l.Provider.Chat(ctx, r)
		if err == nil {
			if c.Health != nil {
				c.Health.ok(l.Name)
			}
			return msg, nil
		}
		if ctx.Err() != nil {
			return Message{}, err
		}
		switchable, hard := Classify(err)
		// Once part of an answer has been shown, a second model's answer would be tacked on after it.
		if !switchable || spoke {
			return Message{}, err
		}
		if c.Health != nil {
			c.Health.fail(l.Name, Short(err), hard)
		}
		tried = append(tried, l.Name+": "+Short(err))
		if i+1 < len(order) && c.OnSwitch != nil {
			c.OnSwitch(l, order[i+1], err)
		}
	}
	if len(tried) == 1 {
		return Message{}, errors.New(tried[0])
	}
	return Message{}, fmt.Errorf("no model in the chain worked (%s)", strings.Join(tried, "; "))
}

// Classify says whether an error means this model is not working (so the next should be tried), and whether it is
// the kind that will not clear by itself.
func Classify(err error) (switchable, hard bool) {
	var se *StatusError
	if errors.As(err, &se) {
		switch {
		case retryable(se.Status):
			return true, false
		case se.Status == 401 || se.Status == 402 || se.Status == 403 || se.Status == 404:
			return true, true // a key, an unpaid account, a model the service does not have
		case se.Status == 400 && modelProblem(se.Error()):
			return true, true
		}
		return false, false
	}
	var ue *url.Error
	var ne net.Error
	switch {
	case errors.As(err, &ue), errors.As(err, &ne), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, context.DeadlineExceeded):
		return true, false
	}
	return false, false
}

// modelProblem recognises a 400 that is really "no such model", which some services answer with instead of a 404.
func modelProblem(msg string) bool {
	m := strings.ToLower(msg)
	if !strings.Contains(m, "model") {
		return false
	}
	for _, w := range []string{"not found", "does not exist", "unknown model", "invalid model", "not supported", "no access", "not available"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// Short says what went wrong in a line a person can read.
func Short(err error) string {
	var se *StatusError
	if errors.As(err, &se) {
		switch se.Status {
		case 401, 403:
			return fmt.Sprintf("the key was refused (%d)", se.Status)
		case 402:
			return "the account has no credit left (402)"
		case 404:
			return "the service does not have this model (404)"
		case 429:
			return "rate limited (429)"
		case 500, 502, 503, 504, 529:
			return fmt.Sprintf("the service is down or overloaded (%d)", se.Status)
		}
		return clip(se.Error(), 140)
	}
	return clip(err.Error(), 140)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
