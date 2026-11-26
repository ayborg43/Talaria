package provider

import (
	"context"
	"sync"
	"unicode/utf8"
)

// Label says what a request is for, so what it cost can be told apart: who asked (a chat, a scheduled job, a
// background task, a delegated helper), which named agent it ran as, and what kind of message the router took it for.
type Label struct {
	Source string // "chat", "job", "task", "delegate", "router", "eval"
	Agent  string // a saved agent's name, "" for the main one
	Kind   string // the router's kind of message ("code", "simple", ...), "" when not known
	// Background is true for work nobody is waiting on: a scheduled job, a task, a goal's check-in, a watch. It sticks:
	// a helper started from background work is background too. A spending limit pauses only this.
	Background bool
}

type labelKey struct{}

// WithLabel returns ctx carrying l. A field left empty keeps the one already on ctx, so an inner part (a delegated
// helper) can name the agent without losing the source.
func WithLabel(ctx context.Context, l Label) context.Context {
	if old, ok := ctx.Value(labelKey{}).(Label); ok {
		if l.Source == "" {
			l.Source = old.Source
		}
		if l.Agent == "" {
			l.Agent = old.Agent
		}
		if l.Kind == "" {
			l.Kind = old.Kind
		}
		l.Background = l.Background || old.Background
	}
	return context.WithValue(ctx, labelKey{}, l)
}

// LabelOf is the label on ctx; the zero Label when there is none.
func LabelOf(ctx context.Context) Label {
	l, _ := ctx.Value(labelKey{}).(Label)
	return l
}

// Spent is one answered request, as the meter reports it.
type Spent struct {
	Profile string
	Model   string
	Usage   Usage
	Label   Label
}

var (
	sinkMu sync.RWMutex
	sink   func(Spent)
)

// SetSink says where every metered request reports. Nil stops reporting. It is set once, at start.
func SetSink(f func(Spent)) {
	sinkMu.Lock()
	sink = f
	sinkMu.Unlock()
}

// Metered reports what each answered request used. A failed request reports nothing: it is not billed.
type Metered struct {
	P       Provider
	Profile string
	Model   string // what the service is asked for when the request does not name one
}

// Meter wraps p so that each answer is reported to the sink under the profile's name.
func Meter(p Provider, profile, model string) Provider {
	if p == nil {
		return nil
	}
	return Metered{P: p, Profile: profile, Model: model}
}

func (m Metered) Chat(ctx context.Context, req Request) (Message, error) {
	msg, err := m.P.Chat(ctx, req)
	if err != nil {
		return msg, err
	}
	sinkMu.RLock()
	f := sink
	sinkMu.RUnlock()
	if f != nil {
		u := msg.Usage
		if !u.Known() {
			u = estimate(req, msg)
		}
		model := req.Model
		if model == "" {
			model = m.Model
		}
		f(Spent{Profile: m.Profile, Model: model, Usage: u, Label: LabelOf(ctx)})
	}
	return msg, nil
}

// estimate guesses tokens from text, for a service that did not report them: about four characters to a token. It is
// marked as a guess, and is only meant to keep the totals in the right place.
func estimate(req Request, msg Message) Usage {
	in := 0
	for _, m := range req.Messages {
		in += utf8.RuneCountInString(m.Content) + 8
		for _, tc := range m.ToolCalls {
			in += utf8.RuneCountInString(tc.Name) + utf8.RuneCountInString(tc.Arguments)
		}
	}
	for _, t := range req.Tools {
		in += utf8.RuneCountInString(t.Name) + utf8.RuneCountInString(t.Description) + len(t.Parameters)
	}
	out := utf8.RuneCountInString(msg.Content)
	for _, tc := range msg.ToolCalls {
		out += utf8.RuneCountInString(tc.Name) + utf8.RuneCountInString(tc.Arguments)
	}
	return Usage{In: in / 4, Out: out / 4, Estimated: true}
}

// Unwrap is the provider inside a metered one, or p itself.
func Unwrap(p Provider) Provider {
	if m, ok := p.(Metered); ok {
		return m.P
	}
	return p
}
