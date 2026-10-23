package x

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"talaria/internal/tool"
)

// scripted pretends to be the X API: it answers users/me, mentions and tweets.
func scripted(t *testing.T, posted *[]map[string]any) *httptest.Server {
	t.Helper()
	var seen []map[string]any
	*posted = seen
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok123" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"title":"Unauthorized","detail":"bad token"}`))
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/users/me":
			w.Write([]byte(`{"data":{"id":"42","name":"Ada User","username":"ada"}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/mentions"):
			if !strings.Contains(r.URL.Path, "/users/42/mentions") {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"errors":[{"message":"unknown user"}]}`))
				return
			}
			data := `[{"id":"9","text":"hello there","author_id":"7","created_at":"2026-10-20T10:00:00.000Z"}]`
			if r.URL.Query().Get("since_id") == "9" {
				data = `[]`
			}
			w.Write([]byte(`{"data":` + data + `,"includes":{"users":[{"id":"7","username":"bob"}]},"meta":{"result_count":1}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/tweets":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("the post is not JSON: %v", err)
			}
			*posted = append(*posted, payload)
			w.Write([]byte(`{"data":{"id":"100","text":"hi"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errors":[{"message":"no such endpoint"}]}`))
		}
	}))
}

func testService(t *testing.T, srv *httptest.Server) *Service {
	t.Helper()
	return &Service{Client: &Client{Token: "tok123", BaseURL: srv.URL, HTTP: srv.Client()}}
}

func TestVerificationNamesTheAccount(t *testing.T) {
	var posted []map[string]any
	srv := scripted(t, &posted)
	defer srv.Close()
	got, err := testService(t, srv).Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "Posting as @ada." {
		t.Fatalf("it should name the account: %q", got)
	}
}

func TestABadTokenSaysSo(t *testing.T) {
	var posted []map[string]any
	srv := scripted(t, &posted)
	defer srv.Close()
	s := &Service{Client: &Client{Token: "wrong", BaseURL: srv.URL, HTTP: srv.Client()}}
	if _, err := s.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("a bad token should fail loudly: %v", err)
	}
}

func TestMentionsListNewestFirst(t *testing.T) {
	var posted []map[string]any
	srv := scripted(t, &posted)
	defer srv.Close()
	got, err := testService(t, srv).RecentMentions(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"id 9", "@bob", "hello there", "x_post"} {
		if !strings.Contains(got, want) {
			t.Fatalf("mentions should show %q:\n%s", want, got)
		}
	}
	got, err = testService(t, srv).RecentMentions(context.Background(), "9", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "No mentions") {
		t.Fatalf("nothing newer should say so:\n%s", got)
	}
}

func TestPostingValidatesBeforeSending(t *testing.T) {
	var posted []map[string]any
	srv := scripted(t, &posted)
	defer srv.Close()
	s := testService(t, srv)
	if _, err := s.Post(context.Background(), "   ", ""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty post should be refused: %v", err)
	}
	if _, err := s.Post(context.Background(), strings.Repeat("x", 281), ""); err == nil || !strings.Contains(err.Error(), "280") {
		t.Fatalf("a long post should be refused: %v", err)
	}
	if _, err := s.Post(context.Background(), "hi", "abc"); err == nil || !strings.Contains(err.Error(), "mention id") {
		t.Fatalf("a bad reply target should be refused: %v", err)
	}
	got, err := s.Post(context.Background(), "hi", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Posted as 100." {
		t.Fatalf("posting should say where it landed: %q", got)
	}
	if len(posted) != 1 || posted[0]["text"] != "hi" {
		t.Fatalf("one post should have gone out: %v", posted)
	}
	if _, err := s.Post(context.Background(), "hello", "9"); err != nil {
		t.Fatal(err)
	}
	reply, _ := posted[1]["reply"].(map[string]any)
	if reply["in_reply_to_tweet_id"] != "9" {
		t.Fatalf("a reply should point at the mention: %v", posted[1])
	}
}

func TestTheToolsAreRegisteredWithApprovalOnPosts(t *testing.T) {
	var posted []map[string]any
	srv := scripted(t, &posted)
	defer srv.Close()
	r := tool.NewRegistry()
	RegisterTools(r, testService(t, srv))
	mentions, ok := r.Get("x_mentions")
	if !ok {
		t.Fatal("x_mentions is missing")
	}
	if !mentions.Untrusted || !mentions.Parallel || mentions.NeedsApproval {
		t.Fatal("reading mentions is free but untrusted")
	}
	post, ok := r.Get("x_post")
	if !ok {
		t.Fatal("x_post is missing")
	}
	if !post.NeedsApproval || !post.Egress(nil) {
		t.Fatal("posting always asks first")
	}
	if got := post.Describe([]byte(`{"text":"hello world"}`)); got != `Post to X: "hello world"` {
		t.Fatalf("the approval prompt should be plain words: %q", got)
	}
	if got := post.Describe([]byte(`{"text":"hi","reply_to":"9"}`)); !strings.Contains(got, "mention 9") {
		t.Fatalf("a reply should name the mention: %q", got)
	}
	out, err := mentions.Execute(context.Background(), []byte(`{"limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text, "@bob") {
		t.Fatalf("the tool should list mentions:\n%s", out.Text)
	}
}
