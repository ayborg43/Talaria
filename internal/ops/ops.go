// Package ops serves a gateway's health and metrics over HTTP, for a monitor
// to poll. It is off unless configured, listens on this machine only unless told
// otherwise, and a non-local listener requires a bearer token for /metrics.
package ops

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"talaria/internal/metrics"
	"talaria/internal/version"
)

// Server is the health and metrics endpoint.
type Server struct {
	Addr  string
	Token string // required as "Authorization: Bearer ..." on /metrics when Addr is not loopback
	Now   func() time.Time
	// Status adds what the caller knows, such as which services are connected, to /healthz.
	Status func() map[string]any

	started time.Time
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Validate refuses a listener that would put metrics on the network with no token.
func (s *Server) Validate() error {
	if _, _, err := net.SplitHostPort(s.Addr); err != nil {
		return fmt.Errorf("ops.listen %q: %w (write it as host:port, such as 127.0.0.1:9100)", s.Addr, err)
	}
	if !isLoopback(s.Addr) && s.Token == "" {
		return fmt.Errorf("ops.listen %q is not a local address: set ops.token_env so /metrics needs a token, or listen on 127.0.0.1", s.Addr)
	}
	return nil
}

// Handler is the HTTP handler: /healthz and /metrics.
func (s *Server) Handler() http.Handler {
	now := s.Now
	if now == nil {
		now = time.Now
	}
	if s.started.IsZero() {
		s.started = now()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"status": "ok", "version": version.String(), "uptime_seconds": int(now().Sub(s.started).Seconds())}
		if s.Status != nil {
			for k, v := range s.Status() {
				body[k] = v
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" {
			got, hasPrefix := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !hasPrefix || subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		metrics.Set("talaria_uptime_seconds", now().Sub(s.started).Seconds())
		metrics.Write(w)
	})
	return mux
}

// Run serves until ctx ends. logf says where it is listening.
func (s *Server) Run(ctx context.Context, logf func(string, ...any)) error {
	if err := s.Validate(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	logf("ops: health and metrics on http://%s (/healthz, /metrics)", ln.Addr())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
