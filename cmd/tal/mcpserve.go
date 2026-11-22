package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"talaria/internal/kanban"
	"talaria/internal/mcpserve"
	"talaria/internal/persona"
	"talaria/internal/runlog"
	"talaria/internal/usage"
	"talaria/internal/version"
	"talaria/internal/web"
)

// runMCPServe is `tal mcp serve`: Talaria as an MCP server on standard input and output, for Claude Desktop, Cursor or
// any other client that can start a program. What it offers is a short, fixed list (see mcpTools): ask it to do
// something, look at the board and the tasks, add a card to the backlog, and read the digest and the cost. It offers no
// way to approve anything, delete, change settings, read credentials or start anything on the board: a client can hand
// Talaria work, never lift one of its limits.
func (c *cli) runMCPServe(args []string) error {
	if err := c.setupModel(false); err != nil {
		return err
	}
	defer c.closeModel()
	srv := &mcpserve.Server{
		Name: "talaria", Version: version.String(),
		Instructions: "Talaria is the user's own personal agent. `ask` hands it a piece of work and returns its answer; it runs without anyone to approve " +
			"risky steps, so anything that sends, posts, deletes or changes something is refused and reported. The other tools look at what it is doing.",
		Tools: c.mcpTools(),
		Logf:  func(f string, a ...any) { c.log.Warn(fmt.Sprintf(f, a...)) },
	}
	return srv.Serve(c.ctx, os.Stdin, os.Stdout)
}

func schema(props string, required ...string) json.RawMessage {
	req := ""
	if len(required) > 0 {
		req = `,"required":["` + strings.Join(required, `","`) + `"]`
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `}` + req + `}`)
}

// mcpTools is everything an MCP client may use.
func (c *cli) mcpTools() []mcpserve.Tool {
	zone, _ := calendarZone(c.cfg, c.getenv)
	agents := &persona.Store{Path: filepath.Join(c.home, "agents.json")}
	runs := &runlog.Log{Path: filepath.Join(c.home, "runs.json")}
	src := digestSources{home: c.home, zone: zone, runs: runs, watches: c.rt.Watches(), goals: c.rt.Goals(), cards: c.rt.Cards(),
		ledger: func() *usage.Ledger { return c.ledger }, prices: usagePrices(c.cfg)}
	short := func(s string, n int) string { return tidy(s, n, "") }

	return []mcpserve.Tool{
		{Name: "ask", Description: "Hand Talaria a piece of work and wait for its answer. It can search the web, read and write files, use the browser and the user's calendar and mail, " +
			"and so on, like in a chat, except that nobody is there to approve risky steps: anything that sends, posts, deletes or changes something is refused and listed in the answer. " +
			"`agent` names one of the user's saved agents to do it (see `agents`).",
			Schema: schema(`"prompt":{"type":"string","description":"What to do, with everything it needs: it has no memory of this conversation"},"agent":{"type":"string","description":"A saved agent's name; leave out for Talaria itself"}`, "prompt"),
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct{ Prompt, Agent string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", fmt.Errorf("the arguments were not understood")
				}
				res := c.runOnce(ctx, runRequest{Prompt: a.Prompt, Agent: a.Agent})
				if !res.OK {
					return "", fmt.Errorf("it could not finish: %s", res.Error)
				}
				out := res.Reply
				if len(res.Denied) > 0 {
					out += "\n\n[Refused, because it needs the user's approval and nobody could give it: " + strings.Join(res.Denied, ", ") + "]"
				}
				return out, nil
			}},
		{Name: "agents", Description: "List the user's saved agents: name, what each is for, its model, and whether it only looks.", ReadOnly: true,
			Run: func(context.Context, json.RawMessage) (string, error) {
				list := agents.List()
				if len(list) == 0 {
					return "There are no saved agents.", nil
				}
				var b strings.Builder
				for _, a := range list {
					fmt.Fprintf(&b, "- %s: %s", a.Name, short(a.About(), 160))
					if a.Profile != "" {
						fmt.Fprintf(&b, " [model %s]", a.Profile)
					}
					if a.ReadOnly {
						b.WriteString(" [looks only]")
					}
					b.WriteByte('\n')
				}
				return b.String(), nil
			}},
		{Name: "board", Description: "Show the board of work lined up for Talaria: every card with its column (backlog, ready, running, review, done), the agent that does it, and what the last run reported.", ReadOnly: true,
			Run: func(context.Context, json.RawMessage) (string, error) {
				cs, err := c.rt.Cards().List("web", web.ChatID)
				if err != nil {
					return "", fmt.Errorf("the board could not be read")
				}
				if len(cs) == 0 {
					return "The board is empty.", nil
				}
				var b strings.Builder
				for _, k := range cs {
					fmt.Fprintf(&b, "- [%s] %s", k.Column, k.Title)
					if k.Agent != "" {
						fmt.Fprintf(&b, " (%s)", k.Agent)
					}
					if k.Result != "" {
						fmt.Fprintf(&b, ": %s", short(k.Result, 200))
					}
					b.WriteByte('\n')
				}
				return b.String(), nil
			}},
		{Name: "board_add", Description: "Add a card to the board's backlog. It is only a note until the user moves it to Ready: adding a card starts nothing.",
			Schema: schema(`"title":{"type":"string"},"body":{"type":"string","description":"What the work is, with what it needs"},"agent":{"type":"string","description":"A saved agent's name, if one should do it"}`, "title"),
			Run: func(_ context.Context, raw json.RawMessage) (string, error) {
				var a struct{ Title, Body, Agent string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", fmt.Errorf("the arguments were not understood")
				}
				var names []string
				for _, p := range agents.List() {
					names = append(names, p.Name)
				}
				if err := kanban.ValidAgent(a.Agent, names); err != nil {
					return "", err
				}
				card, err := c.rt.Cards().Add("web", web.ChatID, kanban.Card{Title: a.Title, Body: a.Body, Column: kanban.Backlog, Agent: kanban.CanonicalAgent(a.Agent, names)})
				if err != nil {
					return "", err
				}
				return "Added to the backlog: " + card.Title + " (it starts nothing until the user moves it to Ready).", nil
			}},
		{Name: "tasks", Description: "List background tasks that finished lately and how each ended.", ReadOnly: true,
			Run: func(context.Context, json.RawMessage) (string, error) {
				done := c.rt.Cron.DoneTasks("web", web.ChatID)
				if len(done) == 0 {
					return "No background task has finished lately.", nil
				}
				var b strings.Builder
				for i, d := range done {
					if i == 15 {
						break
					}
					state := "done"
					if d.Failed() {
						state = "failed"
					}
					fmt.Fprintf(&b, "- %s (%s, %s): %s\n", short(d.Job.Name, 80), state, d.At.In(zone).Format("Mon 15:04"), short(d.Status, 200))
				}
				return b.String(), nil
			}},
		{Name: "while_away", Description: "Say what Talaria did on its own over the last hours: work that finished, watches that saw something, tool calls, cost, and what waits for the user.", ReadOnly: true,
			Schema: schema(`"hours":{"type":"integer","minimum":1,"maximum":720}`),
			Run: func(_ context.Context, raw json.RawMessage) (string, error) {
				var a struct{ Hours int }
				json.Unmarshal(raw, &a)
				if a.Hours < 1 || a.Hours > 720 {
					a.Hours = 24
				}
				now := time.Now()
				return src.build(now.Add(-time.Duration(a.Hours)*time.Hour), now).Text, nil
			}},
		{Name: "usage", Description: "Say how much the AI models were used and what that cost over the last days (default 7).", ReadOnly: true,
			Schema: schema(`"days":{"type":"integer","minimum":1,"maximum":90}`),
			Run: func(_ context.Context, raw json.RawMessage) (string, error) {
				var a struct{ Days int }
				json.Unmarshal(raw, &a)
				if a.Days < 1 || a.Days > 90 {
					a.Days = 7
				}
				c.ledger.Flush()
				return c.ledger.Report(a.Days, usagePrices(c.cfg)).Summary(usagePeriod(a.Days)), nil
			}},
	}
}
