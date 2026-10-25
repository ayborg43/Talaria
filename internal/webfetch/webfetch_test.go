package webfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchReadsPageText(t *testing.T) {
	testAllowPrivate = true
	defer func() { testAllowPrivate = false }()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>T</title><style>.x{}</style><script>var a=1;</script></head><body><h1>Price: 12</h1><p> In stock </p></body></html>`))
	}))
	defer s.Close()
	text, err := Fetch(context.Background(), s.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Price: 12", "In stock"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	for _, gone := range []string{"var a=1", ".x{}"} {
		if strings.Contains(text, gone) {
			t.Fatalf("scripts and styles are not page text:\n%s", text)
		}
	}
}

func TestFetchRefusesWhatItShouldNot(t *testing.T) {
	testAllowPrivate = false
	for _, url := range []string{
		"http://localhost/x", "http://127.0.0.1/", "http://169.254.169.254/latest",
		"http://[::1]/", "http://2130706433/", "ftp://example.com/x",
		"http://user:pass@example.com/", "gopher://example.com/",
	} {
		if _, err := Fetch(context.Background(), url); err == nil {
			t.Fatalf("%s was fetched", url)
		}
	}
}
