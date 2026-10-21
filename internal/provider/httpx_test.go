package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var defaultBackoff = backoff

func noBackoff(t *testing.T) {
	old := backoff
	backoff = func(int, *http.Response) time.Duration { return 0 }
	t.Cleanup(func() { backoff = old })
}

func chat(url string) error {
	_, err := NewOpenAI(url, "k").Chat(context.Background(), Request{Model: "m"})
	return err
}

func TestRetriesTransientFailures(t *testing.T) {
	noBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"finally"}}]}`))
	}))
	defer srv.Close()

	msg, err := NewOpenAI(srv.URL, "k").Chat(context.Background(), Request{Model: "m"})
	if err != nil || msg.Content != "finally" || calls.Load() != 3 {
		t.Fatalf("msg=%q err=%v calls=%d", msg.Content, err, calls.Load())
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	noBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":{"message":"slow down"}}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	err := chat(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "slow down") || calls.Load() != maxAttempts {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	noBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	if err := chat(srv.URL); err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d, want one call", err, calls.Load())
	}
}

func TestRetryAfterAndBackoffSchedule(t *testing.T) {
	resp := func(h string) *http.Response { return &http.Response{Header: http.Header{"Retry-After": {h}}} }
	for _, c := range []struct {
		attempt int
		resp    *http.Response
		want    time.Duration
	}{
		{1, nil, time.Second},
		{2, nil, 2 * time.Second},
		{1, resp("7"), 7 * time.Second},
		{1, resp("9999"), 20 * time.Second}, // capped
		{1, resp("soon"), time.Second},      // unparseable: fall back to the schedule
	} {
		if got := defaultBackoff(c.attempt, c.resp); got != c.want {
			t.Errorf("attempt %d: got %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestRetryStopsWhenContextCancelled(t *testing.T) {
	old := backoff
	backoff = func(int, *http.Response) time.Duration { return time.Hour }
	defer func() { backoff = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := NewOpenAI(srv.URL, "k").Chat(ctx, Request{Model: "m"})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("should abort the backoff wait promptly, err=%v after %v", err, time.Since(start))
	}
}

func TestStalledStreamTimesOut(t *testing.T) {
	old := streamIdle
	streamIdle = 150 * time.Millisecond
	defer func() { streamIdle = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // then say nothing
	}))
	defer srv.Close()

	var got strings.Builder
	_, err := NewOpenAI(srv.URL, "k").Chat(context.Background(), Request{Model: "m", OnText: func(s string) { got.WriteString(s) }})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
	if got.String() != "hi" {
		t.Fatalf("text before the stall should still have been delivered, got %q", got.String())
	}
}

func TestSteadyStreamIsNotCutOffByIdleTimer(t *testing.T) {
	old := streamIdle
	streamIdle = 200 * time.Millisecond
	defer func() { streamIdle = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 6; i++ { // 6 x 80ms = 480ms, longer than the idle limit overall
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
			w.(http.Flusher).Flush()
			time.Sleep(80 * time.Millisecond)
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	msg, err := NewOpenAI(srv.URL, "k").Chat(context.Background(), Request{Model: "m", OnText: func(string) {}})
	if err != nil || msg.Content != "xxxxxx" {
		t.Fatalf("steady stream failed: %q %v", msg.Content, err)
	}
}

func TestErrorMessageExtraction(t *testing.T) {
	for in, want := range map[string]string{
		`{"error":{"message":"bad key","type":"auth"}}`:                     "bad key",
		`{"type":"error","error":{"type":"x","message":"model not found"}}`: "model not found",
		`{"error":"plain string"}`:                                          "plain string",
		"<html>502 Bad Gateway</html>":                                      "<html>502 Bad Gateway</html>",
		`{"unrelated":true}`:                                                `{"unrelated":true}`,
	} {
		if got := errorMessage([]byte(in)); got != want {
			t.Errorf("errorMessage(%q) = %q, want %q", in, got, want)
		}
	}
}
