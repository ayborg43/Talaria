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
	"regexp"
	"strings"
	"time"

	"talaria/internal/agent"
	"talaria/internal/app"
	"talaria/internal/config"
	"talaria/internal/cron"
	"talaria/internal/kanban"
	"talaria/internal/persona"
	"talaria/internal/provider"
	"talaria/internal/trigger"
	"talaria/internal/web"
)

// A selftest answers "will a real chat work?" before a real chat finds out. It builds the agent the way the gateway builds
// one for the web chat, checks every tool it offers is something a model API will accept, and then sends a small real
// request to each model profile with all those tools attached, and asks it to use one. The failures it exists to catch are
// the ones no fake can: a tool whose schema breaks every request, a key that is refused, a model the service does not have,
// a service that rejects the token-count option, a model that cannot call tools.

// Statuses of one check.
const (
	stOK   = "ok"
	stWarn = "warn" // worth knowing, does not fail the run
	stFail = "fail"
	stSkip = "skip"
)

// checkResult is one line of the report.
type checkResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Millis int64  `json:"ms,omitempty"`
}

// selftestReport is what `tal selftest --json` prints.
type selftestReport struct {
	OK     bool          `json:"ok"`
	Checks []checkResult `json:"checks"`
}

func (r *selftestReport) add(c checkResult) { r.Checks = append(r.Checks, c) }

// finish says whether the run passed: nothing failed. A skipped or warned check does not fail it.
func (r *selftestReport) finish() {
	r.OK = true
	for _, c := range r.Checks {
		if c.Status == stFail {
			r.OK = false
		}
	}
}

var toolNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// maxTools is the most tools OpenAI's API takes in one request.
const maxTools = 128

// checkTools looks at the tool list a model would be sent, the way the model APIs will: each name is plain and at most 64
// characters, none repeats, and each schema is a JSON object of type "object".
func checkTools(specs []provider.ToolSpec) checkResult {
	var problems []string
	seen := map[string]bool{}
	for _, s := range specs {
		switch {
		case !toolNameRe.MatchString(s.Name):
			problems = append(problems, fmt.Sprintf("%q is not a name the APIs take (letters, digits, _ and -, at most 64)", clipText(s.Name, 40)))
		case seen[s.Name]:
			problems = append(problems, fmt.Sprintf("%s is offered twice", s.Name))
		}
		seen[s.Name] = true
		if len(s.Parameters) == 0 {
			continue // no arguments: the providers send an empty object
		}
		var v map[string]any
		if err := json.Unmarshal(s.Parameters, &v); err != nil {
			problems = append(problems, fmt.Sprintf("%s has a schema that is not valid JSON (%v)", s.Name, err))
		} else if v["type"] != "object" {
			problems = append(problems, fmt.Sprintf("%s has a schema that is not of type object", s.Name))
		}
	}
	res := checkResult{Name: "tools", Status: stOK, Detail: fmt.Sprintf("%d tools, all valid", len(specs))}
	switch {
	case len(problems) > 0:
		res.Status, res.Detail = stFail, strings.Join(problems, "; ")
	case len(specs) > maxTools:
		res.Status, res.Detail = stWarn, fmt.Sprintf("%d tools: OpenAI's own API takes at most %d, so a profile on it would refuse this chat", len(specs), maxTools)
	}
	return res
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// chatAgent builds an agent with the tools the gateway gives the web chat. The runtime is wired as runGateway wires it
// (saved agents, helper models, the board, the model list); keep the two in step when one gains a tool.
func (c *cli) chatAgent(deny func(string, string) bool) *agent.Agent {
	agents := &persona.Store{Path: filepath.Join(c.home, "agents.json")}
	backends := gatewayBackends(c.cfg, c.res, c.p, c.getenv, c.health, c.log)
	c.rt.SetHelpers(helperModels(backends, func(name string) string { return modelNote(c.cfg, c.health, name) }))
	c.rt.SetNamed(namedAgents(agents))
	c.rt.SetAgents(agents, c.cfg.ProfileNames)
	c.rt.SetModels(modelList(c.cfg, backends, c.health, nil))
	c.rt.SetDigest(func(time.Duration) string { return "" }) // only its schema matters here
	if c.rt.Phone() != nil {
		c.rt.SetTriggers(&trigger.Store{Path: filepath.Join(c.home, "triggers.json")})
	}
	if c.rt.Cards() != nil {
		c.rt.SetKanban(&kanban.Dispatcher{Cards: c.rt.Cards()})
	}
	return c.rt.NewAgent(app.AgentOptions{Approve: deny, Origin: "cli", NoHistory: true, ChatID: web.ChatID, Gateway: "web", Tasks: &cron.Scheduler{}})
}

// profilesToTest is the profiles to ask: the one named with -profile, else every profile in the config, else the
// implicit default of a setup with no config file ("" here).
func (c *cli) profilesToTest() []string {
	if c.overrides.Profile != "" {
		return []string{c.overrides.Profile}
	}
	if names := c.cfg.ProfileNames(); len(names) > 0 {
		return names
	}
	return []string{""}
}

// selftestProfile asks one profile's own model (never its fallbacks: a working backup must not hide a broken primary):
// a streamed request carrying every tool, and, unless quick, a round trip in which the model must call a tool.
func (c *cli) selftestProfile(ctx context.Context, name string, quick bool, report *selftestReport, progress func(checkResult)) {
	label := name
	ov := c.overrides
	if name != "" {
		ov = config.Overrides{Profile: name}
	}
	r, err := c.cfg.Resolve(ov, c.getenv)
	if err != nil {
		progress(addResult(report, checkResult{Name: orName(label, "default") + ": setup", Status: stFail, Detail: err.Error()}))
		return
	}
	label = r.Name
	if err := r.CheckKey(); err != nil {
		st := stSkip // a profile with no key is left out by the gateway too: only the one in use is a problem
		if r.Name == c.res.Name {
			st = stFail
		}
		progress(addResult(report, checkResult{Name: label + ": request", Status: st, Detail: err.Error()}))
		return
	}
	p, err := provider.New(r.Spec())
	if err != nil {
		progress(addResult(report, checkResult{Name: label + ": request", Status: stFail, Detail: err.Error()}))
		return
	}
	p = provider.Meter(p, r.Name, r.Model)
	ctx = provider.WithLabel(ctx, provider.Label{Source: "selftest"})

	var used runCost
	tap := func(s provider.Spent) {
		used.Requests++
		used.In += s.Usage.In
		used.Out += s.Usage.Out
		used.Guessed = used.Guessed || s.Usage.Estimated
	}
	c.tap.Store(&tap)
	defer c.tap.Store(nil)

	deny := func(string, string) bool { return false }
	sub := c.chatAgent(deny)
	specs := sub.Tools.Specs()

	// 1. A real, streamed request with every tool attached.
	start := time.Now()
	rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	_, err = p.Chat(rctx, provider.Request{Model: r.Model, OnText: func(string) {}, Tools: specs,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "Reply with the single word: ok"}}})
	cancel()
	res := checkResult{Name: label + " (" + r.Model + "): request", Millis: time.Since(start).Milliseconds()}
	if err != nil {
		res.Status, res.Detail = stFail, explainModelError(err)
		progress(addResult(report, res))
		return
	}
	res.Status, res.Detail = stOK, fmt.Sprintf("answered, with %d tools attached", len(specs))
	if used.Guessed {
		res.Status, res.Detail = stWarn, res.Detail+"; the service reports no token counts, so what it costs is estimated"
	}
	progress(addResult(report, res))
	if quick {
		return
	}

	// 2. The model has to call a tool, and carry on after the result: the loop every chat depends on.
	sub.Provider, sub.Model = p, r.Model
	var called []string
	sub.OnToolCall = func(name, _, _ string) { called = append(called, name) }
	start = time.Now()
	tctx, cancel2 := context.WithTimeout(ctx, 120*time.Second)
	reply, err := sub.Run(tctx, "Call the current_time tool once, then answer with just the year it reports.")
	cancel2()
	res = checkResult{Name: label + " (" + r.Model + "): tool use", Millis: time.Since(start).Milliseconds()}
	switch {
	case err != nil:
		res.Status, res.Detail = stFail, explainModelError(err)
	case len(called) == 0:
		res.Status, res.Detail = stWarn, "it answered without using the tool: this model may not support tool calls, and would only chat"
	case strings.TrimSpace(reply) == "":
		res.Status, res.Detail = stWarn, "it called "+called[0]+" but gave no answer afterwards"
	default:
		res.Status, res.Detail = stOK, "called "+called[0]+" and answered"
	}
	progress(addResult(report, res))
}

func orName(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func addResult(r *selftestReport, c checkResult) checkResult {
	r.add(c)
	return c
}

// explainModelError says in a line what went wrong with a request, in the words Settings uses.
func explainModelError(err error) string {
	if strings.Contains(err.Error(), "schema that is not valid JSON") || strings.Contains(err.Error(), "MarshalJSON") {
		return err.Error()
	}
	var se *provider.StatusError
	if errors.As(err, &se) && se.Status == 400 && strings.Contains(strings.ToLower(se.Error()), "stream_options") {
		return "the service rejects the token-count option (stream_options): " + clipText(se.Error(), 160)
	}
	return provider.Short(err) + " — " + clipText(err.Error(), 200)
}

// selftestIcon is how a status is drawn in the plain report.
func selftestIcon(s string) string {
	switch s {
	case stOK:
		return "ok  "
	case stWarn:
		return "warn"
	case stSkip:
		return "skip"
	}
	return "FAIL"
}

// printCheck writes one line of the plain report.
func printCheck(w io.Writer, c checkResult) {
	t := ""
	if c.Millis > 0 {
		t = fmt.Sprintf(" (%.1fs)", float64(c.Millis)/1000)
	}
	fmt.Fprintf(w, "%s  %-34s %s%s\n", selftestIcon(c.Status), c.Name, c.Detail, t)
}

// runSelftestCmd is `tal selftest [--json] [--quick] [-profile NAME]`: it checks that a real chat would work, and exits 1
// when something it needs does not. It sends a few small requests to the real models (the second asks for a tool call:
// leave it out with --quick), so it costs a little. Its requests are counted in the usage ledger as "selftest".
func (c *cli) runSelftestCmd(args []string) error {
	fs := flag.NewFlagSet("selftest", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "print one JSON document instead of a table")
	quick := fs.Bool("quick", false, "only send the plain request to each model: skip asking it to call a tool")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("use: tal selftest [--json] [--quick]   (-profile NAME, before the command, tests one profile)")
	}
	report := &selftestReport{}
	progress := func(r checkResult) {
		if !*asJSON {
			printCheck(os.Stdout, r)
		}
	}
	emit := func() error {
		report.finish()
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(report)
		} else if report.OK {
			fmt.Println("\nA chat should work.")
		} else {
			n := 0
			for _, r := range report.Checks {
				if r.Status == stFail {
					n++
				}
			}
			fmt.Printf("\n%d check(s) failed: a chat would hit these.\n", n)
		}
		if !report.OK {
			return exitError{exitFailed}
		}
		return nil
	}

	// Everything the gateway sets up first: the environment, the vault, connected services, the runtime.
	if err := c.setupModel(true); err != nil {
		progress(addResult(report, checkResult{Name: "setup", Status: stFail, Detail: err.Error()}))
		return emit()
	}
	defer c.closeModel()
	progress(addResult(report, checkResult{Name: "setup", Status: stOK, Detail: "the runtime started"}))

	deny := func(string, string) bool { return false }
	progress(addResult(report, checkTools(c.chatAgent(deny).Tools.Specs())))

	for _, name := range c.profilesToTest() {
		c.selftestProfile(c.ctx, name, *quick, report, progress)
		if c.ctx.Err() != nil {
			break
		}
	}
	return emit()
}
