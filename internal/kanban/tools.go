package kanban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"talaria/internal/tool"
)

// Binding says whose board the tools work on, and what they can reach beyond it.
type Binding struct {
	Store   *Store
	Gateway string
	ChatID  int64
	// Agents lists the names of the user's saved agents, for choosing who does a card.
	Agents func() []string
	// Kick starts what can be started now. Nil leaves that to the scheduler's next look.
	Kick func()
	// Stop asks for a running card's task to be cancelled, and reports whether it was.
	Stop func(job string) bool
	// Create offers the tools that change the board. A background task or a scheduled job only reads it: work that
	// is itself started from a card must not be able to start more.
	Create bool
}

func (b Binding) agents() []string {
	if b.Agents == nil {
		return nil
	}
	return b.Agents()
}

// ValidAgent says whether name can do a card: nobody yet, Talaria itself, or one of the saved agents.
func ValidAgent(name string, saved []string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, Main) {
		return nil
	}
	for _, s := range saved {
		if strings.EqualFold(s, name) {
			return nil
		}
	}
	if len(saved) == 0 {
		return fmt.Errorf("there is no saved agent called %q: none are set up, so use %q for Talaria itself", name, Main)
	}
	return fmt.Errorf("there is no saved agent called %q: choose one of %s, or %q for Talaria itself", name, strings.Join(saved, ", "), Main)
}

// CanonicalAgent is a saved agent's name as it was saved, whatever case it was asked for in.
func CanonicalAgent(name string, saved []string) string {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, Main) {
		return Main
	}
	for _, s := range saved {
		if strings.EqualFold(s, name) {
			return s
		}
	}
	return name
}

// RegisterTools adds the board's tools for one chat.
func RegisterTools(r *tool.Registry, b Binding) {
	r.Register(tool.Tool{
		Name:        "kanban_list",
		Description: "Show the user's board: cards in Backlog, Ready, Running, Review and Done, each with its id, agent and, for a finished one, what it reported. Use it before adding a card, so the same job is not put on twice.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"column":{"type":"string","enum":["backlog","ready","running","review","done"],"description":"only this column"}}}`),
		Parallel:    true,
		Run:         b.list,
	})
	if !b.Create {
		return
	}
	agentHelp := "who does it: the name of one of the user's saved agents, or \"main\" for Talaria itself. Left out, the card waits for the user to pick"
	if names := b.agents(); len(names) > 0 {
		agentHelp += ". Saved agents: " + strings.Join(names, ", ")
	}
	helpJSON, _ := json.Marshal(agentHelp) // it holds quotation marks, so it must be written as a JSON string, not pasted in
	moveHelpJSON, _ := json.Marshal(agentHelp + "; only when moving to ready")
	r.Register(tool.Tool{
		Name: "kanban_add",
		Description: "Put a card on the user's board: a piece of work to do. It goes to Backlog unless you say \"ready\", and it does nothing there. " +
			"A card in Ready with an agent starts as a background task at once (or when there is room), so put it in Ready only when the user has asked for the work to start; you are asked first. " +
			"The card has no memory of this conversation, so write the title and body so that whoever does it can do it alone. " +
			"allow_risky lets that task use tools that normally ask first (a task cannot ask anyone), so set it only when the work truly needs them.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"title":{"type":"string","description":"a short name, at most 120 characters"},
			"body":{"type":"string","description":"the detail: complete and self-contained"},
			"column":{"type":"string","enum":["backlog","ready"]},
			"agent":{"type":"string","description":` + string(helpJSON) + `},
			"allow_risky":{"type":"boolean"}},
			"required":["title"]}`),
		NeedsApprovalFor: func(raw json.RawMessage) bool {
			var a struct {
				Column     string
				AllowRisky bool `json:"allow_risky"`
			}
			json.Unmarshal(raw, &a)
			return strings.EqualFold(a.Column, Ready) || a.AllowRisky
		},
		Describe: func(raw json.RawMessage) string {
			var a struct{ Title, Column, Agent string }
			json.Unmarshal(raw, &a)
			if strings.EqualFold(a.Column, Ready) {
				return fmt.Sprintf("put %q on the board as Ready, so it starts as a task (agent: %s)", a.Title, orNobody(a.Agent))
			}
			return fmt.Sprintf("put %q on the board", a.Title)
		},
		Run: b.add,
	})
	r.Register(tool.Tool{
		Name: "kanban_move",
		Description: "Move a card to another column: backlog, ready, review or done. Moving it to Ready starts it as a background task if it has an agent (give one here if it has none); you are asked first. " +
			"A card moves to Running and on to Review by itself. Moving a running card stops its task. Moving a finished card back to Ready runs it again.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"id":{"type":"string"},
			"column":{"type":"string","enum":["backlog","ready","review","done"]},
			"agent":{"type":"string","description":` + string(moveHelpJSON) + `},
			"allow_risky":{"type":"boolean","description":"only when moving to ready"}},
			"required":["id","column"]}`),
		NeedsApprovalFor: func(raw json.RawMessage) bool {
			var a struct {
				ID, Column string
				AllowRisky bool `json:"allow_risky"`
			}
			json.Unmarshal(raw, &a)
			if strings.EqualFold(a.Column, Ready) || a.AllowRisky {
				return true
			}
			c, err := b.Store.Get(b.Gateway, b.ChatID, a.ID)
			return err == nil && c.Column == Running // stopping work asks, as task_cancel does
		},
		Describe: func(raw json.RawMessage) string {
			var a struct{ ID, Column, Agent string }
			json.Unmarshal(raw, &a)
			title := a.ID
			if c, err := b.Store.Get(b.Gateway, b.ChatID, a.ID); err == nil {
				title = fmt.Sprintf("%q", c.Title)
				if c.Column == Running {
					return "stop " + title + ", which is running"
				}
			}
			if strings.EqualFold(a.Column, Ready) {
				return fmt.Sprintf("move %s to Ready, so it starts as a task", title)
			}
			return fmt.Sprintf("move %s to %s", title, a.Column)
		},
		Run: b.move,
	})
	r.Register(tool.Tool{
		Name:          "kanban_remove",
		Description:   "Take a card off the board for good. A running card cannot be removed: stop it first.",
		Schema:        json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		NeedsApproval: true,
		Risk:          tool.RiskDeletes,
		Run:           b.remove,
	})
}

func orNobody(agent string) string {
	if strings.TrimSpace(agent) == "" {
		return "none picked"
	}
	return agent
}

// Line is one card as one line of text.
func Line(c Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s | %s", c.ID, c.Title)
	switch {
	case c.Agent == Main:
		b.WriteString(" | agent: main")
	case c.Agent != "":
		b.WriteString(" | agent: " + c.Agent)
	case c.Column == Ready:
		b.WriteString(" | waiting for an agent to be picked")
	}
	if c.AllowRisky {
		b.WriteString(" | may use risky tools")
	}
	if c.Status != "" {
		b.WriteString(" | " + c.Status)
	}
	return b.String()
}

func (b Binding) list(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct{ Column string }
	json.Unmarshal(raw, &a)
	cards, err := b.Store.List(b.Gateway, b.ChatID)
	if err != nil {
		return "", err
	}
	want := strings.ToLower(strings.TrimSpace(a.Column))
	if want != "" && !slices.Contains(Columns, want) {
		return "", fmt.Errorf("there is no column %q: use one of %s", want, strings.Join(Columns, ", "))
	}
	var out strings.Builder
	for _, col := range Columns {
		if want != "" && col != want {
			continue
		}
		n := 0
		for _, c := range cards {
			if c.Column != col {
				continue
			}
			if n == 0 {
				fmt.Fprintf(&out, "%s:\n", strings.ToUpper(col[:1])+col[1:])
			}
			n++
			out.WriteString("  " + Line(c) + "\n")
			if c.Body != "" && col != Done {
				out.WriteString("    " + oneLine(c.Body, 200) + "\n")
			}
			if c.Result != "" && (col == Review || col == Done) {
				out.WriteString("    reported: " + oneLine(c.Result, 300) + "\n")
			}
		}
	}
	if out.Len() == 0 {
		if want != "" {
			return "no cards in " + want, nil
		}
		return "the board is empty", nil
	}
	return strings.TrimSpace(out.String()), nil
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (b Binding) kick() {
	if b.Kick != nil {
		b.Kick()
	}
}

func (b Binding) add(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Title, Body, Column, Agent string
		AllowRisky                 bool `json:"allow_risky"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	col := strings.ToLower(strings.TrimSpace(a.Column))
	if col != "" && col != Backlog && col != Ready {
		return "", errors.New("a new card goes to backlog or ready")
	}
	saved := b.agents()
	if err := ValidAgent(a.Agent, saved); err != nil {
		return "", err
	}
	c, err := b.Store.Add(b.Gateway, b.ChatID, Card{Title: a.Title, Body: a.Body, Column: col, Agent: CanonicalAgent(a.Agent, saved), AllowRisky: a.AllowRisky})
	if err != nil {
		return "", err
	}
	if c.Column == Ready {
		b.kick()
		return startedNote(b.Store, b, c), nil
	}
	return "added card " + c.ID + " to the backlog", nil
}

func startedNote(s *Store, b Binding, c Card) string {
	if c.Agent == "" {
		return fmt.Sprintf("card %s is Ready, but it has no agent, so nothing starts until one is picked", c.ID)
	}
	if now, err := s.Get(b.Gateway, b.ChatID, c.ID); err == nil && now.Column == Running {
		return fmt.Sprintf("card %s is running as a background task; it moves to Review when the task ends", c.ID)
	}
	return fmt.Sprintf("card %s is Ready; it starts as soon as there is room (at most 3 tasks run at once)", c.ID)
}

func (b Binding) move(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		ID, Column, Agent string
		AllowRisky        bool `json:"allow_risky"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", errors.New("a card id is needed")
	}
	col := strings.ToLower(strings.TrimSpace(a.Column))
	cur, err := b.Store.Get(b.Gateway, b.ChatID, a.ID)
	if err != nil {
		return "", err
	}
	if cur.Column == Running { // moving a running card away is stopping it
		if col == Ready || col == Running {
			return "", errors.New("it is already running")
		}
		if b.Stop == nil || cur.Job == "" || !b.Stop(cur.Job) {
			return "", errors.New("its task could not be stopped just now: it may have just finished, or has not started yet; look at the board again")
		}
		return "asked its task to stop; the card moves to Review once it has", nil
	}
	if col == Ready {
		saved := b.agents()
		if err := ValidAgent(a.Agent, saved); err != nil {
			return "", err
		}
		if a.Agent != "" || a.AllowRisky {
			if _, err := b.Store.Edit(b.Gateway, b.ChatID, a.ID, func(c *Card) {
				if a.Agent != "" {
					c.Agent = CanonicalAgent(a.Agent, saved)
				}
				if a.AllowRisky {
					c.AllowRisky = true
				}
			}); err != nil {
				return "", err
			}
		}
	}
	c, err := b.Store.Move(b.Gateway, b.ChatID, a.ID, col)
	if err != nil {
		return "", err
	}
	if c.Column == Ready {
		b.kick()
		return startedNote(b.Store, b, c), nil
	}
	return fmt.Sprintf("moved card %s to %s", c.ID, c.Column), nil
}

func (b Binding) remove(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct{ ID string }
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if err := b.Store.Delete(b.Gateway, b.ChatID, a.ID); err != nil {
		return "", err
	}
	return "removed", nil
}
