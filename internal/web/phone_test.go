package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	phonelink "talaria/internal/phone"
)

func TestThePhoneLinkNeedsTheAppsOwnOrigin(t *testing.T) {
	r := newRig(t)
	r.srv.Phone = phonelink.NewHub()
	r.login()
	origin := "http://" + strings.TrimPrefix(r.ts.URL, "http://")
	for _, c := range []struct {
		name, origin string
		cookie       bool
		want         int
	}{
		{"no session", origin, false, 401},
		{"another site", "https://evil.example", true, 403},
		{"no origin", "", true, 403},
		{"the app", origin, true, 101},
	} {
		if _, code := dialWS(t, r, "/api/phone/link", c.origin, c.cookie); code != c.want {
			t.Errorf("%s: %d, want %d", c.name, code, c.want)
		}
	}
}

func TestThePhoneAnswersWhatTheAgentAsks(t *testing.T) {
	r := newRig(t)
	hub := phonelink.NewHub()
	r.srv.Phone = hub
	r.login()
	origin := "http://" + strings.TrimPrefix(r.ts.URL, "http://")
	ws, code := dialWS(t, r, "/api/phone/link", origin, true)
	if code != 101 {
		t.Fatalf("handshake %d", code)
	}
	ws.text(`{"t":"hello","name":"Pixel","caps":["status"]}`)

	var st struct {
		Connected bool
		Name      string
		Caps      []string
	}
	deadline := time.Now().Add(2 * time.Second)
	for !st.Connected || len(st.Caps) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the phone never showed as linked: %+v", st)
		}
		_, body := r.do("GET", "/api/phone", nil)
		json.Unmarshal(body, &st)
		time.Sleep(10 * time.Millisecond)
	}
	if st.Name != "Pixel" || st.Caps[0] != "status" {
		t.Errorf("status: %+v", st)
	}

	type out struct {
		data json.RawMessage
		err  error
	}
	res := make(chan out, 1)
	go func() {
		d, err := hub.Call(t.Context(), phonelink.OpStatus, nil)
		res <- out{d, err}
	}()
	op, p, err := ws.next(t)
	if err != nil || op != 1 || !strings.Contains(string(p), `"op":"status"`) {
		t.Fatalf("the phone was not asked: %d %s %v", op, p, err)
	}
	var req struct{ ID uint64 }
	json.Unmarshal(p, &req)
	ws.text(`{"t":"res","id":` + itoa(int64(req.ID)) + `,"ok":true,"data":{"battery":64}}`)
	got := <-res
	if got.err != nil || !strings.Contains(string(got.data), `"battery":64`) {
		t.Errorf("the answer did not come back: %s %v", got.data, got.err)
	}
}

func TestWithNoPhoneHubTheStatusIsEmptyAndTheLinkIsShut(t *testing.T) {
	r := newRig(t)
	r.login()
	_, body := r.do("GET", "/api/phone", nil)
	if !strings.Contains(string(body), `"connected":false`) {
		t.Errorf("status: %s", body)
	}
	origin := "http://" + strings.TrimPrefix(r.ts.URL, "http://")
	if _, code := dialWS(t, r, "/api/phone/link", origin, true); code != 404 {
		t.Errorf("link with no hub: %d", code)
	}
}
