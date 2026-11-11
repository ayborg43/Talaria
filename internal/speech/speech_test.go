package speech

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func wavOf(rate, channels, bits int, seconds float64) []byte {
	b := Silence(seconds)
	binary.LittleEndian.PutUint16(b[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(b[24:28], uint32(rate))
	binary.LittleEndian.PutUint16(b[34:36], uint16(bits))
	return b
}

func TestOnlyAMono16BitWAVOfASensibleLengthIsAccepted(t *testing.T) {
	good := Silence(1.5)
	if info, err := Check(good, 120); err != nil || info.Rate != 16000 || info.Seconds < 1.49 || info.Seconds > 1.51 {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := Check(wavOf(24000, 1, 16, 1), 120); err != nil {
		t.Fatalf("24 kHz is allowed: %v", err)
	}
	for name, data := range map[string][]byte{
		"empty":     nil,
		"text":      []byte("hello this is not audio at all, just a long enough line of text to pass the header size"),
		"stereo":    wavOf(16000, 2, 16, 1),
		"8-bit":     wavOf(16000, 1, 8, 1),
		"44.1 kHz":  wavOf(44100, 1, 16, 1),
		"a webm":    append([]byte{0x1a, 0x45, 0xdf, 0xa3}, make([]byte, 100)...),
		"truncated": good[:30],
	} {
		if _, err := Check(data, 120); !errors.Is(err, ErrNotWAV) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Check(Silence(0.1), 120); !errors.Is(err, ErrTooShort) {
		t.Errorf("a tap: %v", err)
	}
	if _, err := Check(Silence(31), 30); !errors.Is(err, ErrTooLong) {
		t.Errorf("a long one: %v", err)
	}
	// extra chunks before the audio (a LIST chunk) are skipped, as real files have them
	extra := append([]byte{}, good[:36]...)
	extra = append(extra, []byte("LIST")...)
	extra = binary.LittleEndian.AppendUint32(extra, 4)
	extra = append(extra, []byte("abcd")...)
	extra = append(extra, good[36:]...)
	if _, err := Check(extra, 120); err != nil {
		t.Errorf("a WAV with a LIST chunk: %v", err)
	}
}

func parts(t *testing.T, r *http.Request) (map[string]string, map[string][]byte) {
	t.Helper()
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mt, "multipart/") {
		t.Fatalf("not multipart: %q", r.Header.Get("Content-Type"))
	}
	fields, files := map[string]string{}, map[string][]byte{}
	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return fields, files
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(p)
		if p.FileName() != "" {
			files[p.FormName()] = data
		} else {
			fields[p.FormName()] = string(data)
		}
		if p.FormName() == "request" {
			fields["request.type"] = p.Header.Get("Content-Type")
		}
	}
}

func TestAnOpenAIStyleServiceGetsAFileTheModelAndTheHints(t *testing.T) {
	var gotAuth, gotPath string
	var fields map[string]string
	var files map[string][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		fields, files = parts(t, r)
		json.NewEncoder(w).Encode(map[string]string{"text": "  hello there  "})
	}))
	defer srv.Close()
	svc := &Service{Provider: OpenAI, BaseURL: srv.URL + "/v1/", Model: "whisper-1", Key: "sk-secret", Language: "en", Vocabulary: "Talaria, Lagos"}
	wav := Silence(1)
	got, err := svc.Transcribe(context.Background(), wav)
	if err != nil || got != "hello there" {
		t.Fatalf("%q %v", got, err)
	}
	if gotPath != "/v1/audio/transcriptions" || gotAuth != "Bearer sk-secret" {
		t.Fatalf("%s %s", gotPath, gotAuth)
	}
	if fields["model"] != "whisper-1" || fields["language"] != "en" || fields["prompt"] != "Talaria, Lagos" || fields["response_format"] != "json" || len(files["file"]) != len(wav) {
		t.Fatalf("%v (file %d bytes)", fields, len(files["file"]))
	}
}

func TestMuseGetsItsRequestPartAndTheWAV(t *testing.T) {
	var gotAuth, gotPath string
	var fields map[string]string
	var files map[string][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		fields, files = parts(t, r)
		json.NewEncoder(w).Encode(map[string]any{"sessionId": "s", "transcript": "How is the weather?", "audioDurationMs": 1000, "turns": []any{}})
	}))
	defer srv.Close()
	svc := &Service{Provider: Muse, BaseURL: srv.URL + "/v1", Model: "muse-voice-transcribe-1.0", Key: "k", Language: "English", Vocabulary: "Talaria, , Lagos"}
	got, err := svc.Transcribe(context.Background(), Silence(1))
	if err != nil || got != "How is the weather?" {
		t.Fatalf("%q %v", got, err)
	}
	if gotPath != "/v1/asr/transcribe" || gotAuth != "Bearer k" || fields["request.type"] != "application/json" || len(files["audio"]) == 0 {
		t.Fatalf("%s %s %v", gotPath, gotAuth, fields)
	}
	var req struct {
		Model         string   `json:"model"`
		AudioEncoding string   `json:"audioEncoding"`
		Mode          string   `json:"mode"`
		LanguageBias  []string `json:"languageBias"`
		Keywords      []string `json:"keywords"`
	}
	if err := json.Unmarshal([]byte(fields["request"]), &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "muse-voice-transcribe-1.0" || req.AudioEncoding != "WAV" || req.Mode != "PUSH_TO_TALK" || len(req.LanguageBias) != 1 || len(req.Keywords) != 2 {
		t.Fatalf("%+v", req)
	}
}

func TestProviderFailuresAreReportedSafelyAndNeverEchoTheKey(t *testing.T) {
	for status, want := range map[int]error{401: ErrRefused, 403: ErrRefused, 429: ErrBusy, 413: ErrTooLong} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope sk-secret", status) }))
		_, err := (&Service{Provider: OpenAI, BaseURL: srv.URL, Model: "m", Key: "sk-secret"}).Transcribe(context.Background(), Silence(1))
		srv.Close()
		if !errors.Is(err, want) {
			t.Errorf("%d: %v", status, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "The audio is not a supported WAV file"}})
	}))
	defer srv.Close()
	_, err := (&Service{Provider: Muse, BaseURL: srv.URL, Model: "m"}).Transcribe(context.Background(), Silence(1))
	if err == nil || !strings.Contains(err.Error(), "not a supported WAV file") {
		t.Fatalf("a 400 should say why: %v", err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := down.URL
	down.Close()
	_, err = (&Service{Provider: OpenAI, BaseURL: addr, Model: "m", Key: "sk-secret"}).Transcribe(context.Background(), Silence(1))
	if !errors.Is(err, ErrDown) || strings.Contains(err.Error(), addr) || strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("a service that is down must not leak its address or key: %v", err)
	}
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>") }))
	defer junk.Close()
	if _, err := (&Service{Provider: OpenAI, BaseURL: junk.URL, Model: "m"}).Transcribe(context.Background(), Silence(1)); err == nil {
		t.Fatal("an answer that is not JSON was accepted")
	}
}
