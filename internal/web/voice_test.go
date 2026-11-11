package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"talaria/internal/config"
	"talaria/internal/speech"
)

// A fake OpenAI-style speech service that counts calls and can be told to stall.
type fakeSTT struct {
	srv   *httptest.Server
	calls atomic.Int32
	text  string
	stall chan struct{}
	auth  atomic.Value
}

func newFakeSTT(t *testing.T) *fakeSTT {
	f := &fakeSTT{text: "buy oat milk on the way home"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		f.auth.Store(r.Header.Get("Authorization"))
		io.Copy(io.Discard, r.Body)
		if f.stall != nil {
			<-f.stall
		}
		json.NewEncoder(w).Encode(map[string]string{"text": f.text})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func voiceRig(t *testing.T, f *fakeSTT) *rig {
	t.Helper()
	r := newRig(t)
	r.srv.Cfg = &config.Config{}
	if f != nil {
		r.srv.Cfg.Transcription = config.Transcription{Provider: "openai", BaseURL: f.srv.URL + "/v1", Model: "whisper-1", APIKey: "sk-secret-key"}
	}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.srv.Getenv = func(string) string { return "" }
	r.login()
	return r
}

func (r *rig) postAudio(path string, ct string, body []byte) (*http.Response, []byte) {
	r.t.Helper()
	req, _ := http.NewRequest("POST", r.ts.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Talaria", "1")
	resp, err := r.c.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestAVoiceNoteBecomesTextAndNothingIsKept(t *testing.T) {
	f := newFakeSTT(t)
	r := voiceRig(t, f)
	resp, body := r.postAudio("/api/transcribe", "audio/wav", speech.Silence(2))
	var got struct {
		Text    string  `json:"text"`
		Seconds float64 `json:"seconds"`
	}
	if err := json.Unmarshal(body, &got); err != nil || resp.StatusCode != 200 || got.Text != "buy oat milk on the way home" || got.Seconds < 1.9 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if f.calls.Load() != 1 || f.auth.Load() != "Bearer sk-secret-key" {
		t.Fatalf("calls %d auth %v", f.calls.Load(), f.auth.Load())
	}
	// no audio and no words are left behind: the data directory holds only the config
	entries, _ := os.ReadDir(filepath.Dir(r.srv.ConfigPath))
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Errorf("something was written: %s", e.Name())
		}
	}
}

func TestTheTranscribeEndpointRefusesWhatIsNotAShortWAVAndWhatIsNotSignedIn(t *testing.T) {
	f := newFakeSTT(t)
	r := voiceRig(t, f)
	r.srv.Cfg.Transcription.MaxSeconds = 10
	for name, c := range map[string]struct {
		ct   string
		body []byte
		code int
	}{
		"not audio":  {"application/json", speech.Silence(2), 415},
		"not a WAV":  {"audio/webm", []byte(strings.Repeat("x", 500)), 400},
		"a bit long": {"audio/wav", speech.Silence(10.05), 400}, // under the size cap, over the time
		"too long":   {"audio/wav", speech.Silence(11), 413},    // over the size cap
		"enormous":   {"audio/wav", make([]byte, speech.MaxBytes(10)+100), 413},
		"a tap":      {"audio/wav", speech.Silence(0.1), 400},
		"empty":      {"audio/wav", nil, 400},
	} {
		if resp, body := r.postAudio("/api/transcribe", c.ct, c.body); resp.StatusCode != c.code {
			t.Errorf("%s answered %d (%s), want %d", name, resp.StatusCode, body, c.code)
		}
	}
	if f.calls.Load() != 0 {
		t.Fatalf("a refused recording reached the speech service %d times", f.calls.Load())
	}
	anon, _ := http.NewRequest("POST", r.ts.URL+"/api/transcribe", bytes.NewReader(speech.Silence(2)))
	anon.Header.Set("Content-Type", "audio/wav")
	anon.Header.Set("X-Talaria", "1")
	if resp, _ := http.DefaultClient.Do(anon); resp.StatusCode != 401 {
		t.Fatalf("signed out answered %d", resp.StatusCode)
	}
	none := voiceRig(t, nil)
	if resp, _ := none.postAudio("/api/transcribe", "audio/wav", speech.Silence(2)); resp.StatusCode != 501 {
		t.Fatalf("with no service: %d", resp.StatusCode)
	}
}

func TestOnlyOneVoiceNoteIsTranscribedAtATime(t *testing.T) {
	f := newFakeSTT(t)
	f.stall = make(chan struct{})
	r := voiceRig(t, f)
	first := make(chan int, 1)
	go func() {
		resp, _ := r.postAudio("/api/transcribe", "audio/wav", speech.Silence(2))
		first <- resp.StatusCode
	}()
	deadline := time.Now().Add(2 * time.Second)
	for f.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if resp, body := r.postAudio("/api/transcribe", "audio/wav", speech.Silence(2)); resp.StatusCode != 429 || !strings.Contains(string(body), "one voice note at a time") {
		t.Fatalf("a second one while the first waits: %d %s", resp.StatusCode, body)
	}
	close(f.stall)
	if code := <-first; code != 200 {
		t.Fatalf("the first: %d", code)
	}
	if resp, _ := r.postAudio("/api/transcribe", "audio/wav", speech.Silence(2)); resp.StatusCode != 200 {
		t.Fatalf("after it finished: %d", resp.StatusCode)
	}
}

func TestTheVoiceSettingsNeverShowTheKeyAndRefuseAnAddressThatLeaksIt(t *testing.T) {
	f := newFakeSTT(t)
	r := voiceRig(t, f)
	_, body := r.do("GET", "/api/voice", nil)
	if strings.Contains(string(body), "sk-secret-key") {
		t.Fatalf("the key was sent back: %s", body)
	}
	var v voiceView
	json.Unmarshal(body, &v)
	if !v.Available || !v.HasKey || v.KeySource != "settings" || len(v.Presets) != 4 || v.MaxSeconds != 120 {
		t.Fatalf("%+v", v)
	}
	for name, in := range map[string]map[string]any{
		"clear http to another host": {"provider": "openai", "base_url": "http://speech.example.com/v1"},
		"unknown provider":           {"provider": "whisperx"},
		"bad key variable":           {"provider": "openai", "api_key_env": "X; rm -rf /"},
		"too long a note":            {"provider": "openai", "max_seconds": 3000},
		"not an address":             {"provider": "openai", "base_url": "ftp://x"},
	} {
		if resp, _ := r.do("POST", "/api/voice", in); resp.StatusCode != 400 {
			t.Errorf("%s answered %d", name, resp.StatusCode)
		}
	}
	// a blank key keeps the saved one; a local http server is allowed; clearing and disconnecting work
	resp, body := r.do("POST", "/api/voice", map[string]any{"provider": "openai", "base_url": "http://localhost:8080/v1", "model": "whisper-1", "language": "en", "auto_send": true})
	if resp.StatusCode != 200 || strings.Contains(string(body), "sk-secret-key") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if r.srv.Cfg.Transcription.APIKey != "sk-secret-key" || !r.srv.Cfg.Transcription.AutoSend {
		t.Fatalf("a blank key should keep the saved one: %+v", r.srv.Cfg.Transcription)
	}
	r.do("POST", "/api/voice", map[string]any{"provider": "openai", "base_url": "http://localhost:8080/v1", "clear_key": true})
	if r.srv.Cfg.Transcription.APIKey != "" {
		t.Fatal("the key was not cleared")
	}
	r.do("POST", "/api/voice", map[string]any{"disconnect": true})
	if r.srv.Cfg.Transcription.Configured() {
		t.Fatalf("still configured: %+v", r.srv.Cfg.Transcription)
	}
	if raw, _ := os.ReadFile(r.srv.ConfigPath); strings.Contains(string(raw), "sk-secret-key") {
		t.Fatalf("a cleared key is still in the config: %s", raw)
	}
}

func TestTheVoiceTestUsesSilenceAndSaysWhatWentWrong(t *testing.T) {
	f := newFakeSTT(t)
	r := voiceRig(t, f)
	_, body := r.do("POST", "/api/voice/test", map[string]any{})
	if !strings.Contains(string(body), `"ok":true`) || f.calls.Load() != 1 {
		t.Fatalf("%s (calls %d)", body, f.calls.Load())
	}
	r.srv.Cfg.Transcription.BaseURL = "http://127.0.0.1:1/v1"
	_, body = r.do("POST", "/api/voice/test", map[string]any{})
	if !strings.Contains(string(body), `"ok":false`) || strings.Contains(string(body), "sk-secret-key") || strings.Contains(string(body), "127.0.0.1:1") {
		t.Fatalf("a failure should say so without the key or the address: %s", body)
	}
	none := voiceRig(t, nil)
	if _, b := none.do("POST", "/api/voice/test", map[string]any{}); !strings.Contains(string(b), `"ok":false`) {
		t.Fatalf("%s", b)
	}
}
