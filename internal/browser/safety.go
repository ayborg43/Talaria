package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// CheckURL decides whether the agent may open a URL.
//
// Only http and https are allowed: file:, chrome:, view-source:, javascript:
// and data: addresses can read local files, reach the browser's own pages, or
// run script outside any site. Unless AllowPrivate is set, addresses on this
// machine or a private network (including the cloud metadata address) are
// refused too, since a page the agent was tricked into opening could otherwise
// be used to reach services that trust anything on the network.
//
// This is a check on the address given and on where a navigation ended up. It
// does not see what a page loads for itself, and a name can change what it
// points to between this check and the browser's; the real boundary is the
// network the workspace container is given (see `network` under workspace).
func (m *Manager) CheckURL(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%q is not a web address (give a full URL such as https://example.com)", raw)
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("only http and https addresses can be opened, not %s:", u.Scheme)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%q is not a web address (give a full URL such as https://example.com)", raw)
	}
	if u.User != nil {
		return "", errors.New("addresses with a user name or password in them are not opened: they put a login in the URL, which ends up in logs and history")
	}
	if m.Opts.AllowPrivate {
		return u.String(), nil
	}
	if err := m.checkHost(ctx, u.Hostname()); err != nil {
		return "", err
	}
	return u.String(), nil
}

var privateNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24",
		"192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func privateAddr(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range privateNets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (m *Manager) checkHost(ctx context.Context, host string) error {
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	refuse := fmt.Errorf("%s is on this machine or a private network, which the browser is not allowed to open (set browser.allow_private to allow it)", host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || lower == "host.docker.internal" || lower == "metadata.google.internal" ||
		strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".local") {
		return refuse
	}
	for _, r := range lower {
		if r > 127 {
			// Chrome reads full-width digits and dots, Unicode look-alikes and the like as ordinary ones
			// (http://１２７.０.０.１ is 127.0.0.1), and this check cannot tell what it will make of them.
			return fmt.Errorf("%s has characters that are not plain ASCII. Use the punycode form (xn--...) of the name, or the address as numbers, so it can be checked", host)
		}
	}
	if a, err := netip.ParseAddr(strings.Trim(lower, "[]")); err == nil {
		if privateAddr(a) {
			return refuse
		}
		return nil
	}
	// A bare number such as 2130706433 means 127.0.0.1 to a browser.
	if isNumericHost(lower) {
		return refuse
	}
	resolve := m.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context, h string) ([]string, error) {
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return net.DefaultResolver.LookupHost(rctx, h)
		}
	}
	addrs, err := resolve(ctx, lower)
	if err != nil {
		// Not looking is not the same as safe: a name this machine cannot resolve may still resolve, to
		// somewhere private, where the browser runs.
		return fmt.Errorf("%s could not be looked up from here (%v), so it cannot be checked against private addresses and was not opened", host, err)
	}
	for _, s := range addrs {
		if a, err := netip.ParseAddr(s); err == nil && privateAddr(a) {
			return fmt.Errorf("%s points to %s, which is on this machine or a private network; the browser is not allowed to open it (set browser.allow_private to allow it)", host, a)
		}
	}
	return nil
}

// isNumericHost reports whether host is a decimal, octal or hex number, or
// dotted parts that are, which browsers read as an IPv4 address.
func isNumericHost(host string) bool {
	if host == "" {
		return false
	}
	for _, part := range strings.Split(host, ".") {
		if part == "" {
			return false
		}
		p := strings.ToLower(part)
		hex := strings.HasPrefix(p, "0x")
		for i, r := range p {
			switch {
			case r >= '0' && r <= '9':
			case hex && i >= 2 && r >= 'a' && r <= 'f':
			case hex && i == 1 && r == 'x':
			default:
				return false
			}
		}
	}
	return true
}
