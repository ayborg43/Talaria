package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"talaria/internal/agent"
	"talaria/internal/execenv"
	"talaria/internal/metrics"
)

const (
	queueDepth      = 8
	approvalArgsMax = 900
	// approvalArgsHard is the most that is put to a person to approve at all.
	approvalArgsHard = 12000
)

// Engine connects the private chats of one chat service to agents.
//
// Only allowlisted users are served. With an empty allowlist it runs in setup
// mode: it never starts an agent and only tells people their user ID so it can
// be added.
type Engine struct {
	T       Transport
	Allowed map[int64]bool
	// NewAgent builds a fresh conversation for a chat. approve must be installed
	// as the agent's Approve func so risky tools prompt through the chat.
	NewAgent func(chatID int64, approve func(name, args string) bool) *agent.Agent
	Logf     func(format string, args ...any)

	ApprovalTimeout time.Duration // default 5m

	// Permissions, if set, adds an Always button to approval prompts, and the
	// permissions and log commands.
	Permissions Permissions

	// Commands are further commands this service's users may type, such as
	// /tasks and /goals, keyed by name without the prefix.
	Commands map[string]Command
	// OnUserMessage, if set, is told when a user sends a message for the agent
	// (not a command): the user is there, and has a say in anything under way.
	OnUserMessage func(chatID int64)
	// StopBackground, if set, stops the chat's background tasks and says how many.
	// /stop uses it when nothing is running in the conversation itself.
	StopBackground func(chatID int64) int

	// Stream shows replies as they are written, by editing one message in place.
	Stream       bool
	EditInterval time.Duration // overrides the transport's own interval when set

	// Environment, if set, says where the shell and file tools run; it is shown in /help.
	Environment string

	// Environments, if set, lets each chat choose where its shell and file tools
	// run with /env, among the ones the operator allows.
	Environments *Environments

	// Backends, if set, lets each chat choose its model profile with /model;
	// Prefs, if also set, remembers the choices across restarts.
	Backends *Backends
	Prefs    ChatPrefs

	// MessagesPerMinute limits how fast one chat may give the agent work, a burst of that many
	// included; 0 is no limit. Commands such as /stop are never limited.
	MessagesPerMinute int

	lim     limiter
	root    context.Context
	workers sync.WaitGroup
	mu      sync.Mutex
	chats   map[int64]*chat
	pending map[string]*pendingApproval
	nextID  int
}

type chat struct {
	id    int64
	queue chan string

	mu      sync.Mutex
	agent   *agent.Agent // nil: build a fresh one on the next message
	cancel  context.CancelFunc
	runCtx  context.Context
	profile string     // model profile chosen with /model; "" is the default
	live    *liveReply // the reply being streamed, while a task runs

	envName      string             // environment chosen with /env; "" is the default
	applied      string             // the environment this chat's agent is using now
	turns        int                // tasks the current agent has run
	switching    chan struct{}      // closed when the /env switch in progress finishes; nil if none
	switchCancel context.CancelFunc // stops waiting for that switch
	switchGen    int                // which /env request is the latest
}

// Command is a command the engine does not know itself.
type Command struct {
	Help string // one line for /help, such as "shows what runs in the background"
	Run  func(chatID int64, args []string) string
}

// HasCommand reports whether name (without the prefix) is one of the engine's
// own commands or one added with Commands.
func (e *Engine) HasCommand(name string) bool {
	switch name {
	case "start", "help", "whoami", "new", "stop", "model", "env", "log", "permissions":
		return true
	}
	_, ok := e.Commands[name]
	return ok
}

type pendingApproval struct {
	chatID int64
	tool   string
	ch     chan Choice
}

// Permissions is what the engine needs to let users keep a decision, see what
// they have decided, and see what the agent has done.
type Permissions interface {
	// Rememberable reports whether "always allow" is offered for a tool.
	Rememberable(tool string) bool
	// Remember keeps a chat's choice to always allow a tool.
	Remember(chat int64, tool string) error
	// Describe says what the rules are and what this chat has always allowed.
	Describe(chat int64) string
	// Reset forgets the chat's choices and says how many there were.
	Reset(chat int64) (int, error)
	// Activity renders the chat's last n tool calls.
	Activity(chat int64, n int) string
}

func (e *Engine) logf(format string, args ...any) {
	if e.Logf != nil {
		e.Logf(format, args...)
	}
}

// Start prepares the engine to serve. Everything the engine does ends when ctx does.
func (e *Engine) Start(ctx context.Context) {
	e.mu.Lock() // other goroutines (a job asking for a chat's Selection) may already be calling in
	e.root = ctx
	e.chats = map[int64]*chat{}
	e.pending = map[string]*pendingApproval{}
	e.mu.Unlock()
	if e.ApprovalTimeout == 0 {
		e.ApprovalTimeout = 5 * time.Minute
	}
}

// Wait blocks, for at most timeout, until every chat's worker has finished its
// task. Call it after the context given to Start has ended, so that a task cut
// short has recorded how it ended before the process exits.
func (e *Engine) Wait(timeout time.Duration) {
	done := make(chan struct{})
	go func() { e.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// Incoming handles one message from a user in their private chat. It must not
// block: agent work happens on per-chat workers.
func (e *Engine) Incoming(chatID, userID int64, text string) {
	if text == "" {
		return
	}
	cmd := command(text)
	args := strings.Fields(text)
	if len(args) > 0 {
		args = args[1:]
	}

	if !e.Allowed[userID] {
		// Anyone on the internet can write to a bot. Answering each takes a request to the chat
		// service, and answering a flood of them gets the bot rate limited, or banned.
		if (cmd == "/start" || cmd == "/whoami") && e.lim.allowStrangerReply(userID) {
			e.send(chatID, fmt.Sprintf("Not authorized. Your %s user ID is %d. "+
				"The owner can add it to %s.", e.T.Service(), userID, allowlistVar(e.T.Service())))
		}
		return
	}

	c := e.chatFor(chatID)
	switch cmd {
	case "/start", "/help":
		help := "Talaria is ready. Send a message to chat.\n" + e.cmd("new") + " starts a fresh conversation (reloads notes and skills).\n" + e.cmd("stop") + " cancels the current task."
		if e.Backends != nil && len(e.Backends.Names) > 0 {
			help += "\n" + e.cmd("model") + " shows or changes the model for this chat."
		}
		if e.Permissions != nil {
			help += "\n" + e.cmd("log") + " shows what the agent has run here, and " + e.cmd("permissions") + " what it may run without asking."
		}
		names := make([]string, 0, len(e.Commands))
		for name := range e.Commands {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			help += "\n" + e.cmd(name) + " " + e.Commands[name].Help + "."
		}
		switch {
		case e.canSwitch():
			help += "\n" + e.cmd("env") + " shows or changes where this chat's shell and file tools run."
		case e.Environment != "":
			help += "\nShell and file tools run in " + e.Environment + "."
		}
		e.send(c.id, help)
	case "/log":
		if e.Permissions == nil {
			e.send(c.id, "The activity log is not set up.")
			break
		}
		e.send(c.id, e.Permissions.Activity(c.id, 10))
	case "/permissions":
		e.handlePermissions(c, args)
	case "/model":
		e.handleModel(c, args)
	case "/env":
		e.handleEnv(c, args)
	case "/whoami":
		e.send(c.id, fmt.Sprintf("Your %s user ID is %d.", e.T.Service(), userID))
	case "/new":
		c.stop()
		c.mu.Lock()
		c.agent = nil
		c.mu.Unlock()
		e.send(c.id, "Started a new conversation.")
	case "/stop":
		switch ranTask, waiting := c.stop(), c.cancelSwitch(); {
		case ranTask:
			e.send(c.id, "Stopped.")
		case waiting: // stopping a switch reports for itself
		case e.StopBackground != nil && e.StopBackground(c.id) > 0:
			e.send(c.id, "Stopped the background task(s) running for this chat.")
		default:
			e.send(c.id, "Nothing is running.")
		}
	default:
		if c, ok := e.Commands[strings.TrimPrefix(cmd, "/")]; ok && strings.HasPrefix(cmd, "/") {
			e.send(chatID, c.Run(chatID, args))
			break
		}
		if ok, warn := e.lim.allowMessage(chatID, e.MessagesPerMinute); !ok {
			metrics.Inc("talaria_messages_total", "gateway", strings.ToLower(e.T.Service()), "result", "rate_limited")
			if warn {
				e.send(c.id, fmt.Sprintf("That is more than %d messages a minute, so I am ignoring some. Slow down and I will carry on.", e.MessagesPerMinute))
			}
			break
		}
		metrics.Inc("talaria_messages_total", "gateway", strings.ToLower(e.T.Service()), "result", "accepted")
		if e.OnUserMessage != nil {
			e.OnUserMessage(chatID)
		}
		select {
		case c.queue <- text:
		default:
			e.send(c.id, "Too many messages queued. Wait for me to catch up.")
		}
	}
}

func (e *Engine) handlePermissions(c *chat, args []string) {
	switch {
	case e.Permissions == nil:
		e.send(c.id, "Permissions are not set up.")
	case len(args) == 1 && strings.EqualFold(args[0], "reset"):
		n, err := e.Permissions.Reset(c.id)
		switch {
		case err != nil:
			e.send(c.id, "Could not forget them: "+err.Error())
		case n == 0:
			e.send(c.id, "This chat had not always allowed anything.")
		default:
			e.send(c.id, fmt.Sprintf("Forgot %d always-allowed tool(s). The agent will ask again.", n))
		}
	default:
		e.send(c.id, e.Permissions.Describe(c.id)+"\n\n"+e.cmd("permissions")+" reset makes it ask again for everything.")
	}
}

// cmd writes a command the way users type it on this service.
func (e *Engine) cmd(name string) string { return e.T.CommandPrefix() + name }

// command returns the lowercase slash command of text ("" if none), without any @botname suffix.
func command(text string) string {
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	first := strings.Fields(text)[0]
	first, _, _ = strings.Cut(first, "@")
	return strings.ToLower(first)
}

func (e *Engine) chatFor(id int64) *chat {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.chats[id]
	if !ok {
		c = &chat{id: id, queue: make(chan string, queueDepth)}
		if e.Prefs != nil {
			c.profile = e.Prefs.Profile(id)
			c.envName = e.Prefs.Environment(id)
		}
		e.chats[id] = c
		e.workers.Add(1)
		go func() {
			defer e.workers.Done()
			e.worker(c)
		}()
	}
	return c
}

// stop cancels the running task, reporting whether there was one.
func (c *chat) stop() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel == nil {
		return false
	}
	c.cancel()
	return true
}

func (e *Engine) worker(c *chat) {
	for {
		select {
		case <-e.root.Done():
			return
		case text := <-c.queue:
			e.runOne(c, text)
		}
	}
}

func (e *Engine) editInterval() time.Duration {
	if e.EditInterval > 0 {
		return e.EditInterval
	}
	return e.T.EditInterval()
}

func (e *Engine) runOne(c *chat, text string) {
	if rw, ok := e.T.(RunWatcher); ok {
		rw.RunStart(c.id)
		defer rw.RunEnd(c.id) // the first defer, so the last to run: after the reply is delivered
	}
	ctx, cancel := context.WithCancel(e.root)
	c.mu.Lock()
	c.cancel, c.runCtx = cancel, ctx
	if c.agent == nil {
		c.agent = e.NewAgent(c.id, func(name, args string) bool { return e.askApproval(c, name, args) })
		c.applied, c.turns = e.defaultEnv(), 0 // a new agent starts in the default environment
	}
	a := c.agent
	c.mu.Unlock()
	e.applyBackend(c, a)

	defer func() {
		a.OnText, a.OnToolStart, a.OnToolCall = nil, nil, nil
		cancel()
		c.mu.Lock()
		c.cancel, c.live = nil, nil
		c.mu.Unlock()
	}()

	// Typing shows from here, since starting an environment can take a while.
	stopTyping := sync.OnceFunc(e.typing(ctx, c.id))
	defer stopTyping()

	proceed, notice := e.applyEnvironment(ctx, c, a)
	if !proceed {
		return
	}
	if notice != "" {
		text = notice + "\n\n" + text
	}

	var live *liveReply
	if e.Stream {
		live = newLiveReply(e.root, e.T, c.id, e.editInterval(), e.logf)
		a.OnText = live.onText
		a.OnToolStart = func(string, string) { live.endSegment() } // text before a tool call is complete, so finish it before the tool runs
		c.mu.Lock()
		c.live = live
		c.mu.Unlock()
	}

	if tw, ok := e.T.(ToolWatcher); ok {
		before := a.OnToolStart
		a.OnToolStart = func(name, args string) {
			if before != nil {
				before(name, args)
			}
			tw.ToolStart(c.id, name)
		}
		a.OnToolCall = func(name, _, result string) { tw.ToolEnd(c.id, name, result) }
	}

	reply, err := a.Run(ctx, text)
	stopTyping()
	c.mu.Lock()
	c.turns++
	c.mu.Unlock()

	shown := false
	if live != nil {
		shown = live.finish(reply) // also tidies a reply that /stop or an error cut short
	}
	switch {
	case ctx.Err() != nil:
		return // /stop or shutdown; the user was already told
	case err != nil:
		e.send(c.id, "Error: "+err.Error())
	case !shown:
		e.send(c.id, reply)
	}
}

func (e *Engine) typing(ctx context.Context, chatID int64) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			e.T.Typing(ctx, chatID)
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(done) }
}

// askApproval posts Allow/Deny buttons and blocks until the user answers.
// It denies on timeout, /stop, or any send failure.
func (e *Engine) askApproval(c *chat, name, args string) bool {
	c.mu.Lock()
	ctx, live, env := c.runCtx, c.live, c.applied
	c.mu.Unlock()
	if live != nil {
		live.endSegment() // the text so far goes first; the prompt follows it
	}

	e.mu.Lock()
	token, ok := newToken(e.pending)
	if !ok {
		e.mu.Unlock()
		e.logf("approval for %s denied: no randomness for its token", name)
		return false
	}
	p := &pendingApproval{chatID: c.id, tool: name, ch: make(chan Choice, 1)}
	e.pending[token] = p
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.pending, token)
		e.mu.Unlock()
	}()

	tool := agent.BaseTool(name)
	// Where a call runs matters when deciding whether to allow it, and with
	// per-chat environments the startup banner no longer says. Only the tools
	// that go to the environment say so; the rest run where Talaria runs.
	where := ""
	if e.Environments != nil && env != "" && env != execenv.LocalName {
		switch tool {
		case "shell", "read_file", "write_file", "list_dir":
			where = " in " + env
		}
	}
	body, ok := e.approvalBody(c.id, args)
	if !ok {
		return false
	}
	text := fmt.Sprintf("Allow %s%s?\n%s", name, where, body)
	always := e.Permissions != nil && e.Permissions.Rememberable(tool) && !agent.IsFlagged(name)
	msg, err := e.T.SendApproval(ctx, c.id, text, token, always)
	if err != nil {
		e.logf("approval prompt failed: %v", err)
		return false
	}

	timer := time.NewTimer(e.ApprovalTimeout)
	defer timer.Stop()
	select {
	case choice := <-p.ch:
		if choice == ChoiceAlways {
			if err := e.Permissions.Remember(c.id, tool); err != nil {
				e.logf("keeping an always-allow choice failed: %v", err)
				e.send(c.id, "I could not save that choice ("+err.Error()+"), so I will ask again next time.")
			}
		}
		return choice != ChoiceDeny
	case <-ctx.Done():
		e.T.Edit(e.root, c.id, msg, text+"\n\nCancelled.")
	case <-timer.C:
		e.T.Edit(e.root, c.id, msg, text+"\n\nTimed out, denied.")
	}
	return false
}

// Decision is what became of a button press.
type Decision int

const (
	DecisionExpired   Decision = iota // no such request any more
	DecisionForbidden                 // not an allowed user, or not that request's chat
	DecisionRecorded                  // the answer reached the agent
	DecisionAnswered                  // the request was already answered; this press changed nothing
)

// Decide records the answer to an approval prompt. The caller then shows the
// user the outcome: MsgExpired, MsgNotAllowed, MsgAnswered, or Verdict of the
// returned choice (which differs from the one given when that button was never
// offered) with the prompt edited to match.
func (e *Engine) Decide(token string, user, chatID int64, choice Choice) (Decision, Choice) {
	e.mu.Lock()
	p := e.pending[token]
	e.mu.Unlock()
	switch {
	case p == nil:
		return DecisionExpired, choice
	case !e.Allowed[user] || chatID != p.chatID:
		return DecisionForbidden, choice
	}
	if choice == ChoiceAlways && (e.Permissions == nil || agent.IsFlagged(p.tool) || !e.Permissions.Rememberable(agent.BaseTool(p.tool))) {
		choice = ChoiceAllow // no such button was offered, so no such answer is valid
	}
	select {
	case p.ch <- choice:
		return DecisionRecorded, choice
	default: // already answered: say so, rather than a verdict that contradicts what ran
		return DecisionAnswered, choice
	}
}

// tokenRand is where approval tokens come from: crypto/rand.Reader, replaced
// in tests to simulate its failure.
var tokenRand io.Reader = rand.Reader

// newToken makes the name of an approval request: unguessable, so that a button
// left on screen from before a restart, or one made up, cannot answer a new
// request. Without randomness there is no safe token, so it reports failure
// and the caller denies the approval instead of crashing the process.
func newToken(taken map[string]*pendingApproval) (string, bool) {
	for {
		var b [8]byte
		if _, err := io.ReadFull(tokenRand, b[:]); err != nil {
			return "", false
		}
		t := hex.EncodeToString(b[:])
		if _, dup := taken[t]; !dup {
			return t, true
		}
	}
}

// approvalBody is what the question shows of a call's arguments. All of it must
// be readable before the answer: a prompt that quietly cut off the end could hide
// what the user is agreeing to. What fits is shown with the question; a longer
// request is sent in full just before it; one too long to read at all is refused.
func (e *Engine) approvalBody(chatID int64, args string) (string, bool) {
	full := agent.ApprovalText(args)
	runes := []rune(full)
	if len(runes) > approvalArgsHard {
		e.send(chatID, fmt.Sprintf("I was about to ask whether to allow something with %d characters of arguments, which is more than anyone can sensibly check here, so I did not run it. Ask for something smaller, or do it from the terminal.", len(runes)))
		return "", false
	}
	fence := func(s string) string {
		if e.T.Markdown() { // show it as written: a command is full of characters that mean something to Markdown
			return "```\n" + strings.ReplaceAll(s, "```", "`\u200b``") + "\n```"
		}
		return s
	}
	if len(runes) <= approvalArgsMax {
		return fence(full), true
	}
	e.send(chatID, "The whole of what you are about to be asked to allow:\n"+fence(full))
	return fence(string(runes[:approvalArgsMax]) + "\n… (that was the start: the whole request is in the message above)"), true
}

func (e *Engine) send(chatID int64, text string) { e.Send(e.root, chatID, text) }

// Send delivers text to a chat, split to fit the service's limit. It is safe
// to call from other goroutines.
func (e *Engine) Send(ctx context.Context, chatID int64, text string) {
	Deliver(ctx, e.T, chatID, text, e.Logf)
}

// Selection reports the model profile and the environment a chat has chosen
// with /model and /env, as names; "" means the default. A job scheduled from
// the chat starts out with these.
func (e *Engine) Selection(chatID int64) (profile, environment string) {
	e.mu.Lock()
	c := e.chats[chatID]
	e.mu.Unlock()
	if c == nil {
		return "", ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.profile, c.envName
}

// cutAt picks where to break r so that the first part has at most limit runes,
// preferring a newline in the second half.
func cutAt(r []rune, limit int) int {
	for i := limit - 1; i > limit/2; i-- {
		if r[i] == '\n' {
			return i
		}
	}
	return limit
}

// fenceHeadroom is room kept free in each piece of a split reply, so a code
// block cut by the split can be closed at the end of one piece and reopened at
// the start of the next.
const fenceHeadroom = 24

// SplitMessage breaks text into pieces of at most limit characters, preferring
// to cut at a newline. With markdown, a code block that a cut falls inside is
// closed and reopened so every piece renders properly.
func SplitMessage(text string, limit int, markdown bool) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{"(no output)"}
	}
	r := []rune(text)
	if len(r) <= limit {
		return []string{text}
	}
	if markdown {
		limit -= fenceHeadroom
	}
	var parts []string
	for len(r) > limit {
		cut := cutAt(r, limit)
		parts = append(parts, strings.TrimSpace(string(r[:cut])))
		r = []rune(strings.TrimSpace(string(r[cut:])))
	}
	parts = append(parts, string(r))
	if markdown {
		parts = balanceFences(parts)
	}
	return parts
}

// fenceState reads text from a given state and reports whether a code block is
// open at its end, and the language tag it was opened with.
func fenceState(text string, open bool, lang string) (bool, string) {
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimLeft(line, " ")
		if !strings.HasPrefix(t, "```") {
			continue
		}
		if open {
			open, lang = false, ""
		} else {
			open, lang = true, strings.TrimSpace(strings.TrimLeft(t, "`"))
		}
	}
	return open, lang
}

// balanceFences closes a code block at the end of any piece that leaves one open,
// and reopens it, with the same language, at the start of the next.
func balanceFences(parts []string) []string {
	out := make([]string, len(parts))
	open, lang := false, ""
	for i, p := range parts {
		if open {
			p = "```" + lang + "\n" + p
		}
		open, lang = fenceState(p, false, "")
		if open {
			out[i] = p + "\n```"
		} else {
			out[i] = p
		}
	}
	return out
}
