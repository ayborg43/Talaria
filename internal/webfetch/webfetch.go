// Package webfetch reads one public web page as text: what the agent shows
// the model and what a watch compares. It is read-only, bounded and
// server-side safe: only http and https, no logins in URLs, no private or
// loopback addresses, no numeric-IP tricks, and the host is resolved and
// checked before anything is fetched. The rules mirror the browser's own
// safety checks; keep them in step.
package webfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"talaria/internal/tool"
)

// Limits keep one fetch small and quick.
const (
	// MaxBytes caps the body read: a page is skimmed, not archived.
	MaxBytes = 256 << 10
	// MaxText caps the text returned.
	MaxText = 32 << 10
)

// testAllowPrivate lets the tests use httptest's loopback server.
var testAllowPrivate bool

// privateNets refuses what IsPrivate misses: CGNAT, benchmark, reserved.
func privateNets() []netip.Prefix {
	out := []netip.Prefix{}
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "192.0.0.0/24",
		"198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func privateAddr(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || !a.IsValid() {
		return true
	}
	for _, p := range privateNets() {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// checkHost refuses names and addresses that are not public. It mirrors the
// browser's checkHost; keep the two in step.
func checkHost(ctx context.Context, host string) error {
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	refuse := fmt.Errorf("%s is on this machine or a private network, which pages are not fetched from", host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || lower == "host.docker.internal" || lower == "metadata.google.internal" ||
		strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".local") {
		if !testAllowPrivate {
			return refuse
		}
	}
	for _, r := range lower {
		if r > 127 {
			return fmt.Errorf("%s has characters that are not plain ASCII; use the punycode form (xn--...)", host)
		}
	}
	trimmed := strings.Trim(lower, "[]")
	if a, err := netip.ParseAddr(trimmed); err == nil {
		if !testAllowPrivate && privateAddr(a) {
			return refuse
		}
		return nil
	}
	if isNumericHost(trimmed) {
		return refuse
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(rctx, lower)
	if err != nil {
		return fmt.Errorf("%s could not be looked up from here (%v), so it cannot be checked and was not fetched", host, err)
	}
	for _, s := range addrs {
		if a, err := netip.ParseAddr(s); err == nil && privateAddr(a) {
			return fmt.Errorf("%s points to %s, which is on this machine or a private network", host, a)
		}
	}
	return nil
}

// isNumericHost reports dotted, decimal, octal or hex numbers, which network
// stacks read as IPv4 addresses (2130706433 is 127.0.0.1).
func isNumericHost(host string) bool {
	if host == "" {
		return false
	}
	all := true
	for _, r := range host {
		if !(r >= '0' && r <= '9' || r == '.' || r == 'x' || r == 'X' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' || r == 'b' || r == 'B' || r == 'o' || r == 'O') {
			all = false
		}
	}
	if !all {
		return false
	}
	if strings.Contains(host, ".") {
		for _, part := range strings.Split(host, ".") {
			if part == "" || !isNumPart(part) {
				return false
			}
		}
		return true
	}
	return isNumPart(host)
}

func isNumPart(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' || r == 'x' || r == 'X') {
			return false
		}
	}
	return true
}

// Fetch returns the page's text. Only public http(s) pages are fetched.
func Fetch(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%q is not a public web address (give a full http(s) URL)", raw)
	}
	if u.User != nil {
		return "", errors.New("addresses with a user name or password in them are not fetched: they put a login in the URL")
	}
	if err := checkHost(ctx, u.Hostname()); err != nil {
		return "", err
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Talaria/1 (watch)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/*;q=0.9,*/*;q=0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("the page could not be read: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("the page answered %s", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		main := strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
		if !strings.HasPrefix(main, "text/") && !strings.Contains(main, "html") && !strings.Contains(main, "xml") && !strings.Contains(main, "json") {
			return "", fmt.Errorf("only readable pages are fetched, not %s", main)
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > MaxBytes {
		return "", fmt.Errorf("the page is too big to watch (%d KB at most)", MaxBytes>>10)
	}
	return textify(body), nil
}

// RegisterTools adds the page reader: public pages only, no approval needed.
func RegisterTools(r *tool.Registry) {
	r.Register(tool.Tool{
		Name: "web_fetch",
		Description: "Read one public web page as text: headlines, prices, availability. " +
			"Only public http(s) pages; logins, private networks and files are refused. " +
			"Pair with watch_create to be told when the page changes.",
		Schema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"the full page address"}},"required":["url"]}`),
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			return Fetch(context.Background(), strings.TrimSpace(a.URL))
		},
	})
}
