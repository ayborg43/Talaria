// Package choice presents structured questions the user answers by picking
// an option: a clarification (what did you mean, which one) or a comparison
// (up to three sourced candidates). The rules mirror OpenMuse's Jev panels:
// every comparison option carries its sources, IDs are unique, and a new
// presentation on the same thread supersedes the old one, so a late answer
// can never land on a stale question.
package choice

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Panel types.
const (
	Clarification = "clarification"
	Comparison    = "comparison"
)

// Panel states.
const (
	Open       = "open"
	Answered   = "answered"
	Superseded = "superseded"
)

// Limits mirror the reference: comparison cards stay small enough to read.
const (
	MaxOptions     = 12
	MaxCompareOpts = 3
	MaxIDRunes     = 200
	MaxLabelRunes  = 200
	MaxDetails     = 4
	MaxDetailRunes = 600
	MaxSources     = 5
	MaxTitleRunes  = 200
	MaxSourceRunes = 200
)

// Source is one piece of evidence behind an option.
type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Option is one thing the user can pick.
type Option struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Details []string `json:"details,omitempty"`
	Sources []Source `json:"sources,omitempty"`
}

// Panel is one presented question.
type Panel struct {
	ID        string   `json:"id"`
	Gateway   string   `json:"gateway"`
	ChatID    int64    `json:"chat_id"`
	Thread    string   `json:"thread"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Options   []Option `json:"options"`
	Preferred string   `json:"preferred,omitempty"`
	Selected  string   `json:"selected,omitempty"`
	Status    string   `json:"status"`
	Version   int      `json:"version"`

	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// Store keeps panels in one JSON file, reread on every call.
type Store struct {
	Path string
	Now  func() time.Time

	mu sync.Mutex
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

func validSource(src Source) error {
	if title := squash(src.Title); title == "" || len([]rune(title)) > MaxSourceRunes {
		return errors.New("each source needs a short title")
	}
	u, err := url.Parse(strings.TrimSpace(src.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("each source URL must use HTTP or HTTPS")
	}
	return nil
}

func validOptions(typ string, options []Option) error {
	switch {
	case len(options) == 0:
		return errors.New("a question needs at least one option")
	case len(options) > MaxOptions:
		return errors.New("a question holds at most 12 options")
	}
	if typ == Comparison && len(options) > MaxCompareOpts {
		return errors.New("a comparison shows at most three options")
	}
	seen := map[string]bool{}
	for i, o := range options {
		id, label := strings.TrimSpace(o.ID), squash(o.Label)
		switch {
		case id == "" || len([]rune(id)) > MaxIDRunes:
			return fmt.Errorf("option %d needs an id of at most 200 characters", i+1)
		case seen[id]:
			return errors.New("option IDs must be unique")
		case label == "" || len([]rune(label)) > MaxLabelRunes:
			return fmt.Errorf("option %q needs a label of at most 200 characters", id)
		case len(o.Details) > MaxDetails:
			return fmt.Errorf("option %q holds at most 4 details", id)
		case len(o.Sources) > MaxSources:
			return fmt.Errorf("option %q holds at most 5 sources", id)
		}
		seen[id] = true
		for _, d := range o.Details {
			if strings.TrimSpace(d) == "" || len([]rune(d)) > MaxDetailRunes {
				return fmt.Errorf("option %q details are a sentence each of at most 600 characters", id)
			}
		}
		for _, src := range o.Sources {
			if err := validSource(src); err != nil {
				return err
			}
		}
		if typ == Comparison && len(o.Sources) == 0 {
			return fmt.Errorf("comparison options need sources: %q has none", id)
		}
	}
	return nil
}

func (s *Store) load() ([]Panel, error) {
	raw, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Panels []Panel `json:"panels"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s is corrupt (fix or delete it): %w", s.Path, err)
	}
	return f.Panels, nil
}

func (s *Store) save(panels []Panel) error {
	raw, err := json.MarshalIndent(struct {
		Panels []Panel `json:"panels"`
	}{panels}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "choices-*.tmp")
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
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}

// Present asks a new question. A prior open panel on the same thread is
// superseded: only the newest presentation can be answered.
func (s *Store) Present(gateway string, chatID int64, thread, typ, title string, options []Option, preferred string) (Panel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread = squash(thread)
	if thread == "" {
		thread = "main"
	}
	title = squash(title)
	if title == "" {
		return Panel{}, errors.New("a question needs a title")
	}
	if len([]rune(title)) > MaxTitleRunes {
		return Panel{}, errors.New("the title is too long (200 characters at most)")
	}
	if typ != Clarification && typ != Comparison {
		return Panel{}, errors.New(`a question is "clarification" or "comparison"`)
	}
	if err := validOptions(typ, options); err != nil {
		return Panel{}, err
	}
	if preferred != "" {
		found := false
		for _, o := range options {
			if o.ID == preferred {
				found = true
			}
		}
		if !found {
			return Panel{}, errors.New("the preferred option is missing")
		}
	}
	panels, err := s.load()
	if err != nil {
		return Panel{}, err
	}
	version := 1
	now := s.now()
	for i := range panels {
		p := &panels[i]
		if p.Gateway != gateway || p.ChatID != chatID || p.Thread != thread {
			continue
		}
		if p.Version >= version {
			version = p.Version + 1
		}
		if p.Status == Open {
			p.Status, p.Updated = Superseded, now
		}
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Panel{}, err
	}
	clean := make([]Option, 0, len(options))
	for _, o := range options {
		details := make([]string, 0, len(o.Details))
		for _, d := range o.Details {
			details = append(details, strings.TrimSpace(d))
		}
		clean = append(clean, Option{ID: strings.TrimSpace(o.ID), Label: squash(o.Label), Details: details, Sources: o.Sources})
	}
	p := Panel{
		ID: hex.EncodeToString(id[:]), Gateway: gateway, ChatID: chatID,
		Thread: thread, Type: typ, Title: title, Options: clean,
		Preferred: preferred, Status: Open, Version: version,
		Created: now, Updated: now,
	}
	panels = append(panels, p)
	if err := s.save(panels); err != nil {
		return Panel{}, err
	}
	return p, nil
}

// Select records the user's pick. Only the newest open presentation answers.
func (s *Store) Select(gateway string, chatID int64, panelID, optionID string) (Panel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	panels, err := s.load()
	if err != nil {
		return Panel{}, err
	}
	for i := range panels {
		p := &panels[i]
		if p.ID != panelID {
			continue
		}
		if p.Gateway != gateway || p.ChatID != chatID {
			return Panel{}, errors.New("no such question")
		}
		switch p.Status {
		case Superseded:
			return Panel{}, errors.New("that question was replaced by a newer one")
		case Answered:
			return Panel{}, errors.New("that question is already answered")
		case Open:
		default:
			return Panel{}, errors.New("that question cannot be answered")
		}
		for _, o := range p.Options {
			if o.ID == optionID {
				p.Selected, p.Status, p.Updated = optionID, Answered, s.now()
				if err := s.save(panels); err != nil {
					return Panel{}, err
				}
				return *p, nil
			}
		}
		return Panel{}, errors.New("no such option")
	}
	return Panel{}, errors.New("no such question")
}

// Get reads one panel of this chat.
func (s *Store) Get(gateway string, chatID int64, panelID string) (Panel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	panels, err := s.load()
	if err != nil {
		return Panel{}, err
	}
	for _, p := range panels {
		if p.ID == panelID {
			if p.Gateway != gateway || p.ChatID != chatID {
				return Panel{}, errors.New("no such question")
			}
			return p, nil
		}
	}
	return Panel{}, errors.New("no such question")
}

// Render writes a panel as plain text the user can answer on any service:
// numbered options with their details and sources.
func Render(p Panel) string {
	var b strings.Builder
	kind := "pick one"
	if p.Type == Comparison {
		kind = "compare"
	}
	fmt.Fprintf(&b, "%s (%s, %d options — reply with the number or name):", p.Title, kind, len(p.Options))
	for i, o := range p.Options {
		mark := ""
		if o.ID == p.Preferred {
			mark = " ★"
		}
		fmt.Fprintf(&b, "\n%d. %s%s", i+1, o.Label, mark)
		for _, d := range o.Details {
			fmt.Fprintf(&b, "\n   - %s", d)
		}
		for _, src := range o.Sources {
			fmt.Fprintf(&b, "\n   - %s: %s", squash(src.Title), strings.TrimSpace(src.URL))
		}
	}
	return b.String()
}
