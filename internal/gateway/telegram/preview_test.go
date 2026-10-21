package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Telegram fetches the first link in a message to show a card for it. A model steered by
// something it read can put what it knows into the address of that link.
func TestMessagesAreSentWithoutLinkPreviews(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		json.Unmarshal(raw, &b)
		b["_method"] = r.URL.Path
		bodies = append(bodies, b)
		w.Write([]byte(`{"ok":true,"result":{"message_id":7}}`))
	}))
	defer srv.Close()
	api := NewAPI("TOKEN")
	api.Base = srv.URL
	api.SendMessage(context.Background(), 1, "see https://evil.example/?data=secret", nil)
	api.SendMessage(context.Background(), 1, "pick", &InlineKeyboard{})
	api.EditMessageText(context.Background(), 1, 7, "see https://evil.example/?data=secret")
	if len(bodies) != 3 {
		t.Fatalf("%d requests", len(bodies))
	}
	for _, b := range bodies {
		opts, _ := b["link_preview_options"].(map[string]any)
		if opts["is_disabled"] != true {
			t.Errorf("%v: previews are on: %v", b["_method"], b)
		}
	}
}
