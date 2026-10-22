package calendar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	maxFeedBytes = 8 << 20
	fetchTimeout = 20 * time.Second
	maxRedirects = 3
)

// Source is one calendar to read. The address of a private feed is a secret: whoever has it can
// read the calendar. It is never written into an error, a log or what the model is shown.
type Source struct {
	Name string
	URL  string
	// AllowPrivate lets the feed be on this machine or a private network, for a calendar you host yourself.
	// Off by default, so a feed address cannot be used to reach services that trust the local network.
	AllowPrivate bool
}

// NormalizeURL accepts https:// and webcal:// addresses, and returns the https form.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "webcal://") {
		raw = "https://" + raw[len("webcal://"):]
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("the calendar address is not a web address")
	}
	if u.Scheme != "https" {
		return "", errors.New("the calendar address must start with https:// (or webcal://): a private calendar address sent over plain http can be read by anyone on the way")
	}
	return u.String(), nil
}

// forbiddenAddr reports whether an address is one a feed may not be fetched from.
func forbiddenAddr(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) // 100.64.0.0/10, shared address space
}

// safeClient fetches a feed over https only, never to a private address (checked when connecting, so
// a name that later resolves somewhere private is caught too), without proxies, with a few redirects
// that must stay on https.
func safeClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(host); ip == nil || forbiddenAddr(ip) {
			return errors.New("the calendar address points at this machine or a private network, which is not allowed")
		}
		return nil
	}}
	return &http.Client{
		Timeout:   fetchTimeout,
		Transport: &http.Transport{DialContext: dialer.DialContext, Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, DisableKeepAlives: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("the calendar redirected to a page that is not https")
			}
			return nil
		},
	}
}

// fetch downloads one feed. Errors say what went wrong without repeating the address.
func fetch(ctx context.Context, hc *http.Client, src Source) (string, error) {
	u, err := NormalizeURL(src.URL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", errors.New("the calendar address is not usable")
	}
	req.Header.Set("Accept", "text/calendar, text/plain, */*")
	req.Header.Set("User-Agent", "Talaria")
	resp, err := hc.Do(req)
	if err != nil {
		return "", scrub(err, u)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusNotFound, http.StatusForbidden, http.StatusUnauthorized, http.StatusGone:
			return "", fmt.Errorf("the calendar address was refused (HTTP %d): it may have been reset, or the calendar's sharing turned off", resp.StatusCode)
		}
		return "", fmt.Errorf("the calendar server answered HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return "", scrub(err, u)
	}
	if len(raw) > maxFeedBytes {
		return "", fmt.Errorf("the calendar is larger than %d MB, which is more than Talaria reads", maxFeedBytes>>20)
	}
	return string(raw), nil
}

// scrub removes the feed's address from an error: the standard library puts the whole address in them.
func scrub(err error, u string) error {
	msg := err.Error()
	if parsed, perr := url.Parse(u); perr == nil {
		for _, s := range []string{u, parsed.Path, parsed.RawQuery, strings.TrimPrefix(parsed.Path, "/")} {
			if len(s) >= 6 {
				msg = strings.ReplaceAll(msg, s, "<calendar address>")
			}
		}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("could not reach the calendar: %s", strings.ReplaceAll(ue.Err.Error(), u, "<calendar address>"))
	}
	return errors.New(msg)
}
