package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"talaria/internal/agent"
	"talaria/internal/provider"
)

// Backend is a model a chat can talk to.
type Backend struct {
	Provider provider.Provider
	Model    string
	Desc     string // for display, e.g. "anthropic, claude-sonnet-5-5"
}

// Backends lets each chat pick among the configured model profiles with /model.
type Backends struct {
	Default string   // the profile a chat uses until it picks another
	Names   []string // every profile a chat may pick, sorted; includes Default
	// Open returns the backend for a profile. An error says why it cannot be
	// used right now (a missing API key, say); the gateway shows it to the user.
	Open func(name string) (Backend, error)
	// Forget drops cached providers, so the next Open rebuilds them from the
	// current config. Call it after profiles are added, changed or removed.
	// It may be nil, when there is nothing cached.
	Forget func()
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
		return e.Prefs.SetProfile(chatID, stored)
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

// applyBackend points the chat's agent at the model the chat has chosen. It
// runs before each task, so a choice made mid-conversation takes effect on the
// next message and the history carries over. A saved profile that no longer
// works is replaced by the default, with a note.
func (g *Engine) applyBackend(c *chat, a *agent.Agent) {
	b := g.Backends
	if b == nil {
		return
	}
	name := g.selected(c)
	be, err := b.Open(name)
	if err != nil && name != b.Default {
		g.send(c.id, fmt.Sprintf("The %s profile cannot be used now (%v), so I'm using %s.", name, err, b.Default))
		c.mu.Lock()
		c.profile = ""
		c.mu.Unlock()
		if g.Prefs != nil {
			g.Prefs.SetProfile(c.id, "")
		}
		be, err = b.Open(b.Default)
	}
	if err != nil {
		g.logf("no usable model for chat %d: %v", c.id, err)
		return
	}
	a.Provider, a.Model = be.Provider, be.Model
}
