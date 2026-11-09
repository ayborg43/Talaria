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

func TestSearchReturnsTitlesAddressesAndSnippets(t *testing.T) {
	testAllowPrivate = true
	defer func() { testAllowPrivate = false }()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "nigeria politics today" {
			t.Errorf("query not passed through: %s", r.URL)
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body>
			<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fnews&amp;rut=abc">Big <b>Story</b></a>
			<a class="result__snippet" href="//duckduckgo.com/l/?uddg=x">What happened, in brief.</a></div>
			<div class="result"><a class="result__a" href="https://other.example/item">Second</a>
			<a class="result__snippet">Another summary.</a></div>
			<div class="result"><a class="result__a" href="https://third.example/">Third has no snippet</a></div>
		</body></html>`))
	}))
	defer s.Close()
	old := searchEndpoint
	searchEndpoint = s.URL
	defer func() { searchEndpoint = old }()

	out, err := Search(context.Background(), "nigeria politics today")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Big Story", "https://example.com/news", "What happened, in brief.", "Second", "https://other.example/item", "Third has no snippet"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if _, err := Search(context.Background(), "  "); err == nil {
		t.Fatal("an empty search was run")
	}
}
