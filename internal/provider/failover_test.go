package provider

import (
	"context"
	"errors"
	"net/url"
	"testing"
)

type scriptedProvider struct {
	err error
	msg Message
}

func (s scriptedProvider) Chat(context.Context, Request) (Message, error) { return s.msg, s.err }

func statusErr(code int) error {
	return &StatusError{Status: code, Err: errors.New("provider returned")}
}

func TestFailoverUsesSecondaryOnTransient(t *testing.T) {
	fellback := 0
	f := Failover{
		Primary:    scriptedProvider{err: statusErr(503)},
		Secondary:  scriptedProvider{msg: Message{Content: "backup"}},
		OnFallback: func(error) { fellback++ },
	}
	msg, err := f.Chat(context.Background(), Request{})
	if err != nil || msg.Content != "backup" || fellback != 1 {
		t.Fatalf("%q %v %d", msg.Content, err, fellback)
	}
}

func TestFailoverDoesNotTouchPermanentErrors(t *testing.T) {
	called := false
	f2 := Failover{
		Primary:   scriptedProvider{err: statusErr(401)},
		Secondary: testHook{called: &called},
	}
	if _, err := f2.Chat(context.Background(), Request{}); err == nil {
		t.Fatal("an auth error should surface, not fall back")
	}
	if called {
		t.Fatal("secondary ran for a permanent error")
	}
}

type testHook struct{ called *bool }

func (h testHook) Chat(context.Context, Request) (Message, error) {
	*h.called = true
	return Message{Content: "backup"}, nil
}

func TestFailoverUsesSecondaryOnNetworkErrors(t *testing.T) {
	called := false
	f := Failover{
		Primary:   scriptedProvider{err: &url.Error{Op: "Post", URL: "https://x", Err: errors.New("connection refused")}},
		Secondary: testHook{called: &called},
	}
	if _, err := f.Chat(context.Background(), Request{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("a network error should fall back")
	}
}

func TestFailoverReportsBothFailures(t *testing.T) {
	f := Failover{
		Primary:   scriptedProvider{err: statusErr(503)},
		Secondary: scriptedProvider{err: statusErr(500)},
	}
	_, err := f.Chat(context.Background(), Request{})
	if err == nil {
		t.Fatal("both failing should fail")
	}
}

func TestFailoverRespectsCancellation(t *testing.T) {
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := Failover{
		Primary:   scriptedProvider{err: context.Canceled},
		Secondary: testHook{called: &called},
	}
	f.Chat(ctx, Request{})
	if called {
		t.Fatal("a cancelled run should not fall back")
	}
}
