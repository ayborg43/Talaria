package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRedirectsAreNotFollowedSoKeysStayWhereTheyWereSent(t *testing.T) {
	var gotKey, gotAuth string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotAuth = r.Header.Get("x-api-key"), r.Header.Get("Authorization")
		io.WriteString(w, `{"content":[{"type":"text","text":"hi"}]}`)
	}))
	defer other.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v1/messages", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	for name, p := range map[string]Provider{"anthropic": NewAnthropic(redirector.URL, "SECRET-KEY", 100), "openai": NewOpenAI(redirector.URL, "SECRET-KEY")} {
		gotKey, gotAuth = "", ""
		_, err := p.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hello"}}})
		if err == nil || !strings.Contains(err.Error(), "does not follow redirects") {
			t.Errorf("%s: %v", name, err)
		}
		if gotKey != "" || gotAuth != "" {
			t.Errorf("%s: the key reached the other host: %q %q", name, gotKey, gotAuth)
		}
	}
}

func TestAStreamThatNumbersToolCallsAbsurdlyIsRefusedNotAllocated(t *testing.T) {
	for _, idx := range []string{"-1", "2000000000", "128"} {
		srv := sseServer(t, fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":%s,"id":"c","function":{"name":"x","arguments":"{}"}}]}}]}`, idx), `[DONE]`)
		done := make(chan error, 1)
		go func() { _, _, err := stream(t, srv); done <- err }()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "refusing") {
				t.Errorf("index %s: %v", idx, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("index %s: it tried to allocate", idx)
		}
	}
	srv := sseServer(t, `{"choices":[{"delta":{"tool_calls":[{"index":127,"id":"c","function":{"name":"x","arguments":"{}"}}]}}]}`, `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`, `[DONE]`)
	if msg, _, err := stream(t, srv); err != nil || len(msg.ToolCalls) != 128 {
		t.Fatalf("the highest allowed index works: %v", err)
	}
}

func TestAnthropicRefusesTooManyToolCalls(t *testing.T) {
	var events []string
	for i := 0; i <= maxToolCalls; i++ {
		events = append(events, fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"c%d","name":"x"}}`, i, i))
	}
	srv := sseServer(t, events...)
	_, err := NewAnthropic(srv.URL, "k", 10).Chat(context.Background(), Request{Model: "m", OnText: func(string) {}})
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("%v", err)
	}
}

func TestAStreamPastTheSizeLimitIsStopped(t *testing.T) {
	old := maxStream
	maxStream = 4096
	defer func() { maxStream = old }()
	chunk := `{"choices":[{"delta":{"content":"` + strings.Repeat("x", 500) + `"}}]}`
	var events []string
	for i := 0; i < 40; i++ {
		events = append(events, chunk)
	}
	srv := sseServer(t, events...)
	_, _, err := stream(t, srv)
	if err == nil || !strings.Contains(err.Error(), "larger than Talaria will accept") {
		t.Fatalf("%v", err)
	}
}

func TestAStreamThatKeepsTricklingStopsAtTheOverallLimit(t *testing.T) {
	oldIdle, oldTotal := streamIdle, streamTotal
	streamIdle, streamTotal = time.Minute, 150*time.Millisecond
	defer func() { streamIdle, streamTotal = oldIdle, oldTotal }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 100; i++ { // a comment now and then keeps the idle timer happy
			if _, err := io.WriteString(w, ": still here\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()
	start := time.Now()
	_, _, err := stream(t, &httptest.Server{URL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > 3*time.Second {
		t.Fatalf("%v after %s", err, time.Since(start))
	}
}
