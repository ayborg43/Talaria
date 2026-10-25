package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"talaria/internal/agent"
	"talaria/internal/gateway/chat"
)

const (
	messageLimit = 4000 // Telegram allows 4096; leave headroom
	editInterval = 1200 * time.Millisecond
)

// Gateway connects Telegram chats to agents. It long-polls for updates and
// hands them to a chat.Engine, which does the rest.
//
// Only private chats with allowlisted users are served. With an empty
// allowlist it runs in setup mode: it never starts an agent and only tells
// people their user ID so it can be added.
type Gateway struct {
	API     *API
	Allowed map[int64]bool
	// NewAgent builds a fresh conversation for a chat. approve must be installed
	// as the agent's Approve func so risky tools prompt through Telegram.
	NewAgent func(chatID int64, approve func(name, args string) bool) *agent.Agent
	Logf     func(format string, args ...any)

	ApprovalTimeout time.Duration // default 5m
	PollTimeout     int           // seconds, default 30

	// Stream shows replies as they are written, by editing one message in place.
	Stream       bool
	EditInterval time.Duration // between edits of a streamed reply; default about 1.2s

	// Environment, if set, says where the shell and file tools run; it is shown in /help.
	Environment string

	// Environments, if set, lets each chat choose where its shell and file tools
	// run with /env, among the ones the operator allows.
	Environments *chat.Environments

	// Permissions, if set, adds an Always button to approval prompts and the
	// permissions and log commands.
	Permissions chat.Permissions
	// MessagesPerMinute limits how fast a chat may give the agent work; 0 is no limit.
	MessagesPerMinute int

	// Commands, OnUserMessage and StopBackground are passed to the chat engine:
	// extra commands (/tasks, /goals), a hook for when a user speaks, and a way
	// for /stop to reach background tasks.
	Commands       map[string]chat.Command
	OnUserMessage  func(chatID int64)
	StopBackground func(chatID int64) int

	// Backends, if set, lets each chat choose its model profile with /model;
	// Prefs, if also set, remembers the choice across restarts.
	Backends *chat.Backends
	Prefs    chat.ChatPrefs

	// OffsetPath, if set, persists the poll cursor there, so a restart does
	// not replay (and re-run the agent on) updates already handled.
	OffsetPath string

	// OutboxPath, if set, keeps messages that could not be delivered there and
	// retries them in the background.
	OutboxPath string

	// HeldPath, if set, keeps requests for permission there until the user answers, and Destructive says only
	// deleting and posting ask; see chat.Engine.
	HeldPath    string
	Destructive bool

	once sync.Once
	eng  *chat.Engine
}

// engine builds the chat engine from the gateway's settings, once. Send and
// Selection may be called before Run, so it cannot wait for Run to do this.
func (g *Gateway) engine() *chat.Engine {
	g.once.Do(func() {
		g.eng = &chat.Engine{
			T: transport{g.API}, Allowed: g.Allowed, NewAgent: g.NewAgent, Logf: g.Logf,
			ApprovalTimeout: g.ApprovalTimeout, Stream: g.Stream, EditInterval: g.EditInterval,
			Environment: g.Environment, Environments: g.Environments,
			Backends: g.Backends, Prefs: g.Prefs, Permissions: g.Permissions,
			Commands: g.Commands, OnUserMessage: g.OnUserMessage, StopBackground: g.StopBackground,
			MessagesPerMinute: g.MessagesPerMinute,
			HeldPath:          g.HeldPath, Destructive: g.Destructive,
		}
		if g.OutboxPath != "" {
			g.eng.Outbox = &chat.Outbox{Path: g.OutboxPath}
		}
	})
	return g.eng
}

func (g *Gateway) logf(format string, args ...any) {
	if g.Logf != nil {
		g.Logf(format, args...)
	}
}

// Run polls until ctx ends.
func (g *Gateway) Run(ctx context.Context) error {
	eng := g.engine()
	eng.Start(ctx)
	if g.PollTimeout == 0 {
		g.PollTimeout = 30
	}

	offset, backoff := loadOffset(g.OffsetPath, g.logf), time.Second
	for ctx.Err() == nil {
		updates, err := g.API.GetUpdates(ctx, offset, g.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			g.logf("poll failed (retrying in %s): %v", backoff, err)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for _, u := range updates {
			offset = u.UpdateID + 1
			g.handle(ctx, eng, u)
			saveOffset(g.OffsetPath, offset, g.logf)
		}
	}
	eng.Wait(5 * time.Second)
	return ctx.Err()
}

// handle must not block: agent work happens on the engine's per-chat workers.
func (g *Gateway) handle(ctx context.Context, eng *chat.Engine, u Update) {
	if u.CallbackQuery != nil {
		g.handleCallback(ctx, eng, u.CallbackQuery)
		return
	}
	m := u.Message
	if m == nil || m.From == nil || m.Text == "" || m.Chat.Type != "private" {
		return
	}
	eng.Incoming(m.Chat.ID, m.From.ID, m.Text)
}

func (g *Gateway) handleCallback(ctx context.Context, eng *chat.Engine, q *CallbackQuery) {
	if panelID, idx, ok := chat.ParseChoice(q.Data); ok {
		g.handlePick(ctx, eng, q, panelID, idx)
		return
	}
	token, choice, ok := chat.ParseApproval(q.Data)
	if !ok {
		return
	}
	var chatID int64
	if q.Message != nil {
		chatID = q.Message.Chat.ID
	}
	decision, choice := eng.Decide(token, q.From.ID, chatID, choice)
	switch decision {
	case chat.DecisionExpired:
		g.API.AnswerCallback(ctx, q.ID, chat.MsgExpired)
	case chat.DecisionForbidden:
		g.API.AnswerCallback(ctx, q.ID, chat.MsgNotAllowed)
	case chat.DecisionAnswered:
		g.API.AnswerCallback(ctx, q.ID, chat.MsgAnswered)
	default:
		verdict := chat.Verdict(choice)
		g.API.AnswerCallback(ctx, q.ID, verdict)
		if q.Message == nil { // an inline answer carries no message to rewrite; the verdict stands on its own
			return
		}
		g.API.EditMessageText(ctx, chatID, q.Message.MessageID, q.Message.Text+"\n\n"+verdict)
	}
}

// handlePick turns a tapped choice into the words the agent records: the
// panel and the option number, exactly what choice_select needs. Taps from a
// chat with no message carry no conversation and are only acknowledged.
func (g *Gateway) handlePick(ctx context.Context, eng *chat.Engine, q *CallbackQuery, panelID string, idx int) {
	verdict := fmt.Sprintf("Picked option %d.", idx+1)
	g.API.AnswerCallback(ctx, q.ID, verdict)
	if q.Message == nil {
		return
	}
	eng.Incoming(q.Message.Chat.ID, q.From.ID, chat.PickText(panelID, idx))
	g.API.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, q.Message.Text+"\n\n"+verdict)
}

// Send delivers text to a chat, split to fit Telegram's size limit. It is safe
// to call from other goroutines (for example the cron scheduler).
func (g *Gateway) Send(ctx context.Context, chatID int64, text string) {
	g.engine().Send(ctx, chatID, text)
}

// SendFile delivers a kept file to a chat as a native document.
func (g *Gateway) SendFile(ctx context.Context, chatID int64, name string, data []byte) error {
	return g.API.SendDocument(ctx, chatID, name, data)
}

// Selection reports the model profile and the environment a chat has chosen
// with /model and /env, as names; "" means the default.
func (g *Gateway) Selection(chatID int64) (profile, environment string) {
	return g.engine().Selection(chatID)
}

// transport is Telegram as the chat engine sees it. Messages are plain text, so
// model output can never be rejected for bad markup.
type transport struct{ api *API }

func (transport) Service() string             { return "Telegram" }
func (transport) MaxMessage() int             { return messageLimit }
func (transport) EditInterval() time.Duration { return editInterval }
func (transport) Markdown() bool              { return false }
func (transport) CommandPrefix() string       { return "/" }

func (t transport) Send(ctx context.Context, chatID int64, text string) (int64, error) {
	if id, labels, ok := chat.ParseChoicePanel(text); ok {
		rows := make([][]InlineButton, 0, len(labels))
		for i, l := range labels {
			rows = append(rows, []InlineButton{{Text: clipButton(l), CallbackData: chat.ChoiceData(id, i)}})
		}
		sent, err := t.api.SendMessage(ctx, chatID, text, &InlineKeyboard{InlineKeyboard: rows})
		return int64(sent), err
	}
	id, err := t.api.SendMessage(ctx, chatID, text, nil)
	return int64(id), err
}

// clipButton keeps a button label readable: Telegram truncates long ones.
func clipButton(s string) string {
	if r := []rune(s); len(r) > 32 {
		return string(r[:31]) + "\u2026"
	}
	return s
}

func (t transport) Edit(ctx context.Context, chatID, msg int64, text string) error {
	return t.api.EditMessageText(ctx, chatID, int(msg), text)
}

func (transport) Classify(err error) chat.EditOutcome {
	m := err.Error()
	switch {
	case strings.Contains(m, "not modified"):
		return chat.EditUnchanged
	case strings.Contains(m, "not found"), strings.Contains(m, "MESSAGE_ID_INVALID"):
		return chat.EditGone
	}
	return chat.EditFailed
}

func (t transport) Typing(ctx context.Context, chatID int64) error {
	return t.api.SendChatAction(ctx, chatID, "typing")
}

func (t transport) SendApproval(ctx context.Context, chatID int64, text, token string, always bool) (int64, error) {
	row := []InlineButton{{Text: "Allow", CallbackData: chat.ApprovalData(token, chat.ChoiceAllow)}}
	if always {
		row = append(row, InlineButton{Text: "Always", CallbackData: chat.ApprovalData(token, chat.ChoiceAlways)})
	}
	row = append(row, InlineButton{Text: "Deny", CallbackData: chat.ApprovalData(token, chat.ChoiceDeny)})
	id, err := t.api.SendMessage(ctx, chatID, text, &InlineKeyboard{InlineKeyboard: [][]InlineButton{row}})
	return int64(id), err
}
