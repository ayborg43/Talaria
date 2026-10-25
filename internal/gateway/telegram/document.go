package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
)

// SendDocument delivers a file to a chat as a native document.
func (a *API) SendDocument(ctx context.Context, chatID int64, name string, data []byte) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	part, err := w.CreateFormFile("document", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Base+"/bot"+a.Token+"/sendDocument", &body)
	if err != nil {
		return a.redact(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return a.redact(fmt.Errorf("telegram sendDocument: %w", err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return a.redact(fmt.Errorf("telegram sendDocument: %w", err))
	}
	var env struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		ErrorCode   int    `json:"error_code"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("telegram sendDocument: HTTP %s, undecodable response", resp.Status)
	}
	if !env.OK {
		return &APIError{Method: "sendDocument", Code: env.ErrorCode, Description: env.Description}
	}
	return nil
}
