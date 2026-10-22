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
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
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
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
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

	home, err := app.HomeDir()
	if err != nil {
		return err
	}
	cfgPath := config.Path(home)
	overrides := config.Overrides{Profile: *profile, Model: *model, BaseURL: *baseURL, APIKey: os.Getenv("TALARIA_API_KEY"), Type: os.Getenv("TALARIA_PROVIDER")}

	cmd := flag.Arg(0)
	if cmd == "version" {
		fmt.Println("talaria", version.String())
		return nil
	}
	if cmd == "restore" { // before the config is read: a damaged config is a reason to restore
		return runRestore(home, flag.Args()[1:], os.Stdout)
	}
	if cmd == "config" {
		return runConfig(cfgPath, flag.Arg(1), overrides)
	}

	warn := func(format string, args ...any) { fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...) }
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	for _, w := range cfg.Warnings {
		warn("%s", w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM) // SIGTERM too, so a stopped service still removes its container
	defer stop()

	switch cmd {
	case "cron": // read-only, so no model or API key is needed
		if sub := flag.Arg(1); sub != "" && sub != "list" {
			return fmt.Errorf("unknown cron command %q (try: tal cron list)", sub)
		}
		rt := app.Open(app.Config{Home: home}, warn)
		defer rt.Close()
		return runCronList(rt)
	case "log": // needs no model or API key
		return runLog(home, flag.Args()[1:])
	case "policy": // needs no model or API key
		return runPolicy(cfg, cfgPath, home, flag.Args()[1:])
	case "connect", "disconnect": // needs no model or API key
		var ask prompter
		if isTerminal(os.Stdin) {
			ask = newTerminal()
		}
		args := flag.Args()
		if cmd == "disconnect" {
			args = append([]string{"disconnect"}, args[1:]...)
		} else {
			args = args[1:]
		}
		return runConnect(ctx, cfg, home, args, ask, os.Stdout, realConnectDeps)
	case "calendar": // needs no model or API key
		return runCalendar(ctx, cfg, os.Getenv, flag.Args()[1:], os.Stdout)
	case "email": // needs no model or API key
		return runEmail(ctx, cfg, os.Getenv, flag.Args()[1:], os.Stdout)
	case "doctor": // needs no model or API key
		if runDoctor(cfg, cfgPath, home, os.Getenv, os.Stdout) > 0 {
			os.Exit(1)
		}
		return nil
	case "backup": // needs no model or API key
		return runBackup(cfg, home, flag.Args()[1:], os.Stdout)
	case "vault": // needs no model or API key
		return runVault(cfg, home, flag.Args()[1:], newTerminal(), os.Stdout)
	case "workspace": // needs no model or API key
		return runWorkspace(ctx, cfg, flag.Args()[1:])
	case "env": // needs no model or API key
		return runEnv(ctx, cfg, cfgPath, *envName, flag.Arg(1), flag.Arg(2))
	case "mcp": // needs no model or API key
		if sub := flag.Arg(1); sub != "" && sub != "list" {
			return fmt.Errorf("unknown mcp command %q (try: tal mcp list)", sub)
		}
		return runMCPList(ctx, cfg, cfgPath, home)
	case "", "gateway":
	default:
		return fmt.Errorf("unknown command %q (try: tal gateway, tal cron list, tal mcp list, tal env, tal log, tal policy, tal workspace, tal vault, tal connect, tal backup, tal restore, tal doctor, tal calendar, tal email, tal version, tal config)", cmd)
	}

	res, err := cfg.Resolve(overrides, os.Getenv)
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
	env, defaultEnvName, err := openEnvironment(ctx, cfg, *envName)
	if err != nil {
		return err
	}
	if env != nil {
		defer env.Close()
	}
	guarded, err := buildGuard(cfg, home, os.Getenv, os.Environ())
	if err != nil {
		return err
	}
	// Saved logins and sign-ins, if there is a vault and it can be opened. Only the
	// interactive command line may ask for its passphrase; a gateway has to be given it.
	var ask prompter
	if cmd != "gateway" && isTerminal(os.Stdin) {
		ask = newTerminal()
	}
	vlt := loadVault(cfg, home, guarded, ask, warn)
	// Connected services, which may need the vault for their sign-ins.
	mcpServers := startMCP(ctx, cfg, vlt)
	defer mcpServers.Close()
	guarded.Policy.NeverRemember(mcpServers.Sensitive()...) // the Always button is never offered for a tool that sends or deletes
	calSvc, calProblems := buildCalendars(cfg, os.Getenv)
	for _, p := range calProblems {
		warn("%s", p)
	}
	mailSvc, mailProblems := buildMailbox(cfg, os.Getenv)
	for _, p := range mailProblems {
		warn("%s", p)
	}
	appCfg := app.Config{Home: home, Provider: p, Model: res.Model, MCP: mcpServers, Subagents: cfg.Subagents, Env: env, Guard: guarded, Fence: buildFence(cfg, cfgPath, home), Calendars: calSvc, Mail: mailSvc, TrustOutsideContent: !cfg.Policy.GuardUntrusted(), ModelCallsPerHour: cfg.Limits.ModelCalls(),
		Browser: browserConfig(cfg), Goals: &goal.Store{Path: filepath.Join(home, "goals.json")},
		Watches: &watch.Store{Path: filepath.Join(home, "watches.json")}}
	if vlt != nil {
		appCfg.Vault = vlt
	}
	rt := app.Open(appCfg, warn)
	defer rt.Close()

	if cmd == "gateway" {
		return runGateway(ctx, rt, cfg, res, p, home, env, defaultEnvName, *yolo)
	}
	return runCLI(ctx, rt, res, *prompt, *yolo)
}

// openEnvironment starts the environment the shell and file tools will use.
// It returns nil for this machine, which needs no setup.
func openEnvironment(ctx context.Context, cfg *config.Config, selected string) (execenv.Env, string, error) {
	name, spec, err := cfg.Environment(selected)
	if err != nil {
		return nil, "", err
	}
	if spec == nil {
		return nil, name, nil
	}
	fmt.Fprintf(os.Stderr, "environment %s: starting (%s)\n", name, spec.Summary()) // a first image pull can take a while
	env, err := execenv.Open(ctx, name, *spec, os.Getenv)
	if err != nil {
		return nil, "", err
	}
	fmt.Fprintf(os.Stderr, "environment %s: the shell and file tools run in %s\n", name, env.Describe())
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
		name, spec, err := cfg.Environment(firstNonEmpty(arg, selected))
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// startMCP connects to the configured MCP servers, reporting each one on
// stderr. A server that fails is skipped; the rest still work.
func startMCP(ctx context.Context, cfg *config.Config, v *vault.Vault) *mcp.Manager {
	if len(cfg.MCPServers) == 0 {
		return nil
	}
	mgr := mcp.StartAuth(ctx, cfg.MCPServers, os.Getenv, mcpAuth(cfg, v))
	for _, st := range mgr.Status() {
		switch {
		case st.Disabled:
		case st.Err != nil:
			fmt.Fprintf(os.Stderr, "warning: mcp server %s failed to start: %v\n", st.Name, st.Err)
		default:
			fmt.Fprintf(os.Stderr, "mcp: %s ready, %d tools\n", st.Name, len(st.Tools))
		}
		for _, w := range st.Warnings {
			fmt.Fprintf(os.Stderr, "warning: mcp server %s: %s\n", st.Name, w)
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
		fmt.Printf("  thinking: %s, reasoning effort: %s, images: %s\n", orDefault(r.Thinking, "service default (on)"), orDefault(r.ReasoningEffort, "service default"), map[bool]string{true: "not sent", false: "sent"}[r.NoVision])
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
			return strings.HasPrefix(answer, "y")
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
	cache := map[string]chat.Backend{
		def.Name: {Provider: defProvider, Model: def.Model, Desc: describe(def)},
	}
	names := cfg.ProfileNames()
	if !slices.Contains(names, def.Name) { // the implicit "default" profile of a config-less setup
		names = append(names, def.Name)
		sort.Strings(names)
	}

	return &chat.Backends{
		Default: def.Name,
		Names:   names,
		Open: func(name string) (chat.Backend, error) {
			mu.Lock()
			defer mu.Unlock()
			if b, ok := cache[name]; ok {
				return b, nil
			}
			r, err := cfg.Resolve(config.Overrides{Profile: name}, getenv)
			if err != nil {
				return chat.Backend{}, err
			}
			if err := r.CheckKey(); err != nil {
				return chat.Backend{}, err
			}
			p, err := provider.New(r.Spec())
			if err != nil {
				return chat.Backend{}, err
			}
			b := chat.Backend{Provider: p, Model: r.Model, Desc: describe(r)}
			cache[name] = b
			return b, nil
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
		id, err := strconv.ParseInt(f, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not a user ID (a positive whole number)", f)
		}
		ids[id] = true
	}
	return ids, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// indent sets a multi-line block off from the question it belongs to.
func indent(s string) string { return "    " + strings.ReplaceAll(s, "\n", "\n    ") }
