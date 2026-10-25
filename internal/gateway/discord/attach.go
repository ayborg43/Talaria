package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CreateAttachment posts a file to a channel as a native attachment.
func (a *API) CreateAttachment(ctx context.Context, channelID, name string, data []byte) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	payload, err := json.Marshal(map[string]any{"content": name, "allowed_mentions": noMentions, "flags": flagSuppressEmbeds})
	if err != nil {
		return err
	}
	if err := w.WriteField("payload_json", string(payload)); err != nil {
		return err
	}
	part, err := w.CreateFormFile("files[0]", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	base := a.Base
	if base == "" {
		base = defaultBase
	}
	label := "POST /channels/" + channelID + "/messages"
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/channels/"+channelID+"/messages", &body)
		if err != nil {
			return a.redact(err)
		}
		// The body is reread on retry.
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body.Bytes())), nil }
		req.Header.Set("Authorization", "Bot "+a.Token)
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", w.FormDataContentType())
		resp, err := a.HTTP.Do(req)
		if err != nil {
			return a.redact(fmt.Errorf("discord %s: %w", label, err))
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return a.redact(fmt.Errorf("discord %s: %w", label, err))
		}
		if resp.StatusCode < 300 {
			return nil
		}
		var e struct {
			Code       int     `json:"code"`
			Message    string  `json:"message"`
			RetryAfter float64 `json:"retry_after"`
		}
		json.Unmarshal(raw, &e) // an HTML error page from a proxy is not JSON; the status says enough
		if e.Message == "" {
			e.Message = http.StatusText(resp.StatusCode)
		}
		retry := time.Duration(e.RetryAfter * float64(time.Second))
		if retry == 0 {
			if s, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil {
				retry = time.Duration(s * float64(time.Second))
			}
		}
		if resp.StatusCode != http.StatusTooManyRequests || attempt >= maxRateLimitTries || retry > maxRetryAfter {
			return &APIError{Method: label, Status: resp.StatusCode, Code: e.Code, Message: e.Message, RetryAfter: retry}
		}
		wait := retry
		if wait <= 0 {
			wait = time.Second
		}
		if a.sleep(ctx, wait) != nil {
			return &APIError{Method: label, Status: resp.StatusCode, Code: e.Code, Message: e.Message, RetryAfter: retry}
		}
	}
}
