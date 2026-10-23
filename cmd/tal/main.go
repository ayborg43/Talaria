// Command tal is the Talaria CLI.
//
//	tal               interactive chat
//	tal -p "..."      run one prompt and exit
//	tal -profile claude   pick a provider profile from the config file
//	tal gateway       serve Telegram and Discord, and run scheduled jobs (see README)
//	tal cron list     show scheduled jobs
//	tal config        show how the provider settings resolve (also: init, path)
//	tal mcp list      connect to the configured MCP servers and list their tools
//	tal log           what the agent has run (also: log 50, log denied)
//	tal policy        permission rules and "always allow" choices (also: policy forget <chat|all>)
//	tal connect       sign in to an MCP server that uses OAuth (also: connect NAME, disconnect NAME)
//	tal vault         saved logins the agent can use without seeing them (init, add, list, remove, passwd)
//	tal workspace     set up the persistent workspace with a browser (up, down, status, shell, remove)
//	tal env           list execution environments (also: check [name]); -env picks one to run in
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"talaria/internal/agent"
	"talaria/internal/app"
	"talaria/internal/config"
	"talaria/internal/cron"
	"talaria/internal/execenv"
	"talaria/internal/gateway/chat"
	"talaria/internal/goal"
	"talaria/internal/guard"
	"talaria/internal/mcp"
	"talaria/internal/provider"
	"talaria/internal/vault"
	"talaria/internal/version"
	"talaria/internal/watch"
)

func main() {
	if err := run(); err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// exitError returns a process exit code from a command. Unlike other errors it
// prints nothing: the command has already said what is wrong.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

// cli carries what every command needs: where things are, how to report, and
// the model setup once a command that needs it has asked for it.
type cli struct {
	ctx       context.Context
	home      string
	cfgPath   string
	cfg       *config.Config
	log       *slog.Logger
	getenv    func(string) string
	overrides config.Overrides
	prompt    string
	yolo      bool
	envName   string

	servers *mcp.Manager
	res     config.Resolved
	p       provider.Provider
	env     execenv.Env
	defEnv  string
	rt      *app.Runtime
}

// infof reports progress in the longtime form: one line, no prefix. It exists
// because slog has no Printf; new messages should use log.Info directly.
func (c *cli) infof(format string, args ...any) {
	c.log.Info(fmt.Sprintf(format, args...))
}

// command is one `tal` subcommand. needsConfig reads the config file first
// (before it, only version, restore and config can run); needsModel
// additionally resolves the provider, starts the environment, opens the vault
// and the connected services, and builds the runtime.
type command struct {
	name        string
	hint        string // "tal "+hint in the unknown-command list; "" leaves it out
	needsConfig bool
	needsModel  bool
	run         func(c *cli, args []string) error
}

// commands is every subcommand, in the order the unknown-command error lists
// them. The empty name is the default: bare `tal` starts the interactive chat.
var commands = []command{
	{name: "gateway", hint: "gateway", needsConfig: true, needsModel: true, run: (*cli).runGatewayCmd},
	{name: "cron", hint: "cron list", needsConfig: true, run: (*cli).runCronCmd},
	{name: "mcp", hint: "mcp list", needsConfig: true, run: (*cli).runMCPCmd},
	{name: "env", hint: "env", needsConfig: true, run: (*cli).runEnvCmd},
	{name: "log", hint: "log", needsConfig: true, run: (*cli).runLogCmd},
	{name: "policy", hint: "policy", needsConfig: true, run: (*cli).runPolicyCmd},
	{name: "workspace", hint: "workspace", needsConfig: true, run: (*cli).runWorkspaceCmd},
	{name: "vault", hint: "vault", needsConfig: true, run: (*cli).runVaultCmd},
	{name: "connect", hint: "connect", needsConfig: true, run: (*cli).runConnectCmd},
	{name: "disconnect", needsConfig: true, run: (*cli).runDisconnectCmd},
	{name: "backup", hint: "backup", needsConfig: true, run: (*cli).runBackupCmd},
	{name: "restore", hint: "restore", run: (*cli).runRestoreCmd},
	{name: "doctor", hint: "doctor", needsConfig: true, run: (*cli).runDoctorCmd},
	{name: "calendar", hint: "calendar", needsConfig: true, run: (*cli).runCalendarCmd},
	{name: "email", hint: "email", needsConfig: true, run: (*cli).runEmailCmd},
	{name: "version", hint: "version", run: (*cli).runVersionCmd},
	{name: "config", hint: "config", run: (*cli).runConfigCmd},
	{name: "", needsConfig: true, needsModel: true, run: (*cli).runCLICmd},
}

func findCommand(name string) *command {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

// commandHints lists every command for the unknown-command error, from the
// table itself so the two cannot drift apart.
func commandHints() string {
	var hints []string
	for _, cmd := range commands {
		if cmd.hint != "" {
			hints = append(hints, "tal "+cmd.hint)
		}
	}
	return strings.Join(hints, ", ")
}

func run() error {
	var (
		profile = flag.String("profile", os.Getenv("TALARIA_PROFILE"), "provider profile from the config file")
		model   = flag.String("model", os.Getenv("TALARIA_MODEL"), "model name (overrides the profile)")
		baseURL = flag.String("base-url", os.Getenv("TALARIA_BASE_URL"), "API base URL (overrides the profile)")
		prompt  = flag.String("p", "", "run one prompt and exit")
		yolo    = flag.Bool("yolo", false, "run write_file, shell and skill changes without asking")
		envName = flag.String("env", os.Getenv("TALARIA_ENV"), "where the shell and file tools run: an environment from the config file (default: this machine)")
	)
	flag.Parse()

	log := newLogger(os.Stderr, os.Getenv)
	home, err := app.HomeDir(os.Getenv)
	if err != nil {
		return err
	}
	c := &cli{
		home: home, cfgPath: config.Path(home, os.Getenv), log: log, getenv: os.Getenv,
		overrides: config.Overrides{Profile: *profile, Model: *model, BaseURL: *baseURL, APIKey: os.Getenv("TALARIA_API_KEY"), Type: os.Getenv("TALARIA_PROVIDER")},
		prompt:    *prompt, yolo: *yolo, envName: *envName,
	}
	cmd := findCommand(flag.Arg(0))
	if cmd == nil {
		return fmt.Errorf("unknown command %q (try: %s)", flag.Arg(0), commandHints())
	}
	if cmd.needsConfig {
		cfg, err := config.Load(c.cfgPath)
		if err != nil {
			return err
		}
		c.cfg = cfg
		for _, w := range cfg.Warnings {
			c.log.Warn(w)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM) // SIGTERM too, so a stopped service still removes its container
	defer stop()
	c.ctx = ctx
	if cmd.needsModel {
		if err := c.setupModel(cmd.name == "gateway"); err != nil {
			return err
		}
		defer c.closeModel()
	}
	return cmd.run(c, flag.Args()[1:])
}

func (c *cli) setupModel(gateway bool) (err error) {
	res, err := c.cfg.Resolve(c.overrides, c.getenv)
	if err != nil {
		return err
	}
	if err := res.CheckKey(); err != nil {
		return err
	}
	p, err := provider.New(res.Spec())
	if err != nil {
		return err
	}
	// Where the shell and file tools run. A container or connection that cannot
	// be set up stops us here: falling back to this machine would run things
	// somewhere the user chose not to.
	env, defaultEnvName, err := c.openEnvironment()
	if err != nil {
		return err
	}
	c.env, c.defEnv = env, defaultEnvName
	defer func() {
		if err != nil { // a half-built setup still stops what it started
			c.closeModel()
		}
	}()
	guarded, err := buildGuard(c.cfg, c.home, c.getenv, os.Environ())
	if err != nil {
		return err
	}
	// Saved logins and sign-ins, if there is a vault and it can be opened. Only the
	// interactive command line may ask for its passphrase; a gateway has to be given it.
	var ask prompter
	if !gateway && isTerminal(os.Stdin) {
		ask = newTerminal()
	}
	vlt := loadVault(c.cfg, c.home, guarded, ask, c.log)
	// Connected services, which may need the vault for their sign-ins.
	c.servers = startMCP(c.ctx, c.cfg, vlt, c.log)
	guarded.Policy.NeverRemember(c.servers.Sensitive()...) // the Always button is never offered for a tool that sends or deletes
	calSvc, calProblems := buildCalendars(c.cfg, c.getenv, c.home)
	for _, p := range calProblems {
		c.log.Warn(p)
	}
	mailSvc, mailProblems := buildMailbox(c.cfg, c.getenv)
	for _, p := range mailProblems {
		c.log.Warn(p)
	}
	appCfg := app.Config{Home: c.home, Provider: p, Model: res.Model, MCP: c.servers, Subagents: c.cfg.Subagents, Env: env, Guard: guarded, Fence: buildFence(c.cfg, c.cfgPath, c.home), Calendars: calSvc, Mail: mailSvc, TrustOutsideContent: !c.cfg.Policy.GuardUntrusted(), ModelCallsPerHour: c.cfg.Limits.ModelCalls(), Log: c.log,
		Browser: browserConfig(c.cfg), Goals: &goal.Store{Path: filepath.Join(c.home, "goals.json")},
		Watches: &watch.Store{Path: filepath.Join(c.home, "watches.json")}}
	if vlt != nil {
		appCfg.Vault = vlt
	}
	c.res, c.p = res, p
	c.rt = app.Open(appCfg)
	return nil
}

// closeModel stops the runtime, the connected services and the environment, in
// that order. It is safe to call twice.
func (c *cli) closeModel() {
	if c.rt != nil {
		c.rt.Close()
		c.rt = nil
	}
	if c.servers != nil {
		c.servers.Close()
		c.servers = nil
	}
	if c.env != nil {
		c.env.Close()
		c.env = nil
	}
}

func (c *cli) runVersionCmd(args []string) error {
	fmt.Println("talaria", version.String())
	return nil
}

func (c *cli) runRestoreCmd(args []string) error {
	// Before the config is read: a damaged config is a reason to restore.
	return runRestore(c.home, args, os.Stdout)
}

func (c *cli) runConfigCmd(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	return runConfig(c.cfgPath, sub, c.overrides)
}

func (c *cli) runCronCmd(args []string) error {
	if len(args) > 0 && args[0] != "list" {
		return fmt.Errorf("unknown cron command %q (try: tal cron list)", args[0])
	}
	rt := app.Open(app.Config{Home: c.home, Log: c.log})
	defer rt.Close()
	return runCronList(rt)
}

func (c *cli) runLogCmd(args []string) error {
	return runLog(c.home, args)
}

func (c *cli) runPolicyCmd(args []string) error {
	return runPolicy(c.cfg, c.cfgPath, c.home, args)
}

func (c *cli) runConnectCmd(args []string) error {
	return c.connect(args, false)
}

func (c *cli) runDisconnectCmd(args []string) error {
	return c.connect(args, true)
}

func (c *cli) connect(args []string, disconnect bool) error {
	var ask prompter
	if isTerminal(os.Stdin) {
		ask = newTerminal()
	}
	if disconnect {
		args = append([]string{"disconnect"}, args...)
	}
	return runConnect(c.ctx, c.cfg, c.home, args, ask, os.Stdout, realConnectDeps)
}

func (c *cli) runCalendarCmd(args []string) error {
	return runCalendar(c.ctx, c.cfg, c.getenv, c.home, args, os.Stdout)
}

func (c *cli) runEmailCmd(args []string) error {
	return runEmail(c.ctx, c.cfg, c.getenv, args, os.Stdout)
}

func (c *cli) runDoctorCmd(args []string) error {
	if runDoctor(c.cfg, c.cfgPath, c.home, c.getenv, os.Stdout) > 0 {
		return exitError{code: 1}
	}
	return nil
}

func (c *cli) runBackupCmd(args []string) error {
	return runBackup(c.cfg, c.home, args, os.Stdout)
}

func (c *cli) runVaultCmd(args []string) error {
	return runVault(c.cfg, c.home, args, newTerminal(), os.Stdout)
}

func (c *cli) runWorkspaceCmd(args []string) error {
	return runWorkspace(c.ctx, c.cfg, args)
}

func (c *cli) runEnvCmd(args []string) error {
	sub, arg := "", ""
	if len(args) > 0 {
		sub = args[0]
	}
	if len(args) > 1 {
		arg = args[1]
	}
	return runEnv(c.ctx, c.cfg, c.cfgPath, c.envName, sub, arg)
}

func (c *cli) runMCPCmd(args []string) error {
	if len(args) > 0 && args[0] != "list" {
		return fmt.Errorf("unknown mcp command %q (try: tal mcp list)", args[0])
	}
	return runMCPList(c.ctx, c.cfg, c.cfgPath, c.home)
}

func (c *cli) runCLICmd(args []string) error {
	return runCLI(c.ctx, c.rt, c.res, c.prompt, c.yolo)
}

func (c *cli) runGatewayCmd(args []string) error {
	return runGateway(c)
}

// openEnvironment starts the environment the shell and file tools will use.
// It returns nil for this machine, which needs no setup.
func (c *cli) openEnvironment() (execenv.Env, string, error) {
	name, spec, err := c.cfg.Environment(c.envName)
	if err != nil {
		return nil, "", err
	}
	if spec == nil {
		return nil, name, nil
	}
	c.infof("environment %s: starting (%s)", name, spec.Summary()) // a first image pull can take a while
	env, err := execenv.Open(c.ctx, name, *spec, c.getenv)
	if err != nil {
		return nil, "", err
	}
	c.infof("environment %s: the shell and file tools run in %s", name, env.Describe())
	return env, name, nil
}

// runEnv handles `tal env [list]` and `tal env check [name]`.
func runEnv(ctx context.Context, cfg *config.Config, cfgPath, selected, sub, arg string) error {
	switch sub {
	case "", "list":
		current, _, err := cfg.Environment(selected)
		if err != nil {
			return err
		}
		mark := func(name string) string {
			if name == current {
				return "  (selected)"
			}
			return ""
		}
		fmt.Printf("%-16s this machine (built in)%s%s\n", execenv.LocalName, mark(execenv.LocalName), switchableBy(cfg, execenv.LocalName))
		defined := 0
		for _, name := range cfg.EnvironmentNames() {
			note := ""
			if s := cfg.Environments[name]; name == execenv.WorkspaceName && s.Container == execenv.WorkspaceContainer && s.Type == "docker" {
				note = "  (built in: set it up with `tal workspace up`)"
			} else {
				defined++
			}
			fmt.Printf("%-16s %s%s%s%s\n", name, cfg.Environments[name].Summary(), mark(name), switchableBy(cfg, name), note)
		}
		if defined == 0 {
			fmt.Printf("\nno other environments are defined; add some under \"environments\" in %s\n", cfgPath)
		}
		return nil
	case "check":
		name, spec, err := cfg.Environment(config.First(arg, selected))
		if err != nil {
			return err
		}
		var env execenv.Env = execenv.Local{}
		if spec != nil {
			fmt.Printf("starting %s (%s)...\n", name, spec.Summary())
			if env, err = execenv.Open(ctx, name, *spec, os.Getenv); err != nil {
				return err
			}
			defer env.Close()
		}
		fmt.Printf("%s: %s\n\n", name, env.Describe())
		res, err := env.Shell(ctx, "uname -sm; pwd; id -un 2>/dev/null; true", 30*time.Second)
		if err != nil {
			return err
		}
		fmt.Print(res.Output)
		if res.TimedOut {
			return errors.New("the check command timed out")
		}
		fmt.Println("\nok: commands run there")
		return nil
	}
	return fmt.Errorf("unknown env command %q (try: tal env, tal env check [name])", sub)
}

// startMCP connects to the configured MCP servers, reporting each one.
// A server that fails is skipped; the rest still work.
func startMCP(ctx context.Context, cfg *config.Config, v *vault.Vault, log *slog.Logger) *mcp.Manager {
	if len(cfg.MCPServers) == 0 {
		return nil
	}
	mgr := mcp.StartAuth(ctx, cfg.MCPServers, os.Getenv, mcpAuth(cfg, v))
	for _, st := range mgr.Status() {
		switch {
		case st.Disabled:
		case st.Err != nil:
			log.Warn(fmt.Sprintf("mcp server %s failed to start: %v", st.Name, st.Err))
		default:
			log.Info(fmt.Sprintf("mcp: %s ready, %d tools", st.Name, len(st.Tools)))
		}
		for _, w := range st.Warnings {
			log.Warn(fmt.Sprintf("mcp server %s: %s", st.Name, w))
		}
	}
	return mgr
}

// runMCPList connects to every configured server and prints what it offers.
func runMCPList(ctx context.Context, cfg *config.Config, cfgPath, home string) error {
	if len(cfg.MCPServers) == 0 {
		fmt.Printf("no MCP servers configured (add them under \"mcp_servers\" in %s)\n", cfgPath)
		return nil
	}
	var v *vault.Vault
	if len(oauthServers(cfg)) > 0 {
		var ask prompter
		if isTerminal(os.Stdin) {
			ask = newTerminal()
		}
		v, _ = openVault(cfg, home, ask, nil)
	}
	mgr := mcp.StartAuth(ctx, cfg.MCPServers, os.Getenv, mcpAuth(cfg, v))
	defer mgr.Close()

	for i, st := range mgr.Status() {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s  (%s)\n", st.Name, st.Transport)
		switch {
		case st.Disabled:
			fmt.Println("  disabled")
		case st.Err != nil:
			fmt.Println("  FAILED:", st.Err)
		default:
			fmt.Printf("  connected, protocol %s, %d tools\n", st.Protocol, len(st.Tools))
		}
		for _, w := range st.Warnings {
			fmt.Println("  warning:", w)
		}
		for _, t := range st.Tools {
			policy := "auto"
			if t.NeedsApproval {
				policy = "asks"
			}
			note := ""
			if t.Class.Sensitive() {
				note = " [never remembered]"
			}
			fmt.Printf("  %-34s %-5s %-7s %s%s\n", t.Exposed, policy, t.Class, mcp.FirstLine(t.Description, 60), note)
		}
	}
	return nil
}

// runConfig handles `tal config [show|path|init]`.
func runConfig(path, sub string, overrides config.Overrides) error {
	switch sub {
	case "path":
		fmt.Println(path)
		return nil
	case "init":
		if err := config.Init(path); err != nil {
			return err
		}
		fmt.Printf("wrote %s\nEdit it, then check the result with: tal config show\n", path)
		return nil
	case "", "show":
		return showConfig(path, overrides)
	}
	return fmt.Errorf("unknown config command %q (try: tal config show, tal config init, tal config path)", sub)
}

// showConfig prints how the current settings resolve, without ever printing a key.
func showConfig(path string, overrides config.Overrides) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		fmt.Printf("config file: %s (not found: using environment variables only; create one with: tal config init)\n", path)
	} else {
		fmt.Printf("config file: %s\n", path)
	}
	for _, w := range cfg.Warnings {
		fmt.Println("warning:", w)
	}
	if names := cfg.ProfileNames(); len(names) > 0 {
		fmt.Println("profiles:   ", strings.Join(names, ", "))
	}
	if names := cfg.EnvironmentNames(); len(names) > 0 {
		fmt.Println("environments:", strings.Join(names, ", "), "(see: tal env)")
	}
	if names := cfg.MCPServerNames(); len(names) > 0 {
		fmt.Println("mcp servers:", strings.Join(names, ", "), "(see: tal mcp list)")
	}

	r, err := cfg.Resolve(overrides, os.Getenv)
	if err != nil {
		return err
	}
	key := r.KeySource
	if r.APIKey == "" {
		key = "none"
	}
	fmt.Printf("\nselected profile: %s\n  type:     %s\n  base url: %s\n  model:    %s\n  api key:  %s\n", r.Name, r.Type, r.BaseURL, r.Model, key)
	if r.MaxTokens > 0 {
		fmt.Printf("  max tokens: %d\n", r.MaxTokens)
	}
	if r.Type == provider.TypeDeepSeek {
		fmt.Printf("  thinking: %s, reasoning effort: %s, images: %s\n", config.First(r.Thinking, "service default (on)"), config.First(r.ReasoningEffort, "service default"), map[bool]string{true: "not sent", false: "sent"}[r.NoVision])
	}
	if err := r.CheckKey(); err != nil {
		fmt.Println("\nproblem:", err)
	}
	return nil
}

func runCLI(ctx context.Context, rt *app.Runtime, res config.Resolved, prompt string, yolo bool) error {
	in := bufio.NewReader(os.Stdin)

	// Replies print as they are generated. Parallel subagents may report progress
	// or ask for approval at the same moment, so the output state is guarded and
	// questions are asked one at a time. midLine tracks whether the cursor is
	// partway along a line, so messages on stderr start on a fresh one.
	var (
		outMu, askMu      sync.Mutex
		streamed, midLine bool
	)
	endLine := func() { // the caller holds outMu
		if midLine {
			fmt.Println()
			midLine = false
		}
	}
	var scope *guard.Scope // set below, once the agent exists; nil when there is no guard
	approve := func(name, args string) bool {
		if yolo {
			return true
		}
		askMu.Lock()
		defer askMu.Unlock()
		outMu.Lock()
		endLine()
		outMu.Unlock()
		tool := agent.BaseTool(name)
		always := scope != nil && scope.Rememberable(tool) && !agent.IsFlagged(name)
		choices := "[y/N]"
		if always {
			choices = "[y/N/a=always]"
		}
		// All of the arguments, with nothing in them able to redraw the line: what is on screen must be what will run.
		fmt.Fprintf(os.Stderr, "\nallow %s?\n%s\n%s ", name, indent(agent.ApprovalText(args)), choices)
		line, _ := in.ReadString('\n')
		switch answer := strings.ToLower(strings.TrimSpace(line)); {
		case always && (answer == "a" || answer == "always"):
			if err := scope.Remember(tool); err != nil {
				fmt.Fprintf(os.Stderr, "could not keep that choice (%v); asking again next time\n", err)
			}
			return true
		default:
			yes := strings.HasPrefix(answer, "y")
			if yes && scope != nil && !agent.IsFlagged(name) {
				scope.RememberSession(tool) // one yes covers shell and write_file for the rest of the run
			}
			return yes
		}
	}
	a := rt.NewAgent(app.AgentOptions{Approve: approve})
	scope, _ = a.Gate.(*guard.Scope)
	a.OnText = func(s string) {
		outMu.Lock()
		defer outMu.Unlock()
		fmt.Print(s)
		streamed = true
		midLine = !strings.HasSuffix(s, "\n")
	}
	a.OnToolStart = func(name, args string) { // shown as the tool starts, so a long one is not silent
		outMu.Lock()
		defer outMu.Unlock()
		endLine()
		fmt.Fprintf(os.Stderr, "· %s %s\n", name, oneLine(args, 100))
	}
	ask := func(text string) error {
		outMu.Lock()
		streamed = false
		outMu.Unlock()
		reply, err := a.Run(ctx, text)
		outMu.Lock()
		endLine()
		wasStreamed := streamed
		outMu.Unlock()
		if err != nil {
			return err
		}
		if !wasStreamed { // a provider that does not stream still gets its reply shown
			fmt.Println(reply)
		}
		return nil
	}

	if prompt != "" {
		return ask(prompt)
	}

	fmt.Printf("Talaria (%s, %s). Ctrl-D to exit.\n", res.Name, res.Model)
	type input struct {
		line string
		err  error
	}
	for {
		fmt.Print("\n› ")
		// Read in the background so Ctrl-C or SIGTERM ends the session at the prompt:
		// a read that is waiting for a key cannot be interrupted any other way.
		got := make(chan input, 1)
		go func() {
			line, err := in.ReadString('\n')
			got <- input{line, err}
		}()
		var next input
		select {
		case next = <-got:
		case <-ctx.Done():
			fmt.Println()
			return nil
		}
		if text := strings.TrimSpace(next.line); text != "" {
			if runErr := ask(text); runErr != nil {
				fmt.Fprintln(os.Stderr, "error:", runErr)
			}
		}
		if next.err != nil || ctx.Err() != nil {
			return nil
		}
	}
}

const telegramNote = `You are chatting with the user through Telegram. Replies are shown as plain text with no markdown rendering, so keep them short and avoid markdown syntax. Your file and shell tools run on the machine hosting you, not on the user's phone. Risky tool calls ask the user to approve with a button. You can schedule one-off and recurring jobs with cron_add.`

const webNote = `You are chatting with the user in Talaria's own web app on their phone. Replies are rendered as Markdown: use short paragraphs, lists and code blocks, and avoid tables and large headings. Your file and shell tools run on the machine hosting you, not on the user's phone. Risky tool calls show the user a card with Allow and Deny buttons. You can schedule one-off and recurring jobs with cron_add, and they are delivered to this chat, with a notification if the app is closed.`

const discordNote = `You are chatting with the user through Discord direct messages. Replies are rendered as Markdown and each message holds at most 2000 characters, so keep replies short, use code blocks for code and commands, and avoid headings and tables. Your file and shell tools run on the machine hosting you, not on the user's device. Risky tool calls ask the user to approve with buttons. You can schedule one-off and recurring jobs with cron_add.`

const cronNote = `You are running as a scheduled job with nobody watching, so you cannot ask questions. Your final reply is delivered to the user's chat as plain text: keep it short. If there is nothing worth reporting, reply with exactly [SILENT]. Tools that need approval are denied unless this job was created with allow_risky; if one is denied, say what you could not do.`

const cronNoteMarkdown = `You are running as a scheduled job with nobody watching, so you cannot ask questions. Your final reply is delivered to the user's chat, which renders Markdown: keep it short. If there is nothing worth reporting, reply with exactly [SILENT]. Tools that need approval are denied unless this job was created with allow_risky; if one is denied, say what you could not do.`

// gatewayBackends offers every configured profile to chats. The
// profile in use at startup keeps the provider already built for it; the others
// are opened on first use, and one that cannot be used (a missing API key, say)
// reports why instead of failing the gateway.
func gatewayBackends(cfg *config.Config, def config.Resolved, defProvider provider.Provider, getenv func(string) string) *chat.Backends {
	describe := func(r config.Resolved) string { return r.Type + ", " + r.Model }

	var mu sync.Mutex
	listProfiles := func() []string {
		names := cfg.ProfileNames()
		if !slices.Contains(names, def.Name) { // the implicit "default" profile of a config-less setup
			names = append(names, def.Name)
		}
		slices.Sort(names)
		return names
	}
	// traits says what a resolved profile can do, so the router can pick it.
	traits := func(name string, r config.Resolved) (vision, reasoning bool, best []string) {
		return !r.NoVision, r.Type == provider.TypeDeepSeek, cfg.Profiles[name].BestFor
	}
	build := func(name, effort string) (chat.Backend, error) {
		r, err := cfg.Resolve(config.Overrides{Profile: name}, getenv)
		if err != nil {
			return chat.Backend{}, err
		}
		if err := r.CheckKey(); err != nil {
			return chat.Backend{}, err
		}
		spec := r.Spec()
		if effort != "" && r.Type == provider.TypeDeepSeek {
			spec.ReasoningEffort = effort
		}
		p, err := provider.New(spec)
		if err != nil {
			return chat.Backend{}, err
		}
		vision, reasoning, best := traits(name, r)
		return chat.Backend{Provider: p, Model: r.Model, Desc: describe(r), Vision: vision, Reasoning: reasoning, Best: best}, nil
	}
	// The default keeps the provider already built for it, which may carry
	// startup overrides; profiles edited in Settings rebuild from config.
	cache := map[string]chat.Backend{
		def.Name: {Provider: defProvider, Model: def.Model, Desc: describe(def),
			Vision: !def.NoVision, Reasoning: def.Type == provider.TypeDeepSeek, Best: cfg.Profiles[def.Name].BestFor},
	}

	return &chat.Backends{
		Default: def.Name,
		Names:   listProfiles(),
		List:    listProfiles,
		Open: func(name string) (chat.Backend, error) {
			mu.Lock()
			defer mu.Unlock()
			if b, ok := cache[name]; ok {
				return b, nil
			}
			b, err := build(name, "")
			if err != nil {
				return chat.Backend{}, err
			}
			cache[name] = b
			return b, nil
		},
		OpenEffort: func(name, effort string) (chat.Backend, error) {
			mu.Lock()
			defer mu.Unlock()
			return build(name, effort)
		},
		Forget: func() {
			mu.Lock()
			defer mu.Unlock()
			cache = map[string]chat.Backend{}
		},
	}
}

// runCronList prints every scheduled job. Jobs are created and removed by the
// agent through the gateway; this is a read-only view.
func runCronList(rt *app.Runtime) error {
	jobs, err := rt.Cron.List()
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Println("no scheduled jobs")
	}
	for _, j := range jobs {
		fmt.Printf("%s | %s chat %d\n", cron.Describe(j), j.Gateway, j.ChatID)
	}
	return nil
}

// parseIDs reads a comma-separated list of user IDs.
func parseIDs(s string) (map[int64]bool, error) {
	ids := map[int64]bool{}
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		id, err := config.ParseUserID(f)
		if err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, nil
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// indent sets a multi-line block off from the question it belongs to.
func indent(s string) string { return "    " + strings.ReplaceAll(s, "\n", "\n    ") }
