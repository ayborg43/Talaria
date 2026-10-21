// Package telegram is a Telegram Bot API gateway: long polling, an allowlist,
// one agent per chat, and inline-button approvals for risky tools.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type Message struct {
	MessageID int    `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type InlineKeyboard struct {
	InlineKeyboard [][]InlineButton `json:"inline_keyboard"`
}

// API is a minimal Bot API client.
type API struct {
	Token string
	Base  string // default https://api.telegram.org
	HTTP  *http.Client
	// RetrySleep waits out a rate limit; nil sleeps for real. Tests replace it.
	RetrySleep func(ctx context.Context, d time.Duration) error
}

// Telegram rate-limits bots (about one message or edit per second per chat) and
// says how long to wait. A wait up to maxRetryAfter is honoured, up to
// maxRateLimitTries attempts.
const (
	maxRetryAfter     = 30 * time.Second
	maxRateLimitTries = 3
)

// APIError is an error reply from the Bot API.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration // set on 429
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %s (code %d)", e.Method, e.Description, e.Code)
}

func NewAPI(token string) *API {
	return &API{
		Token: token,
		Base:  "https://api.telegram.org",
		HTTP:  &http.Client{Timeout: 45 * time.Second}, // longer than the 30s poll
	}
}

// redactedError hides the bot token, which net/http embeds in URL errors.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

func (a *API) call(ctx context.Context, method string, params, out any) error {
	for attempt := 1; ; attempt++ {
		err := a.callOnce(ctx, method, params, out)
		var ae *APIError
		if !errors.As(err, &ae) || ae.Code != 429 || attempt >= maxRateLimitTries || ae.RetryAfter > maxRetryAfter {
			return err
		}
		wait := ae.RetryAfter
		if wait <= 0 {
			wait = time.Second
		}
		if a.sleep(ctx, wait) != nil {
			return err
		}
	}
}

func (a *API) sleep(ctx context.Context, d time.Duration) error {
	if a.RetrySleep != nil {
		return a.RetrySleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *API) callOnce(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Base+"/bot"+a.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return a.redact(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return a.redact(fmt.Errorf("telegram %s: %w", method, err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return a.redact(fmt.Errorf("telegram %s: %w", method, err))
	}

	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("telegram %s: HTTP %s, undecodable response", method, resp.Status)
	}
	if !env.OK {
		return &APIError{Method: method, Code: env.ErrorCode, Description: env.Description,
			RetryAfter: time.Duration(env.Parameters.RetryAfter) * time.Second}
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func (a *API) redact(err error) error {
	return &redactedError{msg: strings.ReplaceAll(err.Error(), a.Token, "<token>"), err: err}
}

func (a *API) GetMe(ctx context.Context) (User, error) {
	var u User
	err := a.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// GetUpdates long-polls for up to timeoutSec seconds.
func (a *API) GetUpdates(ctx context.Context, offset, timeoutSec int) ([]Update, error) {
	var ups []Update
	err := a.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

// SendMessage sends plain text (no parse mode, so model output can never be
// rejected for bad markup) and returns the new message id.
func (a *API) SendMessage(ctx context.Context, chatID int64, text string, kb *InlineKeyboard) (int, error) {
	params := map[string]any{"chat_id": chatID, "text": text}
	if kb != nil {
		params["reply_markup"] = kb
	}
	var m Message
	err := a.call(ctx, "sendMessage", params, &m)
	return m.MessageID, err
}

// EditMessageText replaces a message's text and drops its inline keyboard.
func (a *API) EditMessageText(ctx context.Context, chatID int64, messageID int, text string) error {
	return a.call(ctx, "editMessageText", map[string]any{"chat_id": chatID, "message_id": messageID, "text": text}, nil)
}

func (a *API) AnswerCallback(ctx context.Context, id, text string) error {
	return a.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

func (a *API) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return a.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action}, nil)
}
