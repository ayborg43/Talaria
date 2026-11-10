package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Profile is how the user wants to be addressed: set by them in the app, read
// into the prompt with the notes. It is a few short strings, kept in a file.
type Profile struct {
	Name   string `json:"name,omitempty"`   // what to call the user
	Tone   string `json:"tone,omitempty"`   // how replies should sound, in the user's words
	Avatar string `json:"avatar,omitempty"` // an emoji or initials, shown beside their messages
	// Theme is how the app looks: "" or "light" (the default), "dark", or "auto" to follow the device.
	// It is for the page alone and is never put in the prompt.
	Theme string `json:"theme,omitempty"`
	// Onboarded is set once the user has been through the first-day introduction (or waved it away), so the
	// app stops offering it. It is for the page alone and is never put in the prompt.
	Onboarded bool `json:"onboarded,omitempty"`
}

const (
	maxProfileName   = 60
	maxProfileTone   = 200
	maxProfileAvatar = 4 // characters: an emoji (with its modifiers) or initials
)

func (n *Notes) profilePath() string { return filepath.Join(n.Dir, "profile.json") }

// Profile reads the user's profile; a missing or damaged file is an empty one.
func (n *Notes) Profile() Profile {
	var p Profile
	if raw, err := os.ReadFile(n.profilePath()); err == nil {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

// SetProfile saves the user's profile. Each field is one short line: it goes
// into the prompt, so it cannot be a place to write instructions at length.
func (n *Notes) SetProfile(p Profile) error {
	p.Name, p.Tone, p.Avatar = oneLine(p.Name), oneLine(p.Tone), oneLine(p.Avatar)
	switch {
	case len([]rune(p.Name)) > maxProfileName:
		return fmt.Errorf("the name is longer than %d characters", maxProfileName)
	case len([]rune(p.Tone)) > maxProfileTone:
		return fmt.Errorf("the tone is longer than %d characters", maxProfileTone)
	case len([]rune(p.Avatar)) > maxProfileAvatar*4: // an emoji is several code points
		return errors.New("the avatar is one emoji or a couple of initials")
	}
	switch p.Theme {
	case "", "light", "dark", "auto":
	default:
		return errors.New("the theme is light, dark or auto")
	}
	if err := os.MkdirAll(n.Dir, 0o700); err != nil {
		return err
	}
	raw, _ := json.Marshal(p)
	return os.WriteFile(n.profilePath(), raw, 0o600)
}

// Entries lists a target's notes, for the app to show and edit, with how much
// room they take and how much there is.
func (n *Notes) Entries(t Target) (entries []string, used, limit int, err error) {
	if err := checkTarget(t); err != nil {
		return nil, 0, 0, err
	}
	entries, err = n.load(t)
	return entries, len(strings.Join(entries, sep)), caps[t], err
}

// EditAt changes or removes the entry at index, which must still read as old:
// the agent may have edited the notes since the app listed them, and an edit
// aimed at one entry must never land on another. Empty text removes it.
func (n *Notes) EditAt(t Target, index int, old, text string) error {
	if err := checkTarget(t); err != nil {
		return err
	}
	entries, err := n.load(t)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(entries) || entries[index] != strings.TrimSpace(old) {
		return errors.New("the notes changed since they were listed: reload and try again")
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "§", ""))
	if text == "" {
		return n.save(t, append(entries[:index], entries[index+1:]...))
	}
	entries[index] = text
	return n.save(t, entries)
}

// profilePrompt is the profile as the prompt states it.
func (n *Notes) profilePrompt() string {
	p := n.Profile()
	var b strings.Builder
	if p.Name != "" {
		fmt.Fprintf(&b, "The user asks to be called: %s\n", p.Name)
	}
	if p.Tone != "" {
		fmt.Fprintf(&b, "The tone the user asks for in replies: %s\n", p.Tone)
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	return b.String()
}
