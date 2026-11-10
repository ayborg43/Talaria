package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A muse profile speaks Chat Completions to Meta Model API: the key as a
// bearer token, the model by its id, and a reasoning effort when one is set.
func TestMuseRequestsAreChatCompletionsWithTheKeyAndTheEffort(t *testing.T) {
	var got map[string]any
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		got = nil
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()
	ask := func(s Spec) map[string]any {
		s.Type, s.BaseURL, s.APIKey = TypeMuse, srv.URL+"/v1", "LLM_1607358788850350_secretsecretsecret"
		p, err := New(s)
		if err != nil {
			t.Fatal(err)
		}
		reply, err := p.Chat(context.Background(), Request{Model: "muse-spark-1.3", Messages: []Message{{Role: RoleSystem, Content: "be brief"}, {Role: RoleUser, Content: "hi"}}})
		if err != nil || reply.Content != "ok" {
			t.Fatalf("%+v %v", reply, err)
		}
		return got
	}
	g := ask(Spec{})
	if path != "/v1/chat/completions" || auth != "Bearer LLM_1607358788850350_secretsecretsecret" || g["model"] != "muse-spark-1.3" {
		t.Fatalf("path %q auth %q body %v", path, auth, g)
	}
	if g["reasoning_effort"] != nil || g["thinking"] != nil {
		t.Fatalf("with nothing set the service decides how hard to think: %v", g)
	}
	if g := ask(Spec{ReasoningEffort: "xhigh"}); g["reasoning_effort"] != "xhigh" || g["thinking"] != nil {
		t.Fatalf("%v", g)
	}
	// The models keep their reasoning to themselves: nothing is sent back with earlier turns.
	if raw, _ := json.Marshal(g["messages"]); strings.Contains(string(raw), "reasoning_content") {
		t.Fatalf("reasoning was sent back: %s", raw)
	}
}

func TestMuseDefaultsAndTheListOfTypes(t *testing.T) {
	if got := DefaultBaseURL(TypeMuse); got != "https://api.meta.ai/v1" {
		t.Fatalf("base url %q", got)
	}
	if !Reasons(TypeMuse) || !Reasons(TypeDeepSeek) || Reasons(TypeOpenAI) {
		t.Fatal("which types reason")
	}
	if _, err := New(Spec{Type: "gemini"}); err == nil || !strings.Contains(err.Error(), `"deepseek" or "muse"`) {
		t.Fatalf("the list of types names muse: %v", err)
	}
}

// The provider is asked which models a key can use, with the key, and the answer is tidied.
func TestListModelsAsksTheProviderWithTheKey(t *testing.T) {
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		if auth != "Bearer LLM_1_k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"object":"list","data":[{"id":"muse-spark-1.2"},{"id":"muse-ember-1.0"},{"id":"muse-spark-1.2"},{"id":""},{"id":"bad\nid"}]}`)
	}))
	defer srv.Close()
	got, err := ListModels(context.Background(), Spec{Type: TypeMuse, BaseURL: srv.URL + "/v1", APIKey: "LLM_1_k"})
	if err != nil || path != "/v1/models" || strings.Join(got, ",") != "muse-ember-1.0,muse-spark-1.2" {
		t.Fatalf("%v %q %v", got, path, err)
	}
	if _, err := ListModels(context.Background(), Spec{Type: TypeMuse, BaseURL: srv.URL + "/v1", APIKey: "wrong"}); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("a refused key: %v", err)
	}
}
