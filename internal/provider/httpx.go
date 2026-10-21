package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Variables so tests can shrink them.
var (
	streamIdle   = 3 * time.Minute  // a stream with no bytes for this long is abandoned
	requestLimit = 10 * time.Minute // whole-request limit when not streaming
	backoff      = func(attempt int, resp *http.Response) time.Duration {
		if resp != nil {
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s >= 0 {
				return min(time.Duration(s)*time.Second, 20*time.Second)
			}
		}
		return time.Duration(1<<(attempt-1)) * time.Second
	}
)

const (
	maxAttempts = 3
	maxResponse = 10 << 20
)

// watchdog derives a context that is cancelled after d unless touch is called
// again in time. cancel releases it.
func watchdog(parent context.Context, d time.Duration) (ctx context.Context, cancel context.CancelFunc, touch func()) {
	ctx, cancelCtx := context.WithCancel(parent)
	timer := time.AfterFunc(d, cancelCtx)
	return ctx, func() { timer.Stop(); cancelCtx() }, func() { timer.Reset(d) }
}

// touchReader calls touch whenever bytes arrive.
type touchReader struct {
	r     io.Reader
	touch func()
}

func (t touchReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.touch()
	}
	return n, err
}

func retryable(status int) bool {
	switch status {
	case 429, 500, 502, 503, 504, 529: // 529: Anthropic "overloaded"
		return true
	}
	return false
}

// post sends a JSON body, retrying transient failures (network errors, rate
// limits, overload) before any of the response has been read.
func post(ctx context.Context, c *http.Client, url string, headers map[string]string, body []byte) (*http.Response, error) {
	if c == nil {
		c = http.DefaultClient
	}
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		var failure error
		var wait time.Duration
		resp, err := c.Do(req)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, err
			}
			failure, wait = err, backoff(attempt, nil)
		case resp.StatusCode/100 == 2:
			return resp, nil
		case retryable(resp.StatusCode):
			failure, wait = apiError(resp), backoff(attempt, resp)
		default:
			return nil, apiError(resp)
		}

		if attempt >= maxAttempts {
			return nil, failure
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// apiError reads and closes an error response.
func apiError(resp *http.Response) error {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return fmt.Errorf("provider returned %s: %s", resp.Status, errorMessage(raw))
}

// errorMessage pulls the human message out of {"error":{"message":...}} or
// {"error":"..."} bodies, falling back to the raw text.
func errorMessage(raw []byte) string {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && len(e.Error) > 0 {
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Error, &obj) == nil && obj.Message != "" {
			return obj.Message
		}
		var s string
		if json.Unmarshal(e.Error, &s) == nil && s != "" {
			return s
		}
	}
	return truncate(strings.TrimSpace(string(raw)), 500)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// roundTrip is the exchange both providers share: post the payload, then read
// either a streamed or a plain JSON reply. If streaming was requested but the
// server answered with plain JSON, the whole text is passed to onText at once.
func roundTrip(ctx context.Context, c *http.Client, url string, headers map[string]string, payload []byte,
	onText func(string), parse func([]byte) (Message, error), parseStream func(io.Reader) (Message, error)) (Message, error) {

	limit := requestLimit
	if onText != nil {
		limit = streamIdle
	}
	wctx, cancel, touch := watchdog(ctx, limit)
	defer cancel()

	msg, err := func() (Message, error) {
		resp, err := post(wctx, c, url, headers, payload)
		if err != nil {
			return Message{}, err
		}
		defer resp.Body.Close()

		if onText != nil && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			return parseStream(touchReader{resp.Body, touch})
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
		if err != nil {
			return Message{}, err
		}
		m, err := parse(raw)
		if err == nil && onText != nil && m.Content != "" {
			onText(m.Content)
		}
		return m, err
	}()
	if err != nil && ctx.Err() == nil && wctx.Err() != nil {
		err = fmt.Errorf("provider timed out (no data for %s)", limit)
	}
	return msg, err
}
