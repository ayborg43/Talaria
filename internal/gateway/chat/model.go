package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"talaria/internal/agent"
	"talaria/internal/provider"
	"talaria/internal/routing"
)

// Backend is a model a chat can talk to.
type Backend struct {
	Provider provider.Provider
	Model    string
	Desc     string // for display, e.g. "anthropic, claude-sonnet-5-5"
	// Vision and Reasoning say what the model can do, so the router can pick
	// it: images, and thinking with a per-request effort. Best holds the
	// strengths the profile claims ("code", "cheap").
	Vision    bool
	Reasoning bool
	Best      []string
}

// Backends lets each chat pick among the configured model profiles with /model.
type Backends struct {
	Default string   // the profile a chat uses until it picks another
	Names   []string // every profile a chat may pick, sorted; includes Default
	// Open returns the backend for a profile. An error says why it cannot be
	// used right now (a missing API key, say); the gateway shows it to the user.
	Open func(name string) (Backend, error)
	// OpenRaw is Open without the fallback chain and without the cache: the profile's own model and nothing else, which is
	// what a test of that model must ask. It may be nil.
	OpenRaw func(name string) (Backend, error)
	// OpenEffort is Open with a reasoning effort ("low" or "high") for models
	// that think; other models ignore it. It may be nil, and then effort is
	// whatever the profile says. Backends built without it route without effort.
	OpenEffort func(name, effort string) (Backend, error)
	// Forget drops cached providers, so the next Open rebuilds them from the
	// current config. Call it after profiles are added, changed or removed.
	// It may be nil, when there is nothing cached.
	Forget func()
	// Resting, if set, says a profile failed a moment ago and should not be chosen by the router (a pinned choice and the
	// fallback chain are unaffected).
	Resting func(name string) bool
	// List returns every profile a chat may pick, sorted; includes Default.
	// When nil, Names is the list, frozen from startup.
	List func() []string
}

// ModelNoter is an optional extra for a Transport that shows which model
// answered: told after each task, so the page can name it.
type ModelNoter interface {
	NoteModel(chat int64, model string)
}

// ChatPrefs remembers each chat's choices across restarts.
type ChatPrefs interface {
	Profile(chatID int64) string // the model profile; "" means the default
	SetProfile(chatID int64, profile string) error
	Environment(chatID int64) string // where the tools run; "" means the default
	SetEnvironment(chatID int64, name string) error
}

// FilePrefs keeps chat preferences in a small JSON file. It rereads the file on
// each call, so it never holds stale state.
type FilePrefs struct {
	Path string
	mu   sync.Mutex
}

type chatPref struct {
	Profile     string `json:"profile,omitempty"`
	Environment string `json:"environment,omitempty"`
}

type prefsFile struct {
	Chats map[string]chatPref `json:"chats"`
}

func (p *FilePrefs) load() (prefsFile, error) {
	var f prefsFile
	raw, err := os.ReadFile(p.Path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return prefsFile{}, fmt.Errorf("%s is corrupt (fix or delete it): %w", p.Path, err)
	}
	return f, nil
}

func (p *FilePrefs) get(chatID int64) chatPref {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, _ := p.load()
	return f.Chats[strconv.FormatInt(chatID, 10)]
}

func (p *FilePrefs) Profile(chatID int64) string     { return p.get(chatID).Profile }
func (p *FilePrefs) Environment(chatID int64) string { return p.get(chatID).Environment }

func (p *FilePrefs) SetProfile(chatID int64, profile string) error {
	return p.update(chatID, func(c *chatPref) { c.Profile = profile })
}

func (p *FilePrefs) SetEnvironment(chatID int64, name string) error {
	return p.update(chatID, func(c *chatPref) { c.Environment = name })
}

// update changes one chat's entry, leaving its other choices alone, and drops
// the entry once nothing is chosen.
func (p *FilePrefs) update(chatID int64, change func(*chatPref)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, err := p.load()
	if err != nil {
		return err
	}
	key := strconv.FormatInt(chatID, 10)
	entry := f.Chats[key]
	change(&entry)
	if entry == (chatPref{}) {
		delete(f.Chats, key)
	} else {
		if f.Chats == nil {
			f.Chats = map[string]chatPref{}
		}
		f.Chats[key] = entry
	}

	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p.Path), "chats-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p.Path)
}

// selected is the chat's chosen profile, or the default's name.
func (g *Engine) selected(c *chat) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.profile != "" {
		return c.profile
	}
	return g.Backends.Default
}

// ActiveProfile is the model profile a chat is using: its choice, or the
// default. It is "" when model switching is not set up.
func (e *Engine) ActiveProfile(chatID int64) string {
	if e.Backends == nil {
		return ""
	}
	return e.selected(e.chatFor(chatID))
}

// SetProfile switches a chat to a profile and saves the choice, taking effect
// on the next message. Unlike /model it says nothing in the chat: API callers
// report the result themselves. The profile is validated by opening it, so one
// added since startup works at once.
func (e *Engine) SetProfile(chatID int64, name string) error {
	b := e.Backends
	if b == nil {
		return fmt.Errorf("model switching is not set up: only one model is configured")
	}
	if name == "default" || name == "reset" {
		name = b.Default
	}
	if _, err := b.Open(name); err != nil {
		return fmt.Errorf("cannot switch to %s: %v", name, err)
	}
	stored := name // the default is stored as "" so the chat follows it if the config changes
	if name == b.Default {
		stored = ""
	}
	c := e.chatFor(chatID)
	c.mu.Lock()
	c.profile = stored
	c.mu.Unlock()
	if e.Prefs != nil {
		if err := e.Prefs.SetProfile(chatID, stored); err != nil {
			return err
		}
	}
	if be, err := b.Open(name); err == nil {
		if mn, ok := e.T.(ModelNoter); ok {
			mn.NoteModel(chatID, be.Model)
		}
	}
	return nil
}

// describeModels lists the profiles a chat can pick, marking its current one.
func (g *Engine) describeModels(c *chat) string {
	b := g.Backends
	current := g.selected(c)
	var out strings.Builder
	fmt.Fprintf(&out, "This chat uses: %s\n\nProfiles:\n", current)
	for _, name := range b.Names {
		line := "  " + name
		if be, err := b.Open(name); err != nil {
			line += " (unavailable: " + err.Error() + ")"
		} else {
			line += " (" + be.Desc + ")"
		}
		if name == current {
			line += "  <- this chat"
		}
		if name == b.Default {
			line += "  [default]"
		}
		out.WriteString(line + "\n")
	}
	fmt.Fprintf(&out, "\nSwitch with %s <profile>. %s default returns to %s.", g.cmd("model"), g.cmd("model"), b.Default)
	return out.String()
}

// handleModel serves /model: show the profiles, or switch this chat to one.
func (g *Engine) handleModel(c *chat, args []string) {
	b := g.Backends
	if b == nil || len(b.Names) == 0 {
		g.send(c.id, "Model switching is not set up: only one model is configured.")
		return
	}
	if len(args) == 0 {
		g.send(c.id, g.describeModels(c))
		return
	}

	name := args[0]
	if name == "default" || name == "reset" {
		name = b.Default
	}
	if !slices.Contains(b.Names, name) {
		g.send(c.id, fmt.Sprintf("There is no profile named %q.\n\n%s", args[0], g.describeModels(c)))
		return
	}
	be, err := b.Open(name)
	if err != nil {
		g.send(c.id, fmt.Sprintf("Cannot switch to %s: %v", name, err))
		return
	}

	stored := name // the default is stored as "" so the chat follows it if the config changes
	if name == b.Default {
		stored = ""
	}
	c.mu.Lock()
	c.profile = stored
	running := c.cancel != nil
	c.mu.Unlock()

	reply := fmt.Sprintf("Switched to %s (%s). The conversation continues with the new model.", name, be.Desc)
	if running {
		reply += " The task in progress finishes on the old one."
	}
	if g.Prefs != nil {
		if err := g.Prefs.SetProfile(c.id, stored); err != nil {
			g.logf("saving the profile choice failed: %v", err)
			reply += "\n(I could not save this choice, so it will not survive a restart.)"
		}
	}
	g.send(c.id, reply)
}

// profiles is every profile a chat may pick: live when the backends know how
// to list them, else the names frozen from startup.
func (b *Backends) profiles() []string {
	if b.List != nil {
		return b.List()
	}
	return b.Names
}

// Routing signals, read from the message: code fences and programming words,
// and words that ask for hard thinking.
var (
	codeSignal = regexp.MustCompile("(?i)```|\\b(func|function|class|struct|import|error|traceback|debug|refactor|compile|regex|recursion|pointer)\\b")
	hardSignal = regexp.MustCompile("(?i)\\b(prove|proof|theorem|derive|derivation|complexity|architect|trade-?off|research deeply)\\b")
)

// effortFor tunes a thinking model: full effort for code and hard questions,
// light effort for the plain rest.
func effortFor(text string) string {
	if codeSignal.MatchString(text) || hardSignal.MatchString(text) {
		return "high"
	}
	return "low"
}

// RouterSettings are the choices behind smart routing, read at each message so a change in Settings applies at once.
type RouterSettings struct {
	// Smart asks a small model to say what each message is, when the keyword rules are not decisive.
	Smart bool
	// Profile is the model that labels messages; "" means one marked cheap, else the default.
	Profile string
	// Learn lets how replies were received (thumbs, retries, corrections, carrying on) steer the choice.
	Learn bool
}

// RouterBox holds the router's settings for the engine to read and Settings to change while Talaria runs.
type RouterBox struct {
	v atomic.Pointer[RouterSettings]
}

// Get is the current settings; the zero value (everything off) before any are set.
func (b *RouterBox) Get() RouterSettings {
	if p := b.v.Load(); p != nil {
		return *p
	}
	return RouterSettings{}
}

// Set replaces the settings.
func (b *RouterBox) Set(s RouterSettings) { b.v.Store(&s) }

// RouteNoter is an optional extra for a Transport that wants to know how a model was chosen for a reply, so the page
// can name the model that answered and say why. Told before each task, after the model is chosen.
type RouteNoter interface {
	NoteRoute(chat int64, profile, model, kind, why string)
}

// decision is what the router chose for one message.
type decision struct {
	name   string // the profile; "" for the default
	effort string
	kind   routing.Kind
	why    string
}

// longConversation is how much text a conversation may hold before the router looks for a model that takes long input.
const longConversation = 60000

// usableBackends is every profile that can be opened and is not resting after a failure. If every one is resting they
// are all returned: the chain decides then.
func (e *Engine) usableBackends() (names []string, be map[string]Backend) {
	b := e.Backends
	be = map[string]Backend{}
	var resting []string
	for _, n := range b.profiles() {
		o, err := b.Open(n)
		if err != nil {
			continue
		}
		be[n] = o
		if b.Resting != nil && b.Resting(n) {
			resting = append(resting, n)
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		names = resting
	}
	return names, be
}

func (e *Engine) routerSettings() RouterSettings {
	if e.RouterSettings == nil {
		return RouterSettings{}
	}
	return e.RouterSettings()
}

// kindOf says what the message is, by the cheapest means that is sure: a code fence, a very long conversation, a small
// model's label when smart routing is on, and the keyword rules otherwise.
func (e *Engine) kindOf(c *chat, text string, usable []string, be map[string]Backend) (routing.Kind, string) {
	rule := routing.KindOf(text)
	if c != nil && e.aboutPicture(c, text) {
		return routing.Vision, "about a picture"
	}
	if strings.Contains(text, "```") {
		return routing.Code, "has code in it"
	}
	if c != nil && e.historySize(c) > longConversation {
		return routing.Long, "a very long conversation"
	}
	if s := e.routerSettings(); s.Smart && len(usable) >= 2 {
		if k, ok := e.classify(text, s.Profile, usable, be); ok {
			return k, "judged as " + string(k)
		}
	}
	switch rule {
	case routing.Code:
		return rule, "code words in the message"
	case routing.Hard:
		return rule, "asks for hard thinking"
	case routing.Simple:
		return rule, "a short, plain message"
	}
	return rule, "an ordinary message"
}

// aboutPicture says whether this message is about an image: it names one, or an image was sent a moment ago and this
// message follows it (the model sees the picture through a tool, so the next message is the one that must reach a model that can).
func (e *Engine) aboutPicture(c *chat, text string) bool {
	if routing.MentionsPicture(text) {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var tr []provider.Message
	if c.agent != nil {
		tr = c.agent.Transcript()
	} else {
		tr = c.pending
	}
	for i, n := len(tr)-1, 0; i >= 0 && n < 6; i, n = i-1, n+1 { // the last few turns
		if tr[i].Role == provider.RoleUser && routing.MentionsPicture(tr[i].Content) || len(tr[i].Images) > 0 {
			return true
		}
	}
	return false
}

func (e *Engine) historySize(c *chat) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var tr []provider.Message
	if c.agent != nil {
		tr = c.agent.Transcript()
	} else {
		tr = c.pending
	}
	n := 0
	for _, m := range tr {
		n += len(m.Content)
	}
	return n
}

const classifierPrompt = "Label the user's message with exactly one word. " +
	"code: programming, scripts, errors, technical debugging. " +
	"hard: proofs, careful multi-step reasoning, designing something complicated. " +
	"simple: a short plain question or small talk. " +
	"writing: drafting, rewording or summarising text. " +
	"general: anything else. Answer with the single word and nothing more."

// classify asks a small model what a message is. It waits only a moment: a slow or failed answer just means the keyword
// rules decide, so smart routing can make a reply no slower than a couple of seconds, and never fail one.
func (e *Engine) classify(text, profile string, usable []string, be map[string]Backend) (routing.Kind, bool) {
	key := strings.ToLower(strings.TrimSpace(text))
	e.classMu.Lock()
	if k, ok := e.classCache[key]; ok {
		e.classMu.Unlock()
		return k, true
	}
	e.classMu.Unlock()
	use := profile
	if _, ok := be[use]; !ok {
		use = ""
		for _, n := range usable {
			if slices.Contains(be[n].Best, "cheap") {
				use = n
				break
			}
		}
		if use == "" {
			use = e.Backends.Default
		}
	}
	b, ok := be[use]
	if !ok {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	t := []rune(text)
	if len(t) > 600 {
		t = t[:600]
	}
	msg, err := b.Provider.Chat(ctx, provider.Request{Model: b.Model, Messages: []provider.Message{
		{Role: provider.RoleSystem, Content: classifierPrompt},
		{Role: provider.RoleUser, Content: string(t)},
	}})
	if err != nil {
		e.logf("smart routing: the labelling model did not answer (%v): using the keyword rules", err)
		return "", false
	}
	k, ok := routing.Parse(msg.Content)
	if !ok {
		return "", false
	}
	e.classMu.Lock()
	if e.classCache == nil || len(e.classCache) > 200 {
		e.classCache = map[string]routing.Kind{}
	}
	e.classCache[key] = k
	e.classMu.Unlock()
	return k, true
}

// decide picks the profile and reasoning effort for one message, and says why. An explicit choice always wins:
// routing then only tunes the effort. It returns an empty name when the default path should be used unchanged: under
// two usable backends, or nothing the message asks for.
func (e *Engine) decide(c *chat, text, pinned string) decision {
	b := e.Backends
	if b == nil || b.Open == nil {
		return decision{}
	}
	usable, be := e.usableBackends()
	if len(usable) < 2 {
		return decision{}
	}
	if pinned != "" {
		d := decision{why: "your choice"}
		if o, ok := be[pinned]; ok && o.Reasoning {
			d.effort = effortFor(text)
		}
		return d
	}
	kind, how := e.kindOf(c, text, usable, be)
	claims := func(n string, want string) bool { return slices.Contains(be[n].Best, want) }
	first := func(want func(string) bool) string {
		for _, n := range usable {
			if want(n) {
				return n
			}
		}
		return ""
	}
	var name, rule string
	switch kind {
	case routing.Vision:
		// What matters is that the model can see. The default is kept when it can; only a default that cannot is passed over.
		if o, ok := be[b.Default]; ok && o.Vision {
			name = ""
		} else {
			name = first(func(n string) bool { return be[n].Vision })
		}
		rule = "a model that sees pictures"
	case routing.Code:
		name = first(func(n string) bool { return claims(n, "code") })
		if name == "" {
			name = first(func(n string) bool { return be[n].Reasoning })
		}
		rule = "a code model"
	case routing.Hard:
		name = first(func(n string) bool { return be[n].Reasoning })
		rule = "a model that reasons"
	case routing.Simple:
		name = first(func(n string) bool { return claims(n, "cheap") })
		rule = "the cheap model"
	case routing.Writing:
		name = first(func(n string) bool { return claims(n, "writing") })
		rule = "a writing model"
	case routing.Long:
		name = first(func(n string) bool { return claims(n, "long") })
		rule = "a long-context model"
	}
	why := how
	if kind == routing.Vision && name == "" {
		why = how + ": the default model sees pictures"
	} else if name != "" {
		why = how + ": " + rule
	} else {
		why = how + ": the default model"
	}
	usual := name
	if usual == "" {
		usual = b.Default
	}
	if kind == routing.Vision && name == "" && be[b.Default].Vision == false {
		// no model can see: nothing to do but say so, the default carries on
		why = how + ": no model here is marked as seeing pictures"
	}
	// What has been learned about how replies were received can overrule the rule, when the record is clear.
	if s := e.routerSettings(); s.Learn && e.Learn != nil {
		cands := []string{b.Default}
		for _, n := range usable {
			if n != b.Default && (claims(n, string(kind)) || kind == routing.General || kind == routing.Simple || (kind == routing.Vision && be[n].Vision)) {
				cands = append(cands, n)
			}
		}
		cands = slices.DeleteFunc(cands, func(n string) bool { _, ok := be[n]; return !ok })
		if pick, because := e.Learn.Prefer(kind, cands, usual); because != "" {
			name, why = pick, because
		} else if kind == routing.Simple || kind == routing.General {
			// a little exploring on low-stakes messages, so a good model is not passed over for never having been tried
			if try := e.Learn.Explore(kind, cands, usual, 0.06); try != "" {
				name, why = try, "trying "+try+" to learn how it does on "+string(kind)+" messages"
			}
		}
	}
	d := decision{name: name, kind: kind, why: why}
	use := name
	if use == "" {
		use = b.Default
	}
	if o, ok := be[use]; ok && o.Reasoning {
		d.effort = effortFor(text)
	}
	if d.name == b.Default {
		d.name = ""
	}
	return d
}

// route is decide without the explanation, for callers that only need the choice.
func (e *Engine) route(text, pinned string) (name, effort string) {
	d := e.decide(nil, text, pinned)
	return d.name, d.effort
}

// Escalate says which profile to try when a reply from "from" was not good enough: a model that reasons, else one
// that claims code, else the default if that is not what answered, else any other that is working.
func (e *Engine) Escalate(from string) string {
	b := e.Backends
	if b == nil || b.Open == nil {
		return ""
	}
	usable, be := e.usableBackends()
	pick := func(want func(string) bool) string {
		for _, n := range usable {
			if n != from && want(n) {
				return n
			}
		}
		return ""
	}
	for _, want := range []func(string) bool{
		func(n string) bool { return be[n].Reasoning },
		func(n string) bool { return slices.Contains(be[n].Best, "code") },
		func(n string) bool { return n == b.Default },
		func(string) bool { return true },
	} {
		if n := pick(want); n != "" {
			return n
		}
	}
	return ""
}

// Retry runs the person's last message again, on the named profile (or the one Escalate chooses): the answer it
// replaces is dropped from what the agent remembers, so the new one is not shaped by it. The page must already have
// removed the old answer from what it shows.
func (e *Engine) Retry(chatID, userID int64, text, profile string, kind routing.Kind) error {
	if e.Backends == nil {
		return fmt.Errorf("there is only one model to answer with")
	}
	e.prepareRetry(e.chatFor(chatID), profile, kind)
	e.Incoming(chatID, userID, text)
	return nil
}

// prepareRetry forgets the last exchange and pins profile for the next task.
func (e *Engine) prepareRetry(c *chat, profile string, kind routing.Kind) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var tr []provider.Message
	if c.agent != nil {
		tr = c.agent.Transcript()
	} else {
		tr = c.pending
	}
	for i := len(tr) - 1; i >= 0; i-- {
		if tr[i].Role == provider.RoleUser {
			tr = append([]provider.Message(nil), tr[:i]...)
			if c.agent != nil {
				c.agent.Restore(tr)
			} else {
				c.pending = tr
			}
			break
		}
	}
	c.once, c.onceKind = profile, kind
}

// applyBackend points the chat's agent at the model for this task: the chat's
// explicit choice, a one-off choice made by a retry, or the router's pick when it follows the default. It runs
// before each task, so a choice made mid-conversation takes effect on the
// next message and the history carries over. A saved profile that no longer
// works is replaced by the default, with a note.
func (g *Engine) applyBackend(c *chat, a *agent.Agent, text string) {
	b := g.Backends
	if b == nil {
		return
	}
	c.mu.Lock()
	pinned, ofAgent := c.profile, false
	if c.persona != nil && c.persona.Profile != "" {
		pinned, ofAgent = c.persona.Profile, true // a named agent's own model wins over the chat's choice while it is the one talking
	}
	once, onceKind := c.once, c.onceKind
	c.once, c.onceKind = "", ""
	c.mu.Unlock()
	var d decision
	if once != "" {
		d = g.decide(c, text, once)
		d.name, d.why = once, "tried again on a stronger model"
		d.kind = onceKind // what the reply being replaced was taken for
		if d.kind == "" {
			d.kind = routing.KindOf(text)
		}
	} else {
		d = g.decide(c, text, pinned)
	}
	name, effort := d.name, d.effort
	use := pinned
	if use == "" {
		use = b.Default
	}
	if name != "" {
		use = name
	}
	if once != "" {
		use = once
	}
	open := b.Open
	if effort != "" && b.OpenEffort != nil {
		eff := effort
		open = func(n string) (Backend, error) { return b.OpenEffort(n, eff) }
	}
	be, err := open(use)
	if err != nil && use != b.Default {
		g.send(c.id, fmt.Sprintf("The %s profile cannot be used now (%v), so I'm using %s.", use, err, b.Default))
		if !ofAgent && once == "" { // the chat's own choice is forgotten; a named agent's is left for its owner to fix
			c.mu.Lock()
			c.profile = ""
			c.mu.Unlock()
			if g.Prefs != nil {
				g.Prefs.SetProfile(c.id, "")
			}
		}
		use = b.Default
		d.why = "the model asked for could not be used"
		be, err = b.Open(b.Default)
	}
	if err != nil {
		g.logf("no usable model for chat %d: %v", c.id, err)
		return
	}
	a.Provider, a.Model = be.Provider, be.Model
	if mn, ok := g.T.(ModelNoter); ok {
		mn.NoteModel(c.id, be.Model)
	}
	if rn, ok := g.T.(RouteNoter); ok {
		kind := string(d.kind)
		if kind == "" {
			kind = string(routing.KindOf(text))
		}
		rn.NoteRoute(c.id, use, be.Model, kind, d.why)
	}
}
