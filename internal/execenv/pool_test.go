package execenv

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubEnv is an environment that only counts being closed.
type stubEnv struct {
	Local
	name   string
	closed atomic.Int32
}

func (s *stubEnv) Close() { s.closed.Add(1) }

func TestPoolStartsOnceAndShares(t *testing.T) {
	var opens atomic.Int32
	p := NewPool(func(_ context.Context, name string) (Env, error) {
		opens.Add(1)
		return &stubEnv{name: name}, nil
	})
	defer p.Close()

	if p.Running("a") {
		t.Fatal("nothing is running before it is asked for")
	}
	first, err := p.Get(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := p.Get(context.Background(), "a")
	other, _ := p.Get(context.Background(), "b")
	if first != second || first == other || opens.Load() != 2 {
		t.Fatalf("each environment starts once and is shared: opens=%d same=%v distinct=%v", opens.Load(), first == second, first != other)
	}
	if !p.Running("a") || !p.Running("b") || p.Running("c") {
		t.Fatal("Running should reflect what has been started")
	}
}

func TestPoolStartsOnceWhenManyAskAtTheSameTime(t *testing.T) {
	var opens atomic.Int32
	release := make(chan struct{})
	p := NewPool(func(_ context.Context, name string) (Env, error) {
		opens.Add(1)
		<-release
		return &stubEnv{name: name}, nil
	})
	defer p.Close()

	got := make(chan Env, 10)
	for i := 0; i < 10; i++ {
		go func() {
			env, err := p.Get(context.Background(), "slow")
			if err != nil {
				t.Error(err)
			}
			got <- env
		}()
	}
	time.Sleep(100 * time.Millisecond) // let them all queue up behind the one start
	if p.Running("slow") {
		t.Fatal("still starting")
	}
	close(release)

	var first Env
	for i := 0; i < 10; i++ {
		env := <-got
		if first == nil {
			first = env
		} else if env != first {
			t.Fatal("everyone should get the same environment")
		}
	}
	if opens.Load() != 1 {
		t.Fatalf("started %d times; ten chats asking together must start it once", opens.Load())
	}
}

func TestPoolDoesNotRememberAFailure(t *testing.T) {
	var opens atomic.Int32
	p := NewPool(func(_ context.Context, name string) (Env, error) {
		if opens.Add(1) == 1 {
			return nil, errors.New("docker is not running")
		}
		return &stubEnv{name: name}, nil
	})
	defer p.Close()

	if _, err := p.Get(context.Background(), "a"); err == nil || err.Error() != "docker is not running" {
		t.Fatalf("got %v", err)
	}
	if p.Running("a") {
		t.Fatal("a failed start is not running")
	}
	if _, err := p.Get(context.Background(), "a"); err != nil || opens.Load() != 2 {
		t.Fatalf("once Docker is back, asking again should work: %v (opens %d)", err, opens.Load())
	}
}

// A caller that stops waiting must not abort the start: the environment is
// ready for whoever asks next.
func TestPoolStartSurvivesACallerGivingUp(t *testing.T) {
	release := make(chan struct{})
	var opens atomic.Int32
	p := NewPool(func(ctx context.Context, name string) (Env, error) {
		opens.Add(1)
		select {
		case <-release:
			return &stubEnv{name: name}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := p.Get(ctx, "a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a caller whose context ends stops waiting: %v", err)
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for !p.Running("a") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !p.Running("a") {
		t.Fatal("the start should have carried on after the caller left")
	}
	if _, err := p.Get(context.Background(), "a"); err != nil || opens.Load() != 1 {
		t.Fatalf("the next caller finds it ready without a second start: %v (opens %d)", err, opens.Load())
	}
}

func TestPoolPutAndClose(t *testing.T) {
	var opens atomic.Int32
	made := map[string]*stubEnv{}
	var mu sync.Mutex
	p := NewPool(func(_ context.Context, name string) (Env, error) {
		opens.Add(1)
		e := &stubEnv{name: name}
		mu.Lock()
		made[name] = e
		mu.Unlock()
		return e, nil
	})

	preloaded := &stubEnv{name: "default"}
	p.Put("default", preloaded)
	if env, err := p.Get(context.Background(), "default"); err != nil || env != preloaded || opens.Load() != 0 {
		t.Fatalf("an environment put in the pool is used as it is: %v opens=%d", err, opens.Load())
	}
	if _, err := p.Get(context.Background(), "later"); err != nil {
		t.Fatal(err)
	}

	p.Close()
	p.Close() // twice is fine
	if preloaded.closed.Load() != 1 || made["later"].closed.Load() != 1 {
		t.Fatalf("every environment is closed exactly once: %d, %d", preloaded.closed.Load(), made["later"].closed.Load())
	}
	if _, err := p.Get(context.Background(), "default"); err == nil {
		t.Fatal("a closed pool starts nothing")
	}
}

// Shutting down while an environment is still starting must not leave it running.
func TestPoolCloseWaitsForAStartAndClosesIt(t *testing.T) {
	release := make(chan struct{})
	env := &stubEnv{name: "slow"}
	p := NewPool(func(context.Context, string) (Env, error) {
		<-release
		return env, nil
	})

	getErr := make(chan error, 1)
	go func() { _, err := p.Get(context.Background(), "slow"); getErr <- err }()
	time.Sleep(50 * time.Millisecond)

	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close should wait for the start in progress")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close never finished")
	}

	deadline := time.Now().Add(2 * time.Second)
	for env.closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if env.closed.Load() != 1 {
		t.Fatalf("the environment that finished starting during shutdown must be closed, not leaked: %d", env.closed.Load())
	}
	if err := <-getErr; err == nil {
		t.Fatal("the caller waiting on a start that was overtaken by shutdown should get an error")
	}
}
