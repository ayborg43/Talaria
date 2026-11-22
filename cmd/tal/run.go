package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"talaria/internal/app"
	"talaria/internal/config"
	"talaria/internal/persona"
	"talaria/internal/provider"
)

// runRequest is one piece of work to hand to Talaria from outside: a script, CI, another program.
type runRequest struct {
	Prompt string
	// Agent is the name of a saved agent to do it, "" for Talaria's own.
	Agent string
	// AllowRisky lets the work use tools that normally ask first. Nobody is there to answer, so without it they are refused.
	AllowRisky bool
}

// runTool is one tool call the run made.
type runTool struct {
	Tool   string `json:"tool"`
	Args   string `json:"args,omitempty"`
	Denied bool   `json:"denied,omitempty"`
}

// runResult is what a run came to. It is what `tal run --json` prints.
type runResult struct {
	OK      bool      `json:"ok"`
	Reply   string    `json:"reply"`
	Error   string    `json:"error,omitempty"`
	Agent   string    `json:"agent,omitempty"`
	Profile string    `json:"profile"`
	Model   string    `json:"model"`
	Tools   []runTool `json:"tools"`
	// Denied lists the tools that needed approval and were refused, so a script can tell "finished" from "could not finish".
	Denied  []string `json:"denied,omitempty"`
	Usage   runCost  `json:"usage"`
	Seconds float64  `json:"seconds"`
}

type runCost struct {
	Requests int  `json:"requests"`
	In       int  `json:"tokens_in"`
	Out      int  `json:"tokens_out"`
	Guessed  bool `json:"estimated,omitempty"` // a count was guessed from the text because the service gave none
}

// Exit codes of `tal run`: 0 finished, 1 failed, 3 finished only as far as its rules allowed (something needed approval
// and was refused).
const (
	exitFailed     = 1
	exitIncomplete = 3
)

// savedAgentByName finds one of the saved agents.
func savedAgentByName(home, name string) (persona.Agent, bool) {
	st := &persona.Store{Path: filepath.Join(home, "agents.json")}
	for _, a := range st.List() {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return persona.Agent{}, false
}

// providerFor opens a model profile the way a job does: its own provider with the fallback chain behind it.
func (c *cli) providerFor(profile string) (provider.Provider, string, error) {
	r, err := c.cfg.Resolve(config.Overrides{Profile: profile}, c.getenv)
	if err != nil {
		return nil, "", err
	}
	if err := r.CheckKey(); err != nil {
		return nil, "", err
	}
	p, err := provider.New(r.Spec())
	if err != nil {
		return nil, "", err
	}
	p = provider.Meter(p, r.Name, r.Model)
	return withFallbacks(c.cfg, c.getenv, c.health, c.log, r.Name, r.Model, p), r.Model, nil
}

// runOnce does one piece of work to the end and says what happened. It is unattended: a tool that needs approval is
// refused unless req.AllowRisky, and the agent cannot schedule or start more work.
func (c *cli) runOnce(ctx context.Context, req runRequest) runResult {
	start := time.Now()
	res := runResult{Profile: c.res.Name, Model: c.res.Model, Tools: []runTool{}}
	fail := func(err error) runResult {
		res.Error = err.Error()
		res.Seconds = time.Since(start).Seconds()
		return res
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return fail(errors.New("there is nothing to do: the prompt is empty"))
	}
	var named *persona.Agent
	if req.Agent != "" && !strings.EqualFold(req.Agent, "main") {
		a, ok := savedAgentByName(c.home, req.Agent)
		if !ok {
			return fail(fmt.Errorf("there is no saved agent called %q", req.Agent))
		}
		named, res.Agent = &a, a.Name
	}

	var mu sync.Mutex
	approve := func(name, _ string) bool {
		if req.AllowRisky {
			return true
		}
		mu.Lock()
		res.Denied = append(res.Denied, name)
		mu.Unlock()
		return false
	}
	opts := app.AgentOptions{Approve: approve, Origin: "cli", NoCron: true,
		ExtraSystem: "You are running unattended from a script or another program, so you cannot ask questions. Your reply is returned to it as text: keep it short, and say plainly anything you could not do."}
	if named != nil {
		opts.ExtraSystem += "\n\n" + named.System()
	}
	a := c.rt.NewAgent(opts)
	if named != nil {
		a.ReadOnly = named.ReadOnly
		if len(named.Scopes) > 0 {
			a.ToolAllowed = named.AllowsTool
		}
		if named.Profile != "" {
			if p, model, err := c.providerFor(named.Profile); err == nil {
				a.Provider, a.Model = p, model
				res.Profile, res.Model = named.Profile, model
			}
			// a profile that cannot be opened is not the caller's choice: the work goes on the usual model
		}
	}
	a.OnToolCall = func(name, args, _ string) {
		mu.Lock()
		defer mu.Unlock()
		res.Tools = append(res.Tools, runTool{Tool: name, Args: oneLine(a.Safe(args), 160)})
	}

	// What it cost: every answered request of this run reports here as well as to the ledger.
	var used runCost
	tap := func(s provider.Spent) {
		mu.Lock()
		defer mu.Unlock()
		used.Requests++
		used.In += s.Usage.In
		used.Out += s.Usage.Out
		used.Guessed = used.Guessed || s.Usage.Estimated
	}
	c.tap.Store(&tap)
	defer c.tap.Store(nil)

	ctx = provider.WithLabel(ctx, provider.Label{Source: "cli", Agent: res.Agent})
	reply, err := a.Run(ctx, req.Prompt)
	mu.Lock()
	defer mu.Unlock()
	res.Usage, res.Reply, res.Seconds = used, a.Safe(reply), time.Since(start).Seconds()
	for i := range res.Tools {
		for _, d := range res.Denied {
			if res.Tools[i].Tool == d {
				res.Tools[i].Denied = true
			}
		}
	}
	if err != nil {
		res.Error = a.Safe(err.Error())
		return res
	}
	res.OK = true
	return res
}

// runCmd is `tal run [--json] [--agent NAME] [--allow-risky] PROMPT...`: one piece of work, unattended, then exit.
func (c *cli) runRunCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "print one JSON document: the reply, the tools used, what it cost, and how it ended")
	agentName := fs.String("agent", "", "a saved agent to do it")
	risky := fs.Bool("allow-risky", false, "let it use tools that normally ask first (nobody is there to answer)")
	usage := "use: tal run [--json] [--agent NAME] [--allow-risky] PROMPT   (or PROMPT on standard input; -profile picks the model)"
	if err := fs.Parse(args); err != nil {
		return errors.New(usage)
	}
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" || prompt == "-" {
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			return err
		}
		prompt = strings.TrimSpace(string(raw))
	}
	if prompt == "" {
		return errors.New(usage)
	}
	res := c.runOnce(c.ctx, runRequest{Prompt: prompt, Agent: *agentName, AllowRisky: *risky || c.yolo})
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(res)
	} else {
		if res.Reply != "" {
			fmt.Println(res.Reply)
		}
		for _, t := range res.Tools {
			fmt.Fprintf(os.Stderr, "· %s %s\n", t.Tool, t.Args)
		}
		if res.Error != "" {
			fmt.Fprintln(os.Stderr, "error:", res.Error)
		}
		if len(res.Denied) > 0 {
			fmt.Fprintf(os.Stderr, "refused, because it needs approval and nobody could give it: %s (use --allow-risky to let it)\n", strings.Join(res.Denied, ", "))
		}
	}
	switch {
	case !res.OK:
		return exitError{exitFailed}
	case len(res.Denied) > 0:
		return exitError{exitIncomplete}
	}
	return nil
}
