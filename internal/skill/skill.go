// Package skill stores reusable procedures as SKILL.md files the agent can
// load on demand and write itself.
package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxBody = 64 << 10

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type Skill struct {
	Name        string
	Description string
	Body        string
}

// Store keeps one directory per skill: <Dir>/<name>/SKILL.md.
type Store struct {
	Dir string
}

func ValidName(name string) bool { return nameRE.MatchString(name) }

func (s *Store) path(name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid skill name %q: use lowercase letters, digits and hyphens (max 64)", name)
	}
	return filepath.Join(s.Dir, name, "SKILL.md"), nil
}

// Save creates or overwrites a skill.
func (s *Store) Save(name, description, body string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	description = strings.Join(strings.Fields(description), " ")
	if description == "" {
		return errors.New("description is required")
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("body is required")
	}
	if len(body) > maxBody {
		return fmt.Errorf("body too large (%d bytes, max %d)", len(body), maxBody)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	doc := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", name, description, strings.TrimSpace(body))
	return os.WriteFile(p, []byte(doc), 0o600)
}

func (s *Store) Load(name string) (Skill, error) {
	p, err := s.path(name)
	if err != nil {
		return Skill{}, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return Skill{}, fmt.Errorf("no skill named %q", name)
		}
		return Skill{}, err
	}
	sk := parse(string(raw))
	sk.Name = name
	return sk, nil
}

func (s *Store) Delete(name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("no skill named %q", name)
	}
	return os.RemoveAll(filepath.Dir(p))
}

// List returns all skills sorted by name. Unreadable skills are skipped.
func (s *Store) List() []Skill {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if sk, err := s.Load(e.Name()); err == nil {
			out = append(out, sk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Index renders the skill list for the system prompt: names and descriptions
// only, so full bodies are loaded on demand with skill_view.
func (s *Store) Index() string {
	skills := s.List()
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Available skills (load one with skill_view before following it):\n")
	for _, sk := range skills {
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
	}
	return b.String()
}

// parse reads optional "---" frontmatter with simple "key: value" lines.
func parse(doc string) Skill {
	var sk Skill
	rest := strings.TrimLeft(doc, "\n")
	if strings.HasPrefix(rest, "---\n") {
		if end := strings.Index(rest[4:], "\n---"); end >= 0 {
			for _, line := range strings.Split(rest[4:4+end], "\n") {
				k, v, ok := strings.Cut(line, ":")
				if ok && strings.TrimSpace(k) == "description" {
					sk.Description = strings.TrimSpace(v)
				}
			}
			rest = strings.TrimPrefix(rest[4+end+4:], "\n")
		}
	}
	sk.Body = strings.TrimSpace(rest)
	return sk
}
