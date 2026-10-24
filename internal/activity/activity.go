// Package activity lets a front end show who is working right now: the main agent, the subagents it
// has handed work to, and the background tasks, goals, watches and scheduled jobs.
//
// Whatever starts a piece of work tells a Sink; a Sink that is nil is fine, and means nobody is watching.
package activity

import (
	"context"
	"time"
)

// Kinds of worker, as a front end names them.
const (
	Main  = "main"  // the agent talking with the user
	Sub   = "sub"   // a subagent the main agent handed a task to
	Task  = "task"  // a background task the user started from a chat
	Goal  = "goal"  // a check-in on a goal
	Watch = "watch" // a poll of a service
	Job   = "job"   // a scheduled job
)

// Worker is one thing working.
type Worker struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Doing is what it is in the middle of: a tool it is running, or what it waits for. Empty means thinking.
	Doing string `json:"doing,omitempty"`
	// Model is the model it works with, when known: helpers may use a different one from the main agent.
	Model string `json:"model,omitempty"`
	// AgeMs is how long it has been working, as of when it was reported. A page adds the time since, so
	// the phone's clock and the server's need not agree.
	AgeMs int64     `json:"ageMs"`
	Since time.Time `json:"-"`
}

// Sink is told about work starting, what it is doing, and its end.
type Sink interface {
	// Start says a worker has begun, and returns the number it is known by from now on.
	Start(kind, label string) int64
	// Doing says what it is in the middle of ("" for thinking).
	Doing(id int64, what string)
	// Stop says it has finished, however it ended.
	Stop(id int64)
}

// Modeler is an optional extra for a Sink that shows which model each worker uses.
type Modeler interface {
	SetModel(id int64, model string)
}

// SetModel says which model a worker uses, on a sink that may be nil or may not show it.
func SetModel(s Sink, id int64, model string) {
	if m, ok := s.(Modeler); ok && id != 0 && model != "" {
		m.SetModel(id, model)
	}
}

// Begin starts a worker on a sink that may be nil, and returns a function that ends it. Doing may be nil
// on the result of a nil sink; use the helpers below.
func Begin(s Sink, kind, label string) (id int64, end func()) {
	if s == nil {
		return 0, func() {}
	}
	id = s.Start(kind, label)
	return id, func() { s.Stop(id) }
}

// Note says what a worker is doing, on a sink that may be nil.
func Note(s Sink, id int64, what string) {
	if s != nil && id != 0 {
		s.Doing(id, what)
	}
}

type ctxKey struct{}

// With carries a worker's number with the context its work runs in, so code further down can report on it.
func With(ctx context.Context, id int64) context.Context { return context.WithValue(ctx, ctxKey{}, id) }

// ID is the number of the worker whose context this is, 0 if none.
func ID(ctx context.Context) int64 {
	id, _ := ctx.Value(ctxKey{}).(int64)
	return id
}
