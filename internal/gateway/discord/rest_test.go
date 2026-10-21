package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/gateway/chat"
)

type recorded struct {
	Method, Path string
	Header       http.Header
	Body         map[string]any
}

// restServer answers each request from handler and records them.
func restServer(t *testing.T, handler func(n int, w http.ResponseWriter, r *http.Request)) (*API, func() []recorded) {
	t.Helper()
	var mu sync.Mutex
	var log []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		mu.Lock()
		log = append(log, recorded{r.Method, r.URL.Path, r.Header.Clone(), body})
		n := len(log)
		mu.Unlock()
		handler(n, w, r)
	}))
	t.Cleanup(srv.Close)
	api := NewAPI("SECRET-TOKEN")
	api.Base = srv.URL
	return api, func() []recorded {
		mu.Lock()
		defer mu.Unlock()
		return append([]recorded(nil), log...)
	}
}

func TestRequestsCarryAuthAndUserAgent(t *testing.T) {
	api, log := restServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"900","username":"bot","bot":true}`))
	})
	me, err := api.GetMe(context.Background())
	if err != nil || me.ID != "900" || !me.Bot {
		t.Fatalf("%+v %v", me, err)
	}
	req := log()[0]
	if req.Method != "GET" || req.Path != "/users/@me" || req.Header.Get("Authorization") != "Bot SECRET-TOKEN" {
		t.Fatalf("%+v", req)
	}
	if ua := req.Header.Get("User-Agent"); !strings.HasPrefix(ua, "DiscordBot (") {
		t.Fatalf("Discord requires a User-Agent of the form DiscordBot (url, version); got %q", ua)
	}
}

// What the model writes must never ping anyone, and sending buttons must say so.
func TestCreateMessageSuppressesMentions(t *testing.T) {
	api, log := restServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"77","channel_id":"5"}`))
	})
	m, err := api.CreateMessage(context.Background(), "5", "hi @everyone <@123>", nil)
	if err != nil || m.ID != "77" {
		t.Fatalf("%+v %v", m, err)
	}
	req := log()[0]
	if req.Method != "POST" || req.Path != "/channels/5/messages" || req.Body["content"] != "hi @everyone <@123>" {
		t.Fatalf("%+v", req)
	}
	am, _ := req.Body["allowed_mentions"].(map[string]any)
	if parse, ok := am["parse"].([]any); !ok || len(parse) != 0 {
		t.Fatalf("allowed_mentions.parse must be an empty list, got %v", req.Body["allowed_mentions"])
	}
	if _, has := req.Body["components"]; has {
		t.Fatal("a plain message carries no components")
	}

	api.CreateMessage(context.Background(), "5", "pick", []Component{{Type: componentRow, Components: []Component{{Type: componentButton, Style: styleSuccess, Label: "Allow", CustomID: "ap:1:y"}}}})
	row := log()[1].Body["components"].([]any)[0].(map[string]any)
	btn := row["components"].([]any)[0].(map[string]any)
	if row["type"] != float64(1) || btn["type"] != float64(2) || btn["style"] != float64(3) || btn["custom_id"] != "ap:1:y" || btn["label"] != "Allow" {
		t.Fatalf("components %v", row)
	}
}

// An edit removes the buttons: a message that was an approval prompt must not
// keep offering the choice after it has been answered or has timed out.
func TestEditMessageClearsButtonsAndSuppressesMentions(t *testing.T) {
	api, log := restServer(t, func(_ int, w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	if err := api.EditMessage(context.Background(), "5", "77", "new text"); err != nil {
		t.Fatal(err)
	}
	req := log()[0]
	comps, ok := req.Body["components"].([]any)
	if req.Method != "PATCH" || req.Path != "/channels/5/messages/77" || !ok || len(comps) != 0 || req.Body["content"] != "new text" {
		t.Fatalf("%+v", req)
	}
	if _, ok := req.Body["allowed_mentions"]; !ok {
		t.Fatal("edits must suppress mentions too")
	}
}

func TestOpenDMAndTypingAndGatewayURL(t *testing.T) {
	api, log := restServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/@me/channels":
			w.Write([]byte(`{"id":"chan9"}`))
		case "/gateway/bot":
			w.Write([]byte(`{"url":"wss://gw.example","shards":1}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	ctx := context.Background()
	if c, err := api.OpenDM(ctx, "42"); err != nil || c.ID != "chan9" {
		t.Fatalf("%+v %v", c, err)
	}
	if err := api.Typing(ctx, "chan9"); err != nil {
		t.Fatal(err)
	}
	if u, err := api.GatewayURL(ctx); err != nil || u != "wss://gw.example" {
		t.Fatalf("%q %v", u, err)
	}
	l := log()
	if l[0].Body["recipient_id"] != "42" || l[1].Method != "POST" || l[1].Path != "/channels/chan9/typing" {
		t.Fatalf("%+v", l)
	}
}

func TestRespondToAnInteraction(t *testing.T) {
	api, log := restServer(t, func(_ int, w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	in := Interaction{ID: "11", Token: "tok-xyz"}
	ctx := context.Background()
	if err := api.Respond(ctx, in, respUpdateMessage, "edited", false); err != nil {
		t.Fatal(err)
	}
	if err := api.Respond(ctx, in, respMessage, "private note", true); err != nil {
		t.Fatal(err)
	}
	l := log()
	if l[0].Path != "/interactions/11/tok-xyz/callback" || l[0].Body["type"] != float64(7) {
		t.Fatalf("%+v", l[0])
	}
	d := l[0].Body["data"].(map[string]any)
	if comps, ok := d["components"].([]any); !ok || len(comps) != 0 || d["content"] != "edited" {
		t.Fatalf("an update must clear the buttons: %v", d)
	}
	d2 := l[1].Body["data"].(map[string]any)
	if l[1].Body["type"] != float64(4) || d2["flags"] != float64(64) {
		t.Fatalf("a refusal is shown only to the person who pressed: %v", l[1].Body)
	}
}

func TestRateLimitIsWaitedOutAndRetried(t *testing.T) {
	api, log := restServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		if n <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message":"You are being rate limited.","retry_after":1.5,"global":false}`))
			return
		}
		w.Write([]byte(`{"id":"1"}`))
	})
	var waits []time.Duration
	api.RetrySleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	if _, err := api.CreateMessage(context.Background(), "5", "x", nil); err != nil {
		t.Fatal(err)
	}
	if len(log()) != 3 || len(waits) != 2 || waits[0] != 1500*time.Millisecond {
		t.Fatalf("requests=%d waits=%v", len(log()), waits)
	}
}

func TestRateLimitGivesUp(t *testing.T) {
	for name, retry := range map[string]string{"too long a wait": "120", "never clears": "0.5"} {
		api, log := restServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message":"slow down","retry_after":` + retry + `}`))
		})
		api.RetrySleep = func(context.Context, time.Duration) error { return nil }
		_, err := api.CreateMessage(context.Background(), "5", "x", nil)
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != 429 {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "too long a wait" && len(log()) != 1 {
			t.Fatalf("%s: a wait over the limit must not be attempted: %d requests", name, len(log()))
		}
		if name == "never clears" && len(log()) != maxRateLimitTries {
			t.Fatalf("%s: %d requests, want %d", name, len(log()), maxRateLimitTries)
		}
	}
}

func TestAPIErrorCarriesDiscordsCode(t *testing.T) {
	api, _ := restServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"code":10008,"message":"Unknown Message"}`))
	})
	err := api.EditMessage(context.Background(), "5", "77", "x")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != 10008 || ae.Status != 404 || !strings.Contains(err.Error(), "Unknown Message") {
		t.Fatalf("%v", err)
	}
	if got := (&transport{}).Classify(err); got != chat.EditGone {
		t.Fatalf("a deleted message should be classified as gone, got %d", got)
	}

	// A proxy's HTML error page still gives a useful error.
	api2, _ := restServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`<html>bad gateway</html>`))
	})
	if _, err := api2.GetMe(context.Background()); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("%v", err)
	}
}

func TestTokenNeverAppearsInErrors(t *testing.T) {
	api := NewAPI("SECRET-TOKEN")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	api.Base = srv.URL + "/SECRET-TOKEN" // a URL that carries it, as a misconfigured proxy might
	srv.Close()
	_, err := api.GetMe(context.Background())
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("the token must be redacted: %v", err)
	}

	in := Interaction{ID: "1", Token: "interaction-secret"}
	api.Base = srv.URL + "/interaction-secret"
	err = api.Respond(context.Background(), in, respMessage, "x", true)
	if err == nil || strings.Contains(err.Error(), "interaction-secret") {
		t.Fatalf("the interaction token must be redacted: %v", err)
	}
}
