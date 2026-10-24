// Package notify gives the agent a way to message the user outside the conversation it is in: from the web app, or
// from a scheduled job, to the user's own Telegram.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"talaria/internal/tool"
)

const (
	// maxRunes is the longest message sent; the chat service splits it to fit.
	maxRunes = 8000
	// perHour is how many messages the agent may send in an hour: a loop gone wrong must not flood a phone.
	perHour = 30
)

// Sender delivers text to the user.
type Sender func(ctx context.Context, text string) error

// Limit lets perHour messages through in any hour.
type Limit struct {
	mu   sync.Mutex
	Now  func() time.Time
	sent []time.Time
}

func (l *Limit) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.Now != nil {
		now = l.Now()
	}
	keep := l.sent[:0]
	for _, t := range l.sent {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	l.sent = keep
	if len(l.sent) >= perHour {
		return false
	}
	l.sent = append(l.sent, now)
	return true
}

// RegisterTool gives the agent telegram_notify. The message goes to the user themselves, and only to them, so it
// needs no permission; the limit is shared by every agent that uses the same Limit.
func RegisterTool(r *tool.Registry, send Sender, lim *Limit) {
	r.Register(tool.Tool{
		Name: "telegram_notify",
		Description: "Send a message to the user's own Telegram. Use it when they ask to be sent or told something there, " +
			"for example the result of a task once it is finished: write the message so it reads on its own, with the " +
			"findings or outcome rather than \"done\". It only ever reaches the user, never anyone else.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"text":{"type":"string","description":"the message: plain text, up to a few thousand characters"}},"required":["text"]}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			text := strings.TrimSpace(a.Text)
			if text == "" {
				return "", errors.New("text is empty")
			}
			if r := []rune(text); len(r) > maxRunes {
				text = string(r[:maxRunes]) + "\n[cut: too long]"
			}
			if lim != nil && !lim.allow() {
				return "", fmt.Errorf("not sent: that is more than %d Telegram messages in an hour", perHour)
			}
			if err := send(ctx, text); err != nil {
				return "", fmt.Errorf("could not send it to Telegram: %w", err)
			}
			return "Sent to the user's Telegram.", nil
		},
	})
}
