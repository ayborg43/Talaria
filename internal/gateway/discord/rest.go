package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBase = "https://discord.com/api/v10"
	// userAgent is required in the form "DiscordBot (url, version)".
	userAgent = "DiscordBot (https://github.com/talaria, 1.0) talaria"

	// Discord says how long to wait when it rate-limits a request. A wait up to
	// maxRetryAfter is honoured, up to maxRateLimitTries attempts.
	maxRetryAfter     = 30 * time.Second
	maxRateLimitTries = 3
)

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot"`
}

type Message struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	Content   string `json:"content"`
	Author    User   `json:"author"`
}

type Channel struct {
	ID string `json:"id"`
}

// Interaction is what a press of a message button arrives as.
type Interaction struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	Type      int    `json:"type"`
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	User      *User  `json:"user"` // set in a DM
	Member    *struct {
		User User `json:"user"`
	} `json:"member"` // set in a server
	Data struct {
		CustomID string `json:"custom_id"`
	} `json:"data"`
	Message *Message `json:"message"`
}

// Button components (Discord's message component types and button styles).
type Component struct {
	Type       int         `json:"type"`
	Style      int         `json:"style,omitempty"`
	Label      string      `json:"label,omitempty"`
	CustomID   string      `json:"custom_id,omitempty"`
	Components []Component `json:"components,omitempty"`
}

const (
	componentRow    = 1
	componentButton = 2
	stylePrimary    = 1
	styleSuccess    = 3
	styleDanger     = 4
)

// API is a minimal Discord REST client.
type API struct {
	Token string
	Base  string // default https://discord.com/api/v10
	HTTP  *http.Client
	// RetrySleep waits out a rate limit; nil sleeps for real. Tests replace it.
	RetrySleep func(ctx context.Context, d time.Duration) error
}

// APIError is an error reply from Discord.
type APIError struct {
	Method     string // e.g. "POST /channels/1/messages"
	Status     int
	Code       int // Discord's own error code, such as 10008 for an unknown message
	Message    string
	RetryAfter time.Duration // set on 429
}

func (e *APIError) Error() string {
	return fmt.Sprintf("discord %s: %s (HTTP %d, code %d)", e.Method, e.Message, e.Status, e.Code)
}

func NewAPI(token string) *API {
	return &API{Token: token, Base: defaultBase, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (a *API) call(ctx context.Context, method, path string, body, out any) error {
	for attempt := 1; ; attempt++ {
		err := a.callOnce(ctx, method, path, body, out)
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != http.StatusTooManyRequests || attempt >= maxRateLimitTries || ae.RetryAfter > maxRetryAfter {
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

func (a *API) callOnce(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	base := a.Base
	if base == "" {
		base = defaultBase
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, rdr)
	if err != nil {
		return a.redact(err)
	}
	req.Header.Set("Authorization", "Bot "+a.Token)
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	label := method + " " + path
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return a.redact(fmt.Errorf("discord %s: %w", label, err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return a.redact(fmt.Errorf("discord %s: %w", label, err))
	}

	if resp.StatusCode >= 300 {
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
		return &APIError{Method: label, Status: resp.StatusCode, Code: e.Code, Message: e.Message, RetryAfter: retry}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// redact keeps the bot token out of errors. It travels only in a header, but a
// proxy or a client library could still echo it, and logs get shared.
func (a *API) redact(err error) error {
	if a.Token == "" {
		return err
	}
	return &redactedError{msg: strings.ReplaceAll(err.Error(), a.Token, "<token>"), err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// GetMe returns the bot's own user, which also checks the token.
func (a *API) GetMe(ctx context.Context) (User, error) {
	var u User
	err := a.call(ctx, http.MethodGet, "/users/@me", nil, &u)
	return u, err
}

// GatewayURL asks where to open the Gateway WebSocket.
func (a *API) GatewayURL(ctx context.Context) (string, error) {
	var g struct {
		URL string `json:"url"`
	}
	if err := a.call(ctx, http.MethodGet, "/gateway/bot", nil, &g); err != nil {
		return "", err
	}
	if g.URL == "" {
		return "", errors.New("discord did not say where its gateway is")
	}
	return g.URL, nil
}

// noMentions stops anything the model writes, or anything it quotes, from
// pinging a person, a role or everyone.
var noMentions = map[string]any{"parse": []string{}}

// CreateMessage posts text, with buttons if components is not empty.
func (a *API) CreateMessage(ctx context.Context, channelID, content string, components []Component) (Message, error) {
	body := map[string]any{"content": content, "allowed_mentions": noMentions, "flags": flagSuppressEmbeds}
	if len(components) > 0 {
		body["components"] = components
	}
	var m Message
	err := a.call(ctx, http.MethodPost, "/channels/"+channelID+"/messages", body, &m)
	return m, err
}

// EditMessage replaces a message's text and removes its buttons.
func (a *API) EditMessage(ctx context.Context, channelID, messageID, content string) error {
	body := map[string]any{"content": content, "allowed_mentions": noMentions, "components": []Component{}, "flags": flagSuppressEmbeds}
	return a.call(ctx, http.MethodPatch, "/channels/"+channelID+"/messages/"+messageID, body, nil)
}

// Typing shows "is typing" in a channel for a few seconds.
func (a *API) Typing(ctx context.Context, channelID string) error {
	return a.call(ctx, http.MethodPost, "/channels/"+channelID+"/typing", nil, nil)
}

// OpenDM returns the direct-message channel with a user, creating it if needed.
// Asking again for the same user gives the same channel.
func (a *API) OpenDM(ctx context.Context, userID string) (Channel, error) {
	var c Channel
	err := a.call(ctx, http.MethodPost, "/users/@me/channels", map[string]any{"recipient_id": userID}, &c)
	return c, err
}

// Interaction response types.
const (
	respMessage       = 4 // reply with a new message
	respUpdateMessage = 7 // replace the message the button is on
	flagEphemeral     = 64
	// flagSuppressEmbeds stops Discord fetching a link in a message to show a card for it, which is a
	// request, made by Discord, to whatever address the model wrote.
	flagSuppressEmbeds = 4
)

// Respond answers a button press. Discord wants an answer within three seconds.
func (a *API) Respond(ctx context.Context, in Interaction, kind int, content string, ephemeral bool) error {
	data := map[string]any{"content": content, "allowed_mentions": noMentions, "flags": flagSuppressEmbeds}
	switch {
	case kind == respUpdateMessage:
		data["components"] = []Component{}
	case ephemeral:
		data["flags"] = flagEphemeral | flagSuppressEmbeds
	}
	err := a.call(ctx, http.MethodPost, "/interactions/"+in.ID+"/"+in.Token+"/callback", map[string]any{"type": kind, "data": data}, nil)
	if err != nil && in.Token != "" { // the interaction token is in the URL, and works for a while
		err = &redactedError{msg: strings.ReplaceAll(err.Error(), in.Token, "<token>"), err: err}
	}
	return err
}
