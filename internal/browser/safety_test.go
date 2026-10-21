package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func checker(allowPrivate bool) *Manager {
	return &Manager{Opts: Options{AllowPrivate: allowPrivate}, Resolve: func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "example.com", "www.example.com":
			return []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}, nil
		case "rebind.example":
			return []string{"93.184.216.34", "10.1.2.3"}, nil // one public address and one private
		case "intranet.example":
			return []string{"192.168.0.10"}, nil
		case "v6private.example":
			return []string{"fd12:3456::1"}, nil
		case "mapped.example":
			return []string{"::ffff:127.0.0.1"}, nil
		}
		return nil, errors.New("no such host")
	}}
}

func TestOnlyWebAddressesAreOpened(t *testing.T) {
	m := checker(false)
	for _, bad := range []string{
		"file:///etc/passwd", "chrome://settings", "chrome-extension://x/", "view-source:https://example.com", "javascript:alert(1)",
		"data:text/html,<script>1</script>", "ftp://example.com/", "about:blank", "example.com", "//example.com", "", "   ", "http://", "https:///path",
	} {
		if _, err := m.CheckURL(context.Background(), bad); err == nil {
			t.Errorf("%q should not be opened", bad)
		}
	}
	for _, good := range []string{"https://example.com", "http://example.com/path?q=1#x", " https://www.example.com/ ", "HTTPS://EXAMPLE.COM"} {
		if _, err := m.CheckURL(context.Background(), good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}

func TestLoginsInTheAddressAreRefused(t *testing.T) {
	_, err := checker(false).CheckURL(context.Background(), "https://alice:hunter2@example.com/")
	if err == nil || !strings.Contains(err.Error(), "user name or password") {
		t.Fatalf("%v", err)
	}
	// Even where private addresses are allowed: the problem is the login in the URL.
	if _, err := checker(true).CheckURL(context.Background(), "http://root:x@10.0.0.1/"); err == nil {
		t.Fatal("still refused")
	}
}

func TestPrivateAddressesAreRefused(t *testing.T) {
	m := checker(false)
	for _, bad := range []string{
		"http://localhost/", "http://LOCALHOST:8080/", "http://app.localhost/", "http://127.0.0.1/", "http://127.1.2.3:9000/", "http://[::1]/",
		"http://10.0.0.5/admin", "http://172.16.0.1/", "http://172.31.255.255/", "http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data/", "http://metadata.google.internal/", "http://host.docker.internal:8080/",
		"http://0.0.0.0/", "http://100.64.0.1/", "http://[fd00::1]/", "http://[fe80::1]/",
		"http://2130706433/", "http://0x7f000001/", "http://0x7f.0.0.1/", "http://017700000001/", "http://127.0.0.1./",
		"http://printer.local/", "http://db.internal/", "http://[::ffff:127.0.0.1]/", "http://[::ffff:10.0.0.1]/",
		"http://intranet.example/", "http://rebind.example/", "http://v6private.example/", "http://mapped.example/",
	} {
		if _, err := m.CheckURL(context.Background(), bad); err == nil {
			t.Errorf("%s should be refused", bad)
		}
	}
	for _, good := range []string{"http://93.184.216.34/", "http://172.32.0.1/", "http://[2606:2800:220:1::1]/", "https://example.com:8443/", "http://11.0.0.1/"} {
		if _, err := m.CheckURL(context.Background(), good); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
}

func TestTheRefusalSaysHowToAllowIt(t *testing.T) {
	_, err := checker(false).CheckURL(context.Background(), "http://intranet.example/")
	if err == nil || !strings.Contains(err.Error(), "192.168.0.10") || !strings.Contains(err.Error(), "browser.allow_private") {
		t.Fatalf("%v", err)
	}
}

func TestAllowPrivateOpensThem(t *testing.T) {
	m := checker(true)
	for _, u := range []string{"http://localhost:3000/", "http://10.0.0.5/", "http://intranet.example/", "http://169.254.169.254/"} {
		if _, err := m.CheckURL(context.Background(), u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	// Only the network check is lifted: other schemes stay refused.
	if _, err := m.CheckURL(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("file: is never allowed")
	}
}

// A name that does not resolve is left to the browser, whose error is clearer.
func TestUnresolvableNamesAreNotRefusedHere(t *testing.T) {
	if _, err := checker(false).CheckURL(context.Background(), "http://no-such-host.example/"); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestIsNumericHost(t *testing.T) {
	for host, want := range map[string]bool{
		"2130706433": true, "0x7f000001": true, "0x7F.0.0.1": true, "017700000001": true, "1.2.3.4": true,
		"example.com": false, "": false, "1.2.3.example": false, "0xzz": false, "1..2": false, "a.b": false, "123abc": false,
	} {
		if got := isNumericHost(host); got != want {
			t.Errorf("isNumericHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestTheMessageSaysWhyAnAddressWasRefused(t *testing.T) {
	m := checker(false)
	_, err := m.CheckURL(context.Background(), "file:///etc/passwd")
	if err == nil || !strings.Contains(err.Error(), "only http and https") {
		t.Fatalf("a file: address is refused for its scheme, not for lacking a host: %v", err)
	}
	if _, err := m.CheckURL(context.Background(), "example.com"); err == nil || !strings.Contains(err.Error(), "full URL") {
		t.Fatalf("%v", err)
	}
}
