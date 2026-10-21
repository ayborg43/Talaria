// Package chat is the part of a chat gateway that does not depend on the chat
// service: it keeps one conversation per chat, runs the agent on a worker for
// each, streams replies by editing a message, asks for approval with buttons,
// and serves /new, /stop, /model and /env. A service such as Telegram or Discord
// supplies a Transport and feeds it what users send.
package chat

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Transport is everything the engine needs from a chat service. Chats and
// messages are identified by int64 on every service the engine supports.
//
// The engine serves private chats only, in which the chat and the user are the
// same identity, so a chat is named by its user's ID.
type Transport interface {
	// Service names the service for users: "Telegram", "Discord".
	Service() string
	// MaxMessage is the most characters to put in one message, with headroom
	// under the service's own limit.
	MaxMessage() int
	// EditInterval is how often a streamed reply may be edited without
	// running into the service's rate limits.
	EditInterval() time.Duration
	// CommandPrefix is what users type before a command: "/" or "!". The engine
	// is always handed commands in its own form, starting with "/"; this is only
	// how it writes them to users.
	CommandPrefix() string
	// Markdown reports whether the service renders Markdown. If so, a code block
	// cut across two messages is closed and reopened so both render properly.
	Markdown() bool

	// Send posts text to a chat and returns the new message's ID.
	Send(ctx context.Context, chat int64, text string) (msg int64, err error)
	// Edit replaces the text of a message the bot sent, and removes its buttons.
	Edit(ctx context.Context, chat, msg int64, text string) error
	// Classify says what an Edit error means for the caller.
	Classify(err error) EditOutcome
	// Typing shows that the bot is working.
	Typing(ctx context.Context, chat int64) error
	// SendApproval posts text with Allow and Deny buttons, and an Always button
	// too when always is set. A button press must reach the engine's Decide with
	// the same token, in the form ApprovalData builds.
	SendApproval(ctx context.Context, chat int64, text, token string, always bool) (msg int64, err error)
}

// EditOutcome is what an Edit error means.
type EditOutcome int

const (
	EditFailed    EditOutcome = iota // an error worth logging
	EditUnchanged                    // the message already shows this text; not a problem
	EditGone                         // the message no longer exists, so start a fresh one
)

// Choice is the answer to an approval prompt.
type Choice int

const (
	ChoiceDeny   Choice = iota // do not run it
	ChoiceAllow                // run it this once
	ChoiceAlways               // run it, and do not ask again for this tool in this chat
)

var choiceCode = [...]string{ChoiceDeny: "n", ChoiceAllow: "y", ChoiceAlways: "a"}

// ApprovalData is what an approval button carries back, so every service can
// use one format: "ap:<token>:y" to allow, "ap:<token>:n" to deny and
// "ap:<token>:a" to always allow.
func ApprovalData(token string, c Choice) string { return "ap:" + token + ":" + choiceCode[c] }

// ParseApproval reads what ApprovalData built.
func ParseApproval(data string) (token string, c Choice, ok bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "ap" || parts[1] == "" {
		return "", 0, false
	}
	for choice, code := range choiceCode {
		if parts[2] == code {
			return parts[1], Choice(choice), true
		}
	}
	return "", 0, false
}

// What a user is told about a button press.
const (
	MsgExpired    = "This request has expired."
	MsgNotAllowed = "Not allowed."
)

// Verdict is the line added to an approval prompt once it is answered.
func Verdict(c Choice) string {
	switch c {
	case ChoiceAllow:
		return "Allowed."
	case ChoiceAlways:
		return "Allowed, and always allowed in this chat."
	}
	return "Denied."
}

// Deliver sends text to a chat, split to fit the service's limit. It needs no
// running engine, so a scheduler can use it from any goroutine.
func Deliver(ctx context.Context, t Transport, chat int64, text string, logf func(string, ...any)) {
	parts := SplitMessage(text, t.MaxMessage(), t.Markdown())
	if len(parts) > maxReplyParts {
		parts = append(parts[:maxReplyParts-1:maxReplyParts-1], cutNote(maxReplyParts))
	}
	for _, part := range parts {
		if _, err := t.Send(ctx, chat, part); err != nil {
			if logf != nil {
				logf("%s: send failed: %v", strings.ToLower(t.Service()), err)
			}
			return
		}
	}
}

func allowlistVar(service string) string {
	return fmt.Sprintf("TALARIA_%s_ALLOWED_USERS", strings.ToUpper(service))
}

// maxReplyParts is the most messages one reply may become. A reply past that is cut, with a
// note: thousands of messages in a few minutes is how a chat service bans a bot.
const maxReplyParts = 20

func cutNote(parts int) string {
	return fmt.Sprintf("\n[The rest of this reply was left out: it was longer than %d messages.]", parts)
}
