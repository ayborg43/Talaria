// Package speech turns a short voice recording into text by asking a speech-to-text service. It speaks two
// dialects: an OpenAI-style endpoint (which Groq and the local Whisper servers also offer) and Meta's Muse Voice
// Transcribe. Both take a WAV file, and the app always sends the same one: mono, 16-bit, 16 kHz, which is small
// (about 2 MB a minute) and plays in every browser the same way, so no provider is asked to guess a phone's format.
//
// Nothing here stores audio or text. A recording lives in memory for the one request and is dropped.
package speech

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// Providers.
const (
	OpenAI = "openai" // an OpenAI-style /audio/transcriptions endpoint
	Muse   = "muse"   // Meta's Muse Voice Transcribe
)

// Defaults says where a provider is, the model to ask for, and the environment variable its key is usually in.
func Defaults(provider string) (baseURL, model, keyEnv string) {
	switch provider {
	case Muse:
		return "https://api.meta.ai/v1", "muse-voice-transcribe-1.0", "MODEL_API_KEY"
	default:
		return "https://api.openai.com/v1", "whisper-1", "OPENAI_API_KEY"
	}
}

// Limits.
const (
	DefaultMaxSeconds = 120
	MaxSecondsCap     = 300
	MinSeconds        = 0.3
	// BytesPerSecond is what a 16 kHz mono 16-bit recording takes.
	BytesPerSecond = 16000 * 2
)

// MaxBytes is the most a recording of that many seconds can take, with room for the WAV header.
func MaxBytes(seconds int) int { return seconds*BytesPerSecond + 4096 }

// Errors a caller can tell apart; each message is safe to show the user.
var (
	ErrNotWAV   = errors.New("that is not a recording this app can read: it should be a mono 16-bit 16 kHz WAV")
	ErrTooLong  = errors.New("that recording is too long")
	ErrTooShort = errors.New("that recording is too short")
	ErrRefused  = errors.New("the speech service refused the key: check it in Settings, Voice")
	ErrBusy     = errors.New("the speech service is busy or you have reached its limit: try again in a minute")
	ErrDown     = errors.New("the speech service did not answer")
)

// Info says what a WAV holds.
type Info struct {
	Rate    int
	Seconds float64
}

// Check reads a WAV header and refuses anything but mono 16-bit PCM at 16 or 24 kHz, no longer than maxSeconds. The
// app only ever sends one it made itself, so this is what stands between the speech service and whatever else
// might be posted to the endpoint.
func Check(data []byte, maxSeconds int) (Info, error) {
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return Info{}, ErrNotWAV
	}
	var rate, channels, bits, format, dataLen int
	var haveFmt, haveData bool
	for p := 12; p+8 <= len(data); {
		id, size := string(data[p:p+4]), int(binary.LittleEndian.Uint32(data[p+4:p+8]))
		body := p + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(data) {
				return Info{}, ErrNotWAV
			}
			format = int(binary.LittleEndian.Uint16(data[body : body+2]))
			channels = int(binary.LittleEndian.Uint16(data[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(data[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(data[body+14 : body+16]))
			haveFmt = true
		case "data":
			dataLen = min(size, len(data)-body) // a streamed file may claim more than it holds
			haveData = true
		}
		if haveFmt && haveData {
			break
		}
		if size < 0 || body+size < body {
			return Info{}, ErrNotWAV
		}
		p = body + size + size%2
	}
	if !haveFmt || !haveData || format != 1 || channels != 1 || bits != 16 || (rate != 16000 && rate != 24000) {
		return Info{}, ErrNotWAV
	}
	secs := float64(dataLen) / float64(rate*2)
	switch {
	case secs < MinSeconds:
		return Info{}, ErrTooShort
	case secs > float64(maxSeconds):
		return Info{}, ErrTooLong
	}
	return Info{Rate: rate, Seconds: secs}, nil
}

// Silence is a WAV of that many seconds of silence, for checking that a service and its key work without any
// recording from the user.
func Silence(seconds float64) []byte {
	n := int(seconds * 16000)
	b := &bytes.Buffer{}
	b.WriteString("RIFF")
	binary.Write(b, binary.LittleEndian, uint32(36+n*2))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
		binary.Write(b, binary.LittleEndian, v) // fmt chunk: PCM, mono, 16 kHz, 16-bit
	}
	b.WriteString("data")
	binary.Write(b, binary.LittleEndian, uint32(n*2))
	b.Write(make([]byte, n*2))
	return b.Bytes()
}

// Service is one speech-to-text service as the user set it up.
type Service struct {
	Provider   string
	BaseURL    string
	Model      string
	Key        string
	Language   string // OpenAI: a code such as en. Muse: a name such as English. Empty lets the service detect it.
	Vocabulary string // names and terms to listen for, separated by commas
	HTTP       *http.Client
}

func (s *Service) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// Transcribe sends one WAV and returns what was said, trimmed. The audio is not kept.
func (s *Service) Transcribe(ctx context.Context, wav []byte) (string, error) {
	if s.Provider == Muse {
		return s.muse(ctx, wav)
	}
	return s.openai(ctx, wav)
}

func (s *Service) post(ctx context.Context, url string, body *bytes.Buffer, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, ErrDown
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	if s.Key != "" {
		req.Header.Set("Authorization", "Bearer "+s.Key)
	}
	resp, err := s.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrDown // the address and the key stay out of what is shown
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrRefused
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, ErrBusy
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		return nil, ErrTooLong
	case resp.StatusCode == http.StatusBadRequest:
		return nil, fmt.Errorf("the speech service did not accept the recording (%s)", firstLine(raw))
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("the speech service answered %d", resp.StatusCode)
	}
	return raw, nil
}

// firstLine is a short, one-line part of an error body, for a message that must not run on or echo secrets.
func firstLine(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &e) == nil {
		if e.Error.Message != "" {
			msg = e.Error.Message
		} else if e.Message != "" {
			msg = e.Message
		}
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if r := []rune(msg); len(r) > 120 {
		msg = string(r[:120]) + "…"
	}
	return msg
}

func (s *Service) openai(ctx context.Context, wav []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "voice.wav")
	fw.Write(wav)
	mw.WriteField("model", s.Model)
	mw.WriteField("response_format", "json")
	if s.Language != "" {
		mw.WriteField("language", s.Language)
	}
	if s.Vocabulary != "" {
		mw.WriteField("prompt", s.Vocabulary) // Whisper's way of favouring names and terms
	}
	mw.Close()
	raw, err := s.post(ctx, strings.TrimRight(s.BaseURL, "/")+"/audio/transcriptions", &body, mw.FormDataContentType())
	if err != nil {
		return "", err
	}
	var out struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", errors.New("the speech service answered in a way this app does not understand")
	}
	return strings.TrimSpace(out.Text), nil
}

func (s *Service) muse(ctx context.Context, wav []byte) (string, error) {
	req := map[string]any{"model": s.Model, "audioEncoding": "WAV", "mode": "PUSH_TO_TALK"}
	if s.Language != "" {
		req["languageBias"] = []string{s.Language}
	}
	if kw := splitList(s.Vocabulary); len(kw) > 0 {
		req["keywords"] = kw
	}
	reqJSON, _ := json.Marshal(req)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="request"`)
	h.Set("Content-Type", "application/json")
	part, _ := mw.CreatePart(h)
	part.Write(reqJSON)
	ah := textproto.MIMEHeader{}
	ah.Set("Content-Disposition", `form-data; name="audio"; filename="voice.wav"`)
	ah.Set("Content-Type", "audio/wav")
	ap, _ := mw.CreatePart(ah)
	ap.Write(wav)
	mw.Close()
	raw, err := s.post(ctx, strings.TrimRight(s.BaseURL, "/")+"/asr/transcribe", &body, mw.FormDataContentType())
	if err != nil {
		return "", err
	}
	var out struct {
		Transcript string `json:"transcript"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", errors.New("the speech service answered in a way this app does not understand")
	}
	return strings.TrimSpace(out.Transcript), nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
