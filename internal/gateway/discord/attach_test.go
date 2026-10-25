package discord

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateAttachmentPostsTheFile(t *testing.T) {
	var gotName, gotCT string
	var gotBytes []byte
	var gotPayload string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		gotCT = r.Header.Get("Content-Type")
		mr, err := r.MultipartReader()
		if err != nil {
			t.Fatal(err)
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(part)
			switch part.FormName() {
			case "payload_json":
				gotPayload = string(raw)
			case "files[0]":
				gotName = part.FileName()
				gotBytes = raw
			}
		}
		w.Write([]byte(`{"id":"1","channel_id":"c"}`))
	}))
	defer srv.Close()
	a := NewAPI("TESTTOKEN")
	a.Base = srv.URL
	if err := a.CreateAttachment(context.Background(), "chan1", "report.pdf", []byte("%PDF-fake")); err != nil {
		t.Fatal(err)
	}
	if gotName != "report.pdf" || string(gotBytes) != "%PDF-fake" {
		t.Fatalf("name=%q bytes=%q", gotName, gotBytes)
	}
	if !strings.Contains(gotPayload, "report.pdf") {
		t.Fatalf("payload should name the file: %q", gotPayload)
	}
	if mt, _, err := mime.ParseMediaType(gotCT); err != nil || !strings.HasPrefix(mt, "multipart/form-data") {
		t.Fatalf("content type: %q %v", gotCT, err)
	}
}

func TestCreateAttachmentRefusesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"403: Forbidden","code":50013}`))
	}))
	defer srv.Close()
	a := NewAPI("TESTTOKEN")
	a.Base = srv.URL
	if err := a.CreateAttachment(context.Background(), "c", "x.pdf", []byte("x")); err == nil {
		t.Fatal("an API error was swallowed")
	}
}
