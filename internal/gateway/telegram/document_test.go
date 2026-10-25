package telegram

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendDocumentPostsTheFile(t *testing.T) {
	var gotName, gotChat, gotCT string
	var gotBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendDocument") {
			t.Errorf("wrong method: %s", r.URL.Path)
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
			case "chat_id":
				gotChat = string(raw)
			case "document":
				gotName = part.FileName()
				gotBytes = raw
			}
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	a := NewAPI("TESTTOKEN")
	a.Base = srv.URL
	if err := a.SendDocument(context.Background(), 7, "report.pdf", []byte("%PDF-fake")); err != nil {
		t.Fatal(err)
	}
	if gotChat != "7" || gotName != "report.pdf" || string(gotBytes) != "%PDF-fake" {
		t.Fatalf("chat=%q name=%q bytes=%q", gotChat, gotName, gotBytes)
	}
	if mt, _, err := mime.ParseMediaType(gotCT); err != nil || !strings.HasPrefix(mt, "multipart/form-data") {
		t.Fatalf("content type: %q %v", gotCT, err)
	}
}

func TestSendDocumentRefusesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"description":"bad request","error_code":400}`))
	}))
	defer srv.Close()
	a := NewAPI("TESTTOKEN")
	a.Base = srv.URL
	if err := a.SendDocument(context.Background(), 7, "x.pdf", []byte("x")); err == nil {
		t.Fatal("an API error was swallowed")
	}
}
