package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var (
	png  = Image{MIME: "image/png", Data: []byte("\x89PNG-one")}
	jpeg = Image{MIME: "image/jpeg", Data: []byte("\xff\xd8JPEG-two")}
)

func b64(img Image) string { return base64.StdEncoding.EncodeToString(img.Data) }

// capture runs one Chat against a server that records the request body.
func capture(t *testing.T, p func(url string) Provider, msgs []Message) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		if strings.Contains(r.URL.Path, "messages") {
			w.Write([]byte(`{"type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	if _, err := p(srv.URL).Chat(context.Background(), Request{Model: "m", Messages: msgs}); err != nil {
		t.Fatal(err)
	}
	return body
}

func openAI(noVision bool) func(string) Provider {
	return func(url string) Provider { o := NewOpenAI(url, "k"); o.NoVision = noVision; return o }
}

func anthropic(noVision bool) func(string) Provider {
	return func(url string) Provider { a := NewAnthropic(url, "k", 100); a.NoVision = noVision; return a }
}

func asJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestOpenAISendsUserImagesAsParts(t *testing.T) {
	body := capture(t, openAI(false), []Message{{Role: RoleUser, Content: "what is this?", Images: []Image{png, jpeg}}})
	msgs := body["messages"].([]any)
	want := `{"content":[{"text":"what is this?","type":"text"},` +
		`{"image_url":{"url":"data:image/png;base64,` + b64(png) + `"},"type":"image_url"},` +
		`{"image_url":{"url":"data:image/jpeg;base64,` + b64(jpeg) + `"},"type":"image_url"}],"role":"user"}`
	if got := asJSON(msgs[0]); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

// Chat Completions tool messages hold text only, so a tool's pictures arrive in
// a user message after the whole run of tool messages, which must stay together.
func TestOpenAIPutsToolImagesAfterTheToolMessages(t *testing.T) {
	body := capture(t, openAI(false), []Message{
		{Role: RoleUser, Content: "look"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "shot", Arguments: "{}"}, {ID: "b", Name: "other", Arguments: "{}"}}},
		{Role: RoleTool, ToolCallID: "a", Content: "took a picture", Images: []Image{png}},
		{Role: RoleTool, ToolCallID: "b", Content: "plain"},
		{Role: RoleAssistant, Content: "I see"},
	})
	msgs := body["messages"].([]any)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "user,assistant,tool,tool,user,assistant" {
		t.Fatalf("the picture message must come after both tool results: %v", roles)
	}
	if first := msgs[2].(map[string]any); first["content"] != "took a picture" || first["tool_call_id"] != "a" {
		t.Fatalf("a tool message stays text: %v", first)
	}
	pic := msgs[4].(map[string]any)["content"].([]any)
	if len(pic) != 2 || pic[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("%v", pic)
	}
}

func TestOpenAINoVisionLeavesImagesOut(t *testing.T) {
	body := capture(t, openAI(true), []Message{
		{Role: RoleUser, Content: "what is this?", Images: []Image{png}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "shot", Arguments: "{}"}}},
		{Role: RoleTool, ToolCallID: "a", Content: "took a picture", Images: []Image{png}},
	})
	raw := asJSON(body)
	if strings.Contains(raw, "image_url") || strings.Contains(raw, b64(png)) {
		t.Fatalf("a picture was sent to a model that cannot see: %s", raw)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 3 || !strings.Contains(msgs[0].(map[string]any)["content"].(string), NoImageNote) || !strings.Contains(msgs[2].(map[string]any)["content"].(string), NoImageNote) {
		t.Fatalf("the model is told something was there: %v", msgs)
	}
}

func TestOpenAIWithoutImagesIsUnchanged(t *testing.T) {
	body := capture(t, openAI(false), []Message{{Role: RoleUser, Content: "hi"}, {Role: RoleAssistant, Content: "hello"}})
	if got := asJSON(body["messages"]); got != `[{"content":"hi","role":"user"},{"content":"hello","role":"assistant"}]` {
		t.Fatal(got)
	}
}

func TestAnthropicSendsImages(t *testing.T) {
	body := capture(t, anthropic(false), []Message{
		{Role: RoleUser, Content: "look", Images: []Image{png}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Name: "shot", Arguments: "{}"}}},
		{Role: RoleTool, ToolCallID: "t1", Content: "took a picture", Images: []Image{jpeg}},
	})
	msgs := body["messages"].([]any)
	user := msgs[0].(map[string]any)["content"].([]any)
	wantUser := `[{"text":"look","type":"text"},{"source":{"data":"` + b64(png) + `","media_type":"image/png","type":"base64"},"type":"image"}]`
	if got := asJSON(user); got != wantUser {
		t.Fatalf("\n got %s\nwant %s", got, wantUser)
	}
	// A tool result holds the picture itself, beside its text.
	result := msgs[2].(map[string]any)["content"].([]any)[0]
	wantResult := `{"content":[{"text":"took a picture","type":"text"},{"source":{"data":"` + b64(jpeg) + `","media_type":"image/jpeg","type":"base64"},"type":"image"}],"tool_use_id":"t1","type":"tool_result"}`
	if got := asJSON(result); got != wantResult {
		t.Fatalf("\n got %s\nwant %s", got, wantResult)
	}
}

func TestAnthropicNoVisionLeavesImagesOut(t *testing.T) {
	body := capture(t, anthropic(true), []Message{
		{Role: RoleUser, Content: "look", Images: []Image{png}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Name: "shot", Arguments: "{}"}}},
		{Role: RoleTool, ToolCallID: "t1", Content: "took a picture", Images: []Image{jpeg}},
	})
	raw := asJSON(body)
	if strings.Contains(raw, `"image"`) || strings.Contains(raw, b64(png)) || strings.Contains(raw, b64(jpeg)) {
		t.Fatalf("%s", raw)
	}
	if strings.Count(raw, NoImageNote) != 2 {
		t.Fatalf("each picture is replaced by the note: %s", raw)
	}
}

func TestSpecCarriesNoVision(t *testing.T) {
	for _, typ := range []string{TypeOpenAI, TypeAnthropic} {
		p, err := New(Spec{Type: typ, NoVision: true})
		if err != nil {
			t.Fatal(err)
		}
		switch p := p.(type) {
		case *OpenAI:
			if !p.NoVision {
				t.Error("openai")
			}
		case *Anthropic:
			if !p.NoVision {
				t.Error("anthropic")
			}
		}
	}
}
