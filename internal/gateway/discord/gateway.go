package discord

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"talaria/internal/agent"
	"talaria/internal/gateway/chat"
)

const (
	messageLimit = 1900 // Discord allows 2000 characters; leave headroom
	// Discord allows about five edits per five seconds in a channel.
	editInterval = 1500 * time.Millisecond

	unknownMessage = 10008 // Discord's error code for a message that no longer exists
)

// maxRemembered bounds the direct-message channels kept for people who are not
// on the allowlist: anyone may message the bot.
var maxRemembered = 10000

// Gateway connects Discord direct messages to agents. It holds a Gateway
// session for events and hands them to a chat.Engine, which does the rest.
//
// Only direct messages from allowlisted users are served: messages in servers
// are ignored, so the bot can be invited to one (a bot must share a server with
// someone before they can message it) without anyone there being able to use it.
// With an empty allowlist it runs in setup mode: it never starts an agent and
// only tells people their user ID so it can be added.
type Gateway struct {
	API     *API
	Allowed map[int64]bool
	// NewAgent builds a fresh conversation for a chat. approve must be installed
	// as the agent's Approve func so risky tools prompt through Discord.
	NewAgent func(chatID int64, approve func(name, args string) bool) *agent.Agent
	Logf     func(format string, args ...any)

	ApprovalTimeout time.Duration // default 5m

	// Stream shows replies as they are written, by editing one message in place.
	Stream       bool
	EditInterval time.Duration // between edits of a streamed reply; default 1.5s

	// Environment, if set, says where the shell and file tools run; it is shown in /help.
	Environment string

	// Environments, if set, lets each chat choose where its shell and file tools
	// run with !env, among the ones the operator allows.
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

	// Backends, if set, lets each chat choose its model profile with !model;
	// Prefs, if also set, remembers the choice across restarts.
	Backends *chat.Backends
	Prefs    chat.ChatPrefs

	// OutboxPath, if set, keeps messages that could not be delivered there and
	// retries them in the background.
	OutboxPath string

	// HeldPath, if set, keeps requests for permission there until the user answers, and Destructive says only
	// deleting and posting ask; see chat.Engine.
	HeldPath    string
	Destructive bool

	// GatewayURL, if set, is the WebSocket to connect to instead of the one
	// Discord names. Tests use it.
	GatewayURL string
	// Session timing, replaceable by tests.
	Backoff func(attempt int) time.Duration

	once sync.Once
	eng  *chat.Engine
	tr   *transport
}

func (g *Gateway) setup() {
	g.once.Do(func() {
		g.tr = &transport{api: g.API, channels: map[int64]string{}, users: map[string]int64{}}
		g.eng = &chat.Engine{
			T: g.tr, Allowed: g.Allowed, NewAgent: g.NewAgent, Logf: g.Logf,
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
}

func (g *Gateway) logf(format string, args ...any) {
	if g.Logf != nil {
		g.Logf(format, args...)
	}
}

// Run stays connected until ctx ends. It returns an error only when Discord
// says reconnecting cannot help, such as a rejected token.
func (g *Gateway) Run(ctx context.Context) error {
	g.setup()
	g.eng.Start(ctx)

	var sess *Session
	sess = &Session{
		API: g.API, Logf: g.Logf, URL: g.GatewayURL, Backoff: g.Backoff,
		Handle: func(event string, data json.RawMessage) {
			switch event {
			case "MESSAGE_CREATE":
				g.onMessage(sess.UserID(), data)
			case "INTERACTION_CREATE":
				go g.onInteraction(ctx, data) // answering is a network call; the session must keep reading
			}
		},
	}
	err := sess.Run(ctx)
	g.eng.Wait(5 * time.Second)
	return err
}

// Send delivers text to a user's direct messages, split to fit Discord's size
// limit. It is safe to call from other goroutines (for example the cron
// scheduler), and works for a user who has not written to the bot since it
// started.
func (g *Gateway) Send(ctx context.Context, userID int64, text string) {
	g.setup()
	g.eng.Send(ctx, userID, text)
}

// Selection reports the model profile and the environment a chat has chosen
// with !model and !env, as names; "" means the default.
func (g *Gateway) Selection(userID int64) (profile, environment string) {
	g.setup()
	return g.eng.Selection(userID)
}

// asCommand turns "!new" into "/new", the engine's form, for the commands it
// knows. Anything else is left alone, so "!important: ..." is just a message.
func asCommand(text string, known func(string) bool) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "!") {
		return text
	}
	word, _, _ := strings.Cut(text[1:], " ")
	if !known(strings.ToLower(word)) {
		return text
	}
	return "/" + text[1:]
}

func (g *Gateway) onMessage(me string, data json.RawMessage) {
	var m Message
	if json.Unmarshal(data, &m) != nil {
		return
	}
	if m.GuildID != "" || m.Author.Bot || m.Author.ID == me || strings.TrimSpace(m.Content) == "" {
		return // not a private message from a person
	}
	uid, err := strconv.ParseInt(m.Author.ID, 10, 64)
	if err != nil {
		return
	}
	if g.Allowed[uid] {
		g.tr.learn(uid, m.ChannelID)
	}
	g.eng.Incoming(uid, uid, asCommand(m.Content, g.eng.HasCommand))
}

func (g *Gateway) onInteraction(ctx context.Context, data json.RawMessage) {
	var in Interaction
	if json.Unmarshal(data, &in) != nil || in.Type != 3 { // 3: a message component, which is to say a button
		return
	}
	token, choice, ok := chat.ParseApproval(in.Data.CustomID)
	if !ok {
		return
	}
	var user User
	switch {
	case in.User != nil:
		user = *in.User
	case in.Member != nil:
		user = in.Member.User
	}
	uid, perr := strconv.ParseInt(user.ID, 10, 64)
	if perr != nil || uid <= 0 {
		return // no user, or one that is not an ID: never "user 0", which a sloppy allowlist could contain
	}

	var err error
	decision, choice := g.eng.Decide(token, uid, g.tr.userOf(in.ChannelID), choice)
	switch decision {
	case chat.DecisionExpired:
		err = g.API.Respond(ctx, in, respMessage, chat.MsgExpired, true)
	case chat.DecisionForbidden:
		err = g.API.Respond(ctx, in, respMessage, chat.MsgNotAllowed, true)
	case chat.DecisionAnswered:
		err = g.API.Respond(ctx, in, respMessage, chat.MsgAnswered, true)
	default:
		// Replace the prompt with itself plus the verdict; that also removes the buttons.
		prompt := ""
		if in.Message != nil {
			prompt = in.Message.Content
		}
		err = g.API.Respond(ctx, in, respUpdateMessage, prompt+"\n\n"+chat.Verdict(choice), false)
	}
	if err != nil {
		g.logf("discord: answering a button press failed: %v", err)
	}
}

// transport is Discord as the chat engine sees it. A chat is a user, and its
// messages go to the direct-message channel with them.
type transport struct {
	api *API

	mu       sync.Mutex
	channels map[int64]string // user ID -> their DM channel with the bot
	users    map[string]int64 // channel ID -> user ID
}

func (*transport) Service() string             { return "Discord" }
func (*transport) MaxMessage() int             { return messageLimit }
func (*transport) EditInterval() time.Duration { return editInterval }
func (*transport) Markdown() bool              { return true }
func (*transport) CommandPrefix() string       { return "!" }

func (t *transport) learn(user int64, channel string) {
	t.mu.Lock()
	t.channels[user], t.users[channel] = channel, user
	t.mu.Unlock()
}

// userOf says whose DM channel this is; 0 if it is not one we know.
func (t *transport) userOf(channel string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.users[channel]
}

// channel finds the DM channel with a user, opening it the first time. Opening
// is idempotent, so asking again after a restart gives the same channel.
func (t *transport) channel(ctx context.Context, user int64) (string, error) {
	t.mu.Lock()
	ch, ok := t.channels[user]
	t.mu.Unlock()
	if ok {
		return ch, nil
	}
	c, err := t.api.OpenDM(ctx, strconv.FormatInt(user, 10))
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	if len(t.channels) < maxRemembered { // anyone may message the bot, so what it keeps of strangers is bounded
		t.channels[user], t.users[c.ID] = c.ID, user
	}
	t.mu.Unlock()
	return c.ID, nil
}

func (t *transport) Send(ctx context.Context, chatID int64, text string) (int64, error) {
	ch, err := t.channel(ctx, chatID)
	if err != nil {
		return 0, err
	}
	m, err := t.api.CreateMessage(ctx, ch, text, nil)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(m.ID, 10, 64)
}

func (t *transport) Edit(ctx context.Context, chatID, msg int64, text string) error {
	ch, err := t.channel(ctx, chatID)
	if err != nil {
		return err
	}
	return t.api.EditMessage(ctx, ch, strconv.FormatInt(msg, 10), text)
}

func (*transport) Classify(err error) chat.EditOutcome {
	var ae *APIError
	if errors.As(err, &ae) && (ae.Code == unknownMessage || ae.Status == 404) {
		return chat.EditGone
	}
	return chat.EditFailed
}

func (t *transport) Typing(ctx context.Context, chatID int64) error {
	ch, err := t.channel(ctx, chatID)
	if err != nil {
		return err
	}
	return t.api.Typing(ctx, ch)
}

func (t *transport) SendApproval(ctx context.Context, chatID int64, text, token string, always bool) (int64, error) {
	ch, err := t.channel(ctx, chatID)
	if err != nil {
		return 0, err
	}
	buttons := []Component{{Type: componentButton, Style: styleSuccess, Label: "Allow", CustomID: chat.ApprovalData(token, chat.ChoiceAllow)}}
	if always {
		buttons = append(buttons, Component{Type: componentButton, Style: stylePrimary, Label: "Always", CustomID: chat.ApprovalData(token, chat.ChoiceAlways)})
	}
	buttons = append(buttons, Component{Type: componentButton, Style: styleDanger, Label: "Deny", CustomID: chat.ApprovalData(token, chat.ChoiceDeny)})
	m, err := t.api.CreateMessage(ctx, ch, text, []Component{{Type: componentRow, Components: buttons}})
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(m.ID, 10, 64)
}
