package persona

import (
	"errors"
	"fmt"
	"strings"
)

// MaxFile is the most an agent file may hold. A real one is a name and up to MaxInstructions characters.
const MaxFile = 8 << 10

// FileVersion is the version of the agent file format this build writes and reads.
const FileVersion = 1

// Export writes an agent as one small Markdown file that can be sent to someone else:
//
//	---
//	talaria-agent: 1
//	name: Researcher
//	icon: 🔎
//	looks-only: true
//	---
//
//	What it is for and how it should behave...
//
// It holds only what the person wrote for the agent. The model profile is left out (profiles are the owner's own, with
// their keys), and so is everything else: notes, memory, goals, conversations, and anything about the machine it ran on.
func Export(a Agent) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "talaria-agent: %d\n", FileVersion)
	fmt.Fprintf(&b, "name: %s\n", oneLine(a.Name))
	if a.Icon != "" {
		fmt.Fprintf(&b, "icon: %s\n", oneLine(a.Icon))
	}
	fmt.Fprintf(&b, "looks-only: %t\n", a.ReadOnly)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(a.Instructions))
	b.WriteString("\n")
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Parse reads an agent file. It is strict: the first line must be "---", the header must say which version it is, and
// only the keys above are allowed. The agent it returns has no profile, and is not yet saved or even approved: whoever
// imports it should show the person exactly what it says, since its instructions steer the agent in every conversation
// with it. Its ReadOnly is what the file asked for; an importer should default to the safe side.
func Parse(text string) (Agent, error) {
	if len(text) > MaxFile {
		return Agent{}, fmt.Errorf("that file is larger than an agent file (%d KB)", MaxFile>>10)
	}
	text = strings.ReplaceAll(strings.TrimPrefix(text, "\ufeff"), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return Agent{}, errors.New("this is not a Talaria agent file (it should start with ---)")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return Agent{}, errors.New("the header of this agent file is not closed with ---")
	}
	var a Agent
	version := ""
	for _, l := range lines[1:end] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			return Agent{}, fmt.Errorf("a line in the header is not key: value: %q", clipLine(l))
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "talaria-agent":
			version = v
		case "name":
			a.Name = v
		case "icon":
			a.Icon = v
		case "looks-only":
			switch strings.ToLower(v) {
			case "true", "yes":
				a.ReadOnly = true
			case "false", "no":
			default:
				return Agent{}, errors.New("looks-only is true or false")
			}
		default:
			return Agent{}, fmt.Errorf("this agent file has a setting this version does not know: %q", clipLine(k))
		}
	}
	if version != fmt.Sprint(FileVersion) {
		return Agent{}, errors.New("this agent file is not a version this Talaria reads")
	}
	a.Instructions = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return Check(a)
}

func clipLine(s string) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return strings.TrimSpace(s)
}
