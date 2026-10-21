package ops

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"talaria/internal/metrics"
)

func get(t *testing.T, h http.Handler, path, auth string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b)
}

func TestHealthzReportsStatusVersionAndUptime(t *testing.T) {
	clock := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	s := &Server{Addr: "127.0.0.1:0", Now: func() time.Time { return clock }, Status: func() map[string]any { return map[string]any{"gateways": []string{"discord"}} }}
	h := s.Handler()
	clock = clock.Add(90 * time.Second)
	code, body := get(t, h, "/healthz", "")
	var got map[string]any
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || got["status"] != "ok" || got["uptime_seconds"] != float64(90) || got["version"] == "" || got["gateways"] == nil {
		t.Fatalf("%d %s", code, body)
	}
}

func TestMetricsServesCountersAndNeverTheirInputs(t *testing.T) {
	metrics.Reset()
	metrics.Inc("talaria_x_total", "result", "ok")
	h := (&Server{Addr: "127.0.0.1:0"}).Handler()
	code, body := get(t, h, "/metrics", "")
	if code != 200 || !strings.Contains(body, `talaria_x_total{result="ok"} 1`) || !strings.Contains(body, "talaria_uptime_seconds") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestMetricsTokenIsRequiredWhenSet(t *testing.T) {
	h := (&Server{Addr: "0.0.0.0:9100", Token: "s3cret"}).Handler()
	for _, auth := range []string{"", "Bearer wrong", "s3cret", "Bearer s3cret2", "Bearer "} {
		if code, _ := get(t, h, "/metrics", auth); code != 401 {
			t.Errorf("%q: %d", auth, code)
		}
	}
	if code, _ := get(t, h, "/metrics", "Bearer s3cret"); code != 200 {
		t.Fatal(code)
	}
	if code, _ := get(t, h, "/healthz", ""); code != 200 {
		t.Fatal("health needs no token, so a load balancer can use it")
	}
}

func TestListenerValidation(t *testing.T) {
	for addr, tokenless := range map[string]bool{"127.0.0.1:9100": true, "localhost:9100": true, "[::1]:9100": true, "0.0.0.0:9100": false, "192.168.1.5:9100": false, ":9100": false} {
		err := (&Server{Addr: addr}).Validate()
		if (err == nil) != tokenless {
			t.Errorf("%s without a token: %v", addr, err)
		}
		if err := (&Server{Addr: addr, Token: "t"}).Validate(); err != nil {
			t.Errorf("%s with a token: %v", addr, err)
		}
	}
	if err := (&Server{Addr: "9100"}).Validate(); err == nil || !strings.Contains(err.Error(), "host:port") {
		t.Fatalf("%v", err)
	}
}

func TestRunServesAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{Addr: "127.0.0.1:0"}
	addrs := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx, func(f string, a ...any) { addrs <- a[0].(interface{ String() string }).String() })
	}()
	var addr string
	select {
	case addr = <-addrs:
	case err := <-done:
		t.Fatalf("stopped at once: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("never listened")
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v", err)
	}
	resp.Body.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
