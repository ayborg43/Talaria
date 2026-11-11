package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"talaria/internal/config"
	"talaria/internal/speech"
)

// voicePreset is a speech service the Settings page offers with its usual address and model filled in.
type voicePreset struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	KeyEnv   string `json:"key_env"`
	Note     string `json:"note,omitempty"`
}

var voicePresets = []voicePreset{
	{ID: "openai", Label: "OpenAI Whisper", Provider: "openai", BaseURL: "https://api.openai.com/v1", Model: "whisper-1", KeyEnv: "OPENAI_API_KEY"},
	{ID: "groq", Label: "Groq Whisper", Provider: "openai", BaseURL: "https://api.groq.com/openai/v1", Model: "whisper-large-v3-turbo", KeyEnv: "GROQ_API_KEY"},
	{ID: "muse", Label: "Meta Muse Voice Transcribe", Provider: "muse", BaseURL: "https://api.meta.ai/v1", Model: "muse-voice-transcribe-1.0", KeyEnv: "MODEL_API_KEY"},
	{ID: "local", Label: "A server of my own", Provider: "openai", BaseURL: "http://localhost:8080/v1", Model: "whisper-1", Note: "Any server with an OpenAI-style /audio/transcriptions, such as whisper.cpp or faster-whisper. One on this machine needs no key."},
}

type voiceView struct {
	Available  bool          `json:"available"` // a service is set up and can be used now
	Provider   string        `json:"provider"`
	BaseURL    string        `json:"base_url"`
	Model      string        `json:"model"`
	HasKey     bool          `json:"has_key"`
	KeySource  string        `json:"key_source,omitempty"` // settings or environment
	KeyEnv     string        `json:"api_key_env,omitempty"`
	Language   string        `json:"language"`
	Vocabulary string        `json:"vocabulary"`
	AutoSend   bool          `json:"auto_send"`
	MaxSeconds int           `json:"max_seconds"`
	Presets    []voicePreset `json:"presets"`
}

func (s *Server) voiceView() voiceView {
	v := voiceView{Presets: voicePresets, MaxSeconds: speech.DefaultMaxSeconds}
	if s.Cfg == nil {
		return v
	}
	t := s.Cfg.Transcription
	provider, base, model, key, maxSec := t.Resolved(s.GetenvOrEmpty)
	_, ok := t.Service(s.GetenvOrEmpty)
	v.Available = ok
	v.Provider, v.BaseURL, v.Model, v.MaxSeconds = provider, base, model, maxSec
	v.Language, v.Vocabulary, v.AutoSend, v.KeyEnv = t.Language, t.Vocabulary, t.AutoSend, t.APIKeyEnv
	if key != "" {
		v.HasKey = true
		v.KeySource = "environment"
		if strings.TrimSpace(t.APIKey) != "" {
			v.KeySource = "settings"
		}
	}
	return v
}

func (s *Server) voiceGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.voiceView())
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// voicePost saves which speech service turns voice notes into text. Like the model profiles it is read each time it
// is used, so a change applies at once. A key left blank keeps the one already saved, and is never sent back.
func (s *Server) voicePost(w http.ResponseWriter, r *http.Request) {
	if s.Cfg == nil {
		fail(w, http.StatusNotImplemented, "settings are not available here")
		return
	}
	var in struct {
		Provider   string `json:"provider"`
		BaseURL    string `json:"base_url"`
		Model      string `json:"model"`
		APIKey     string `json:"api_key"`
		ClearKey   bool   `json:"clear_key"`
		KeyEnv     string `json:"api_key_env"`
		Language   string `json:"language"`
		Vocabulary string `json:"vocabulary"`
		AutoSend   bool   `json:"auto_send"`
		MaxSeconds int    `json:"max_seconds"`
		Disconnect bool   `json:"disconnect"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	t := s.Cfg.Transcription
	if in.Disconnect {
		t = config.Transcription{}
	} else {
		one := func(v string, n int) string {
			v = strings.Join(strings.Fields(v), " ")
			if r := []rune(v); len(r) > n {
				v = string(r[:n])
			}
			return v
		}
		in.Provider = strings.TrimSpace(in.Provider)
		in.BaseURL = strings.TrimSpace(in.BaseURL)
		switch {
		case in.Provider != "" && in.Provider != speech.OpenAI && in.Provider != speech.Muse:
			fail(w, http.StatusBadRequest, "the provider is openai or muse")
			return
		case in.KeyEnv != "" && !envName.MatchString(strings.TrimSpace(in.KeyEnv)):
			fail(w, http.StatusBadRequest, "the key variable is a name such as OPENAI_API_KEY")
			return
		case in.MaxSeconds != 0 && (in.MaxSeconds < 10 || in.MaxSeconds > speech.MaxSecondsCap):
			fail(w, http.StatusBadRequest, "a voice note is 10 to 300 seconds at most")
			return
		}
		if in.BaseURL != "" {
			u, err := url.Parse(in.BaseURL)
			if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
				fail(w, http.StatusBadRequest, "the address starts with https:// (or http:// for a server on this machine)")
				return
			}
			// Audio and the key go to this address: not in the clear across the internet.
			if u.Scheme == "http" && !(u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") {
				fail(w, http.StatusBadRequest, "an address that is not on this machine must start with https://, because your voice and the key would cross the internet in the clear")
				return
			}
		}
		t.Provider, t.BaseURL, t.Model = in.Provider, in.BaseURL, one(in.Model, 80)
		t.APIKeyEnv, t.Language, t.Vocabulary = strings.TrimSpace(in.KeyEnv), one(in.Language, 40), one(in.Vocabulary, 300)
		t.AutoSend, t.MaxSeconds = in.AutoSend, in.MaxSeconds
		switch {
		case in.ClearKey:
			t.APIKey = ""
		case strings.TrimSpace(in.APIKey) != "":
			t.APIKey = strings.TrimSpace(in.APIKey)
		}
	}
	was := s.Cfg.Transcription
	s.Cfg.Transcription = t
	if err := s.saveConfigQuiet(); err != nil {
		s.Cfg.Transcription = was
		fail(w, http.StatusInternalServerError, "could not save settings")
		return
	}
	writeJSON(w, http.StatusOK, s.voiceView())
}

// voiceTest sends a second of silence to the service, to prove the address, model and key work without a recording
// from anyone. Silence comes back empty, which is the right answer.
func (s *Server) voiceTest(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.speechService()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": "No speech service is set up yet, or it needs a key that is not there."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if _, err := svc.Transcribe(ctx, speech.Silence(1)); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": "The speech service answered. (A second of silence comes back empty, which is what it should do.)"})
}

func (s *Server) speechService() (*speech.Service, bool) {
	if s.Cfg == nil {
		return nil, false
	}
	return s.Cfg.Transcription.Service(s.GetenvOrEmpty)
}

// voiceLimiter keeps one voice note at a time, and a handful a minute, so a stuck page cannot run up a bill.
type voiceLimiter struct {
	busy  bool
	times []time.Time
}

const voicePerMinute = 20

func (s *Server) voiceAcquire() (release func(), reason string) {
	s.voiceMu.Lock()
	defer s.voiceMu.Unlock()
	now := time.Now()
	recent := s.voice.times[:0]
	for _, t := range s.voice.times {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	s.voice.times = recent
	switch {
	case s.voice.busy:
		return nil, "one voice note at a time: wait for the last one to finish"
	case len(recent) >= voicePerMinute:
		return nil, "that is a lot of voice notes in a minute: wait a little"
	}
	s.voice.busy = true
	s.voice.times = append(s.voice.times, now)
	return func() { s.voiceMu.Lock(); s.voice.busy = false; s.voiceMu.Unlock() }, ""
}

// transcribe turns one recording into text. The body is the WAV itself (mono, 16-bit, 16 kHz, made by the page). It is
// held in memory for this request and then dropped: nothing is saved, and neither the audio nor the words are logged.
func (s *Server) transcribe(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.speechService()
	if !ok {
		fail(w, http.StatusNotImplemented, "no speech service is set up: add one in Settings, Voice")
		return
	}
	_, _, _, _, maxSec := s.Cfg.Transcription.Resolved(s.GetenvOrEmpty)
	if ct := strings.ToLower(r.Header.Get("Content-Type")); !strings.HasPrefix(ct, "audio/") && !strings.HasPrefix(ct, "application/octet-stream") {
		fail(w, http.StatusUnsupportedMediaType, "send the recording as audio/wav")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(speech.MaxBytes(maxSec)))
	data, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, speech.ErrTooLong.Error())
		return
	}
	info, err := speech.Check(data, maxSec)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	release, why := s.voiceAcquire()
	if release == nil {
		fail(w, http.StatusTooManyRequests, why)
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	text, err := svc.Transcribe(ctx, data)
	switch {
	case errors.Is(err, speech.ErrBusy):
		fail(w, http.StatusTooManyRequests, err.Error())
		return
	case errors.Is(err, speech.ErrTooLong), errors.Is(err, speech.ErrNotWAV):
		fail(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		if ctx.Err() != nil && r.Context().Err() != nil {
			return // the page went away
		}
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	s.logf("web: a voice note of %.1fs was turned into text", info.Seconds) // the length only: never the audio or the words
	writeJSON(w, http.StatusOK, map[string]any{"text": text, "seconds": info.Seconds})
}
