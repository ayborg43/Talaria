package calendar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"talaria/internal/tool"
)

// fakeDAV is a one-event CalDAV collection in memory.
type fakeDAV struct {
	mu   sync.Mutex
	ics  map[string][]byte
	etag map[string]string
	puts []string
}

func newFakeDAV(t *testing.T) (*fakeDAV, *httptest.Server) {
	t.Helper()
	f := &fakeDAV{ics: map[string][]byte{}, etag: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		uid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/cal/"), ".ics")
		user, pass, _ := r.BasicAuth()
		if user != "ay" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodPut:
			if im := r.Header.Get("If-Match"); im != "" && im != f.etag[uid] {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			if inm := r.Header.Get("If-None-Match"); inm == "*" && f.ics[uid] != nil {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			body, _ := io.ReadAll(r.Body)
			f.ics[uid] = body
			f.etag[uid] = `"v` + strings.Repeat("x", len(f.puts)+1) + `"`
			f.puts = append(f.puts, string(body))
			w.Header().Set("ETag", f.etag[uid])
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			if f.ics[uid] == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("ETag", f.etag[uid])
			w.Write(f.ics[uid])
		case http.MethodDelete:
			if f.ics[uid] == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(f.ics, uid)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func TestCalDAVWriterCreatesUpdatesAndDeletesWithETags(t *testing.T) {
	f, srv := newFakeDAV(t)
	s, _ := writeService(t)
	s.DAV = map[string]*DAVClient{"work": {Collection: srv.URL + "/cal/", Username: "ay", Password: "secret", Client: srv.Client()}}
	r := tool.NewRegistry()
	RegisterWriteTools(r, s, nil)

	out := runTool(t, r, "calendar_add", `{"calendar":"work","title":"Review","start":"2026-10-23 15:00","end":"2026-10-23 16:00"}`)
	if !strings.Contains(out, "Review") || !strings.Contains(out, "uid ") {
		t.Fatalf("dav add:\n%s", out)
	}
	if len(f.ics) != 1 {
		t.Fatalf("nothing was PUT: %v", f.puts)
	}
	var uid string
	for k := range f.ics {
		uid = k
	}
	body := string(f.ics[uid])
	for _, want := range []string{"BEGIN:VEVENT", "SUMMARY:Review", "DTSTART:20261023T140000Z"} {
		if !strings.Contains(body, want) {
			t.Fatalf("ics missing %q:\n%s", want, body)
		}
	}

	runTool(t, r, "calendar_update", `{"calendar":"work","uid":"`+uid+`","title":"Review (moved)","start":"2026-10-23 17:00"}`)
	if got := string(f.ics[uid]); !strings.Contains(got, "SUMMARY:Review (moved)") || !strings.Contains(got, "DTSTART:20261023T160000Z") {
		t.Fatalf("update did not round-trip:\n%s", got)
	}

	// A stale write is refused by the fake with 412 and surfaces as an error.
	stale := &DAVClient{Collection: srv.URL + "/cal/", Username: "ay", Password: "secret", Client: srv.Client()}
	if _, err := stale.Put(context.Background(), uid, []byte("x"), `"nope"`); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale etag accepted: %v", err)
	}

	runTool(t, r, "calendar_delete", `{"calendar":"work","uid":"`+uid+`"}`)
	if len(f.ics) != 0 {
		t.Fatalf("not deleted: %v", f.ics)
	}
	// A read-only calendar refuses writes by name.
	tl, _ := r.Get("calendar_add")
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"calendar":"nope","title":"x","start":"2026-10-23 15:00"}`)); err == nil {
		t.Fatal("a write to an unknown calendar was accepted")
	}
}
