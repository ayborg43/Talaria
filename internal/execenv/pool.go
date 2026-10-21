package execenv

import (
	"context"
	"errors"
	"sync"
)

var errPoolClosed = errors.New("the environments have been shut down")

// Pool starts environments the first time they are asked for and shares each
// one after that, so every chat that picks "sandbox" uses the same container.
//
// Starting can take long (pulling an image, connecting to a far host), so Get
// waits only as long as its context allows: the start itself carries on under
// the pool's own context, and a caller that gave up finds the environment ready
// next time. A start that fails is not remembered; the next Get tries again.
type Pool struct {
	open   func(ctx context.Context, name string) (Env, error)
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	entries map[string]*poolEntry
	closed  bool
	starts  sync.WaitGroup
}

type poolEntry struct {
	ready chan struct{} // closed when env and err are final
	env   Env
	err   error
}

// NewPool makes a pool that starts environments with open.
func NewPool(open func(ctx context.Context, name string) (Env, error)) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	return &Pool{open: open, ctx: ctx, cancel: cancel, entries: map[string]*poolEntry{}}
}

// Put adds an environment that is already running, which the pool will close.
func (p *Pool) Put(name string, env Env) {
	e := &poolEntry{ready: make(chan struct{}), env: env}
	close(e.ready)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries[name] = e
}

// Get returns the named environment, starting it if need be.
func (p *Pool) Get(ctx context.Context, name string) (Env, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errPoolClosed
	}
	e, ok := p.entries[name]
	if !ok {
		e = &poolEntry{ready: make(chan struct{})}
		p.entries[name] = e
		p.starts.Add(1)
		go p.start(name, e)
	}
	p.mu.Unlock()

	select {
	case <-e.ready:
		return e.env, e.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Pool) start(name string, e *poolEntry) {
	defer p.starts.Done()
	env, err := p.open(p.ctx, name)

	p.mu.Lock()
	if err == nil && p.closed { // shut down while it was starting
		go env.Close()
		env, err = nil, errPoolClosed
	}
	if err != nil {
		delete(p.entries, name)
	}
	e.env, e.err = env, err
	p.mu.Unlock()
	close(e.ready)
}

// Running reports whether the named environment is started and ready.
func (p *Pool) Running(name string) bool {
	p.mu.Lock()
	e, ok := p.entries[name]
	p.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case <-e.ready:
		return e.err == nil
	default:
		return false
	}
}

// Close stops every environment the pool holds, waiting for any that is still
// starting so that nothing is left running. It is safe to call twice.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()

	p.cancel()
	p.starts.Wait()

	p.mu.Lock()
	var envs []Env
	for _, e := range p.entries {
		select {
		case <-e.ready:
			if e.env != nil {
				envs = append(envs, e.env)
			}
		default:
		}
	}
	p.entries = map[string]*poolEntry{}
	p.mu.Unlock()

	for _, env := range envs {
		env.Close()
	}
}
