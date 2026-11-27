package main

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"talaria/internal/agent"
	"talaria/internal/app"
	"talaria/internal/gateway/chat"
	"talaria/internal/memory"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

// The memory review. Nothing else makes learning happen: a model may write a
// note while it chats, but no one is obliged to. This pass runs on the web
// server's own clock, reads the conversations since the last pass, and updates
// MEMORY.md and USER.md with a cheap model that has no other tool.

const (
	// reviewEvery is how often a pass may read the history: often enough that
	// the notes keep up with the week, rarely enough to cost almost nothing.
	reviewEvery = 20 * time.Hour
	// reviewWindow is the longest stretch a pass looks back on when it has never
	// run before, so a first pass does not try to read a year of conversations.
	reviewWindow = 7 * 24 * time.Hour
	// reviewSessions and reviewChars bound what one pass reads, and so what it costs.
	reviewSessions = 8
	reviewChars    = 12000
)

// memoryReviewer runs the consolidation pass against the notes the runtime
// keeps. It is safe to call from several places at once: only one pass runs.
type memoryReviewer struct {
	rt       *app.Runtime
	backends *chat.Backends
	routine  func() string // the profile routine work runs on (a cheap one); nil is the chat's own
	logf     func(string, ...any)
	now      func() time.Time

	mu      sync.Mutex
	running bool
}

func newMemoryReviewer(rt *app.Runtime, backends *chat.Backends, routine func() string, logf func(string, ...any)) *memoryReviewer {
	return &memoryReviewer{rt: rt, backends: backends, routine: routine, logf: logf, now: time.Now}
}

// Run reviews the conversations since the last pass. A scheduled call does
// nothing when a pass already read the history recently; force is the person
// asking for one now. It returns what it did, or why it could not.
func (r *memoryReviewer) Run(ctx context.Context, force bool) (memory.Consolidation, error) {
	notes := r.rt.Notes()
	sessions := r.rt.Sessions()
	if sessions == nil {
		return memory.Consolidation{}, errors.New("the conversation history is not available here, so there is nothing to learn from")
	}
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return memory.Consolidation{}, errors.New("a review is already running")
	}
	r.running = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	now := r.now()
	since := notes.Reviewed()
	if since.IsZero() {
		since = now.Add(-reviewWindow)
	}
	if !force && now.Sub(since) < reviewEvery {
		return memory.Consolidation{Since: since, Until: now}, nil
	}
	be, err := r.backend()
	if err != nil {
		return memory.Consolidation{}, err
	}
	conversations, err := sessions.Since(since, reviewSessions, reviewChars)
	if err != nil {
		return memory.Consolidation{}, err
	}
	res := memory.Consolidation{Sessions: len(conversations), Since: since, Until: now}
	if len(conversations) == 0 {
		if err := notes.MarkReviewed(now); err != nil {
			return res, err
		}
		return res, nil
	}

	before := notes.Snapshot()
	reg := tool.NewRegistry()
	// The reviewer's only tool is the notes themselves: it cannot run a command,
	// read a file, or start anything, so the worst a confused pass can do is
	// write a bad note — which the person sees and can undo.
	memory.RegisterTools(reg, notes, nil, "consolidation", "")
	a := &agent.Agent{
		Provider: be.Provider,
		Model:    be.Model,
		Tools:    reg,
		System:   memory.ReviewSystem(notes.Prompt()),
		MaxSteps: 12,
		Approve:  func(string, string) bool { return false },
		// The review is still work the person pays for, and the same limits hold it back as any other.
		BeforeModel: r.rt.GateModel(),
	}
	ctx = provider.WithLabel(ctx, provider.Label{Source: "memory", Background: true})
	if _, err := a.Run(ctx, memory.ReviewPrompt(conversations)); err != nil {
		return res, err
	}
	if err := notes.MarkReviewed(now); err != nil {
		return res, err
	}
	res.Added, res.Changed, res.Removed = memory.Diff(before, notes.Snapshot())
	if r.logf != nil {
		r.logf("memory: %s", res.Summary())
	}
	return res, nil
}

// backend picks the model a review runs on: the cheap one routine work uses
// when that is set up, and the chat's own otherwise. A review that fails on the
// cheap model is not retried on an expensive one unasked.
func (r *memoryReviewer) backend() (chat.Backend, error) {
	if r.backends == nil || r.backends.Open == nil {
		return chat.Backend{}, errors.New("no model is set up to review the notes")
	}
	if name := r.profile(); name != "" && slices.Contains(r.backends.Names, name) {
		if be, err := r.backends.Open(name); err == nil {
			return be, nil
		}
	}
	be, err := r.backends.Open(r.backends.Default)
	if err != nil {
		return chat.Backend{}, err
	}
	return be, nil
}

func (r *memoryReviewer) profile() string {
	if r.routine == nil {
		return ""
	}
	return r.routine()
}
