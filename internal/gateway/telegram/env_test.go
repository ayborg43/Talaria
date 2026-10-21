package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/agent"
	"talaria/internal/execenv"
	"talaria/internal/gateway/chat"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

// labelEnv is an environment that names itself in everything it lists.
type labelEnv struct {
	execenv.Local
	name string
}

func (e *labelEnv) Describe() string { return "the " + e.name + " box" }
func (e *labelEnv) Remote() bool     { return e.name != "local" }
func (e *labelEnv) ListDir(context.Context, string) ([]string, error) {
	return []string{"in-" + e.name}, nil
}

// envModel asks to list a directory, then reports what the tool returned, so a
// reply shows which environment the tool ran in. It remembers what it was sent.
type envModel struct {
	gate chan struct{} // if set, the first call waits for this to close

	mu    sync.Mutex
	calls int
	users []string
	first sync.Once
}

func (m *envModel) Chat(ctx context.Context, req provider.Request) (provider.Message, error) {
	m.mu.Lock()
	m.calls++
	gate := m.gate
	m.mu.Unlock()
	if gate != nil {
		m.first.Do(func() {
			select {
			case <-gate:
			case <-ctx.Done():
			}
		})
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role == provider.RoleTool {
		return provider.Message{Role: provider.RoleAssistant, Content: "listed: " + strings.TrimSpace(last.Content)}, nil
	}
	m.mu.Lock()
	m.users = append(m.users, last.Content)
	m.mu.Unlock()
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "list_dir", Arguments: `{"path":"/"}`}}}, nil
}

func (m *envModel) modelCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *envModel) userMessages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.users...)
}

// envKit is a gateway's worth of environments, with controls for how they start.
type envKit struct {
	model *envModel
	pool  *execenv.Pool
	def   *labelEnv

	mu      sync.Mutex
	opens   map[string]int
	blocked map[string]chan struct{}
	failing map[string]error
	applied []string
}

func newEnvKit(t *testing.T) *envKit {
	k := &envKit{model: &envModel{}, def: &labelEnv{name: "local"}, opens: map[string]int{}, blocked: map[string]chan struct{}{}, failing: map[string]error{}}
	k.pool = execenv.NewPool(func(ctx context.Context, name string) (execenv.Env, error) {
		k.mu.Lock()
		k.opens[name]++
		gate, err := k.blocked[name], k.failing[name]
		k.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if err != nil {
			return nil, err
		}
		return &labelEnv{name: name}, nil
	})
	k.pool.Put("local", k.def)
	t.Cleanup(k.pool.Close)
	return k
}

func (k *envKit) block(name string) chan struct{} {
	ch := make(chan struct{})
	k.mu.Lock()
	k.blocked[name] = ch
	k.mu.Unlock()
	return ch
}

func (k *envKit) fail(name string, err error) {
	k.mu.Lock()
	k.failing[name] = err
	k.mu.Unlock()
}

func (k *envKit) opened(name string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.opens[name]
}

func (k *envKit) appliedNames() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.applied...)
}

// factory builds agents that start in the default environment.
func (k *envKit) factory() func(int64, func(string, string) bool) *agent.Agent {
	return func(_ int64, approve func(string, string) bool) *agent.Agent {
		reg := tool.NewRegistry()
		tool.RegisterBuiltinsIn(reg, k.def)
		return &agent.Agent{Provider: k.model, Model: "m", Tools: reg, Approve: approve}
	}
}

// with makes a gateway option that offers the given environments to chats.
func (k *envKit) with(names []string, prefs chat.ChatPrefs) func(*Gateway) {
	return func(g *Gateway) {
		g.Prefs = prefs
		g.Environments = &chat.Environments{
			Default: "local",
			Names:   names,
			Summary: func(name string) string { return "summary of " + name },
			Running: k.pool.Running,
			Get:     k.pool.Get,
			Apply: func(a *agent.Agent, env execenv.Env) {
				k.mu.Lock()
				k.applied = append(k.applied, env.(*labelEnv).name)
				k.mu.Unlock()
				tool.RegisterBuiltinsIn(a.Tools, env) // as app.UseEnvironment does
			},
		}
	}
}

func TestEnvListSwitchAndKeepTheConversation(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	start(t, f, []int64{1}, k.factory(), k.with([]string{"local", "sandbox"}, nil))

	say(f, 1, 1, 1, "first")
	f.waitFor("a reply in local", func() bool { return f.count("listed: in-local") == 1 })

	say(f, 2, 1, 1, "/env")
	f.waitFor("the listing", func() bool { return f.sawText("Environments:") })
	var list string
	for _, s := range f.texts() {
		if strings.Contains(s, "Environments:") {
			list = s
		}
	}
	for _, want := range []string{
		"run in: local", "local (summary of local)  <- this chat  [default]", "sandbox (summary of sandbox)", "/env default returns to local",
	} {
		if !strings.Contains(list, want) {
			t.Errorf("listing lacks %q:\n%s", want, list)
		}
	}

	say(f, 3, 1, 1, "/env sandbox")
	f.waitFor("the switch", func() bool { return f.sawText("Switched to sandbox: the sandbox box.") })
	if !f.sawText("Starting sandbox (summary of sandbox)") {
		t.Fatalf("starting it for the first time should be announced: %q", f.texts())
	}
	say(f, 4, 1, 1, "second")
	f.waitFor("a reply in the sandbox", func() bool { return f.count("listed: in-sandbox") == 1 })

	// The conversation carried on, and the model was told its tools moved: once, and not before.
	users := k.model.userMessages()
	if len(users) != 2 || users[0] != "first" {
		t.Fatalf("user messages: %q", users)
	}
	if !strings.HasPrefix(users[1], "[Note for the assistant: the user moved your shell and file tools from the local box to the sandbox box.") || !strings.HasSuffix(users[1], "second") {
		t.Fatalf("the model should be told, in the conversation, that its environment changed: %q", users[1])
	}

	say(f, 5, 1, 1, "third")
	f.waitFor("a second reply in the sandbox", func() bool { return f.count("listed: in-sandbox") == 2 })
	if got := k.model.userMessages()[2]; got != "third" {
		t.Fatalf("the note is given once: %q", got)
	}

	say(f, 6, 1, 1, "/env nope")
	f.waitFor("the refusal", func() bool { return f.sawText(`There is no environment named "nope" that chats may use.`) })
	say(f, 7, 1, 1, "/env sandbox")
	f.waitFor("the no-op", func() bool { return f.sawText("This chat already uses sandbox.") })

	say(f, 8, 1, 1, "/env default")
	f.waitFor("the return", func() bool { return f.sawText("Switched to local: the local box.") })
	say(f, 9, 1, 1, "fourth")
	f.waitFor("back in local", func() bool { return f.count("listed: in-local") == 2 })
	if k.opened("sandbox") != 1 {
		t.Fatalf("the sandbox was started %d times", k.opened("sandbox"))
	}
}

func TestEnvAMessageAfterSwitchWaitsForASlowStart(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	release := k.block("sandbox") // starting takes a while, as pulling an image does
	start(t, f, []int64{1}, k.factory(), k.with([]string{"local", "sandbox"}, nil))

	say(f, 1, 1, 1, "/env sandbox")
	f.waitFor("the starting notice", func() bool { return f.sawText("Starting sandbox") })
	say(f, 2, 1, 1, "run this")

	time.Sleep(300 * time.Millisecond)
	if k.model.modelCalls() != 0 {
		t.Fatal("the message must wait for the environment: running it in the old one would defeat the switch")
	}
	close(release)
	f.waitFor("the reply", func() bool { return f.count("listed: in-sandbox") == 1 })
	if f.count("listed: in-local") != 0 {
		t.Fatal("it must not have run in local")
	}
}

func TestEnvFailedSwitchStaysPut(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	k.fail("sandbox", errors.New("docker is not running"))
	start(t, f, []int64{1}, k.factory(), k.with([]string{"local", "sandbox"}, nil))

	say(f, 1, 1, 1, "/env sandbox")
	f.waitFor("the error", func() bool {
		return f.sawText("Cannot switch to sandbox: docker is not running\nThis chat stays on local.")
	})
	say(f, 2, 1, 1, "hello")
	f.waitFor("a reply in local", func() bool { return f.count("listed: in-local") == 1 })
}

func TestEnvChoiceSurvivesARestart(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	path := filepath.Join(t.TempDir(), "chats.json")
	names := []string{"local", "sandbox"}

	stop := start(t, f, []int64{1}, k.factory(), k.with(names, &chat.FilePrefs{Path: path}))
	say(f, 1, 1, 1, "/env sandbox")
	f.waitFor("the switch", func() bool { return f.sawText("Switched to sandbox") })
	stop()

	start(t, f, []int64{1}, k.factory(), k.with(names, &chat.FilePrefs{Path: path}))
	say(f, 2, 1, 1, "hello again")
	f.waitFor("a reply in the sandbox", func() bool { return f.count("listed: in-sandbox") == 1 })
	if got := k.model.userMessages(); got[len(got)-1] != "hello again" {
		t.Fatalf("a fresh conversation starts out in the right environment, so it needs no note: %q", got)
	}

	// Going back to the default clears it, and leaves the chat's model choice alone.
	(&chat.FilePrefs{Path: path}).SetProfile(1, "claude")
	say(f, 3, 1, 1, "/env default")
	f.waitFor("the return", func() bool { return f.sawText("Switched to local") })
	if p := (&chat.FilePrefs{Path: path}); p.Environment(1) != "" || p.Profile(1) != "claude" {
		t.Fatalf("environment %q, profile %q", p.Environment(1), p.Profile(1))
	}
}

// The environment a chat picked is the one it must get. If it cannot be started,
// the message is not run somewhere else.
func TestEnvSavedChoiceThatCannotStartNeverFallsBack(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	k.fail("sandbox", errors.New("cannot reach the Docker daemon"))
	prefs := &chat.FilePrefs{Path: filepath.Join(t.TempDir(), "chats.json")}
	prefs.SetEnvironment(1, "sandbox")
	start(t, f, []int64{1}, k.factory(), k.with([]string{"local", "sandbox"}, prefs))

	say(f, 1, 1, 1, "delete everything")
	f.waitFor("the explanation", func() bool {
		return f.sawText("it cannot be started now (cannot reach the Docker daemon), so I did not run your message")
	})
	time.Sleep(200 * time.Millisecond)
	if k.model.modelCalls() != 0 {
		t.Fatal("the message must not run anywhere when the chosen environment is unavailable")
	}
	if !f.sawText("use /env default to go back to local") {
		t.Fatalf("the user should be told how to recover: %q", f.texts())
	}

	say(f, 2, 1, 1, "/env default")
	f.waitFor("the switch back", func() bool { return f.sawText("Switched to local") })
	say(f, 3, 1, 1, "try again")
	f.waitFor("a reply in local", func() bool { return f.count("listed: in-local") == 1 })
}

// A saved choice the operator no longer allows is the one case that moves a chat,
// and it moves to the operator's own default.
func TestEnvSavedChoiceThatIsNoLongerAllowedGoesToTheDefault(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	prefs := &chat.FilePrefs{Path: filepath.Join(t.TempDir(), "chats.json")}
	prefs.SetEnvironment(1, "retired")
	start(t, f, []int64{1}, k.factory(), k.with([]string{"local", "sandbox"}, prefs))

	say(f, 1, 1, 1, "hi")
	f.waitFor("the reply", func() bool { return f.count("listed: in-local") == 1 })
	if !f.sawText("The retired environment is no longer available to chats, so this chat is back on local.") {
		t.Fatalf("%q", f.texts())
	}
	if prefs.Environment(1) != "" {
		t.Fatal("the stale choice should be cleared")
	}
	if k.opened("retired") != 0 {
		t.Fatal("an environment that is not allowed must never be started")
	}
}

func TestEnvNotSetUp(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	start(t, f, []int64{1}, k.factory(), func(g *Gateway) { g.Environment = "the build server" })
	say(f, 1, 1, 1, "/env")
	say(f, 2, 1, 1, "/env sandbox")
	say(f, 3, 1, 1, "/help")
	f.waitFor("the help", func() bool { return f.sawText("Talaria is ready") })
	if f.count("Switching environments is not set up. This chat's shell and file tools run in the build server.") != 2 {
		t.Fatalf("both /env forms should explain: %q", f.texts())
	}
	if f.sawText("/env shows") || !f.sawText("Shell and file tools run in the build server.") {
		t.Fatalf("help should not offer a command that does nothing: %q", f.texts())
	}

	// One environment is nothing to choose between.
	f2 := newFake(t)
	k2 := newEnvKit(t)
	start(t, f2, []int64{1}, k2.factory(), k2.with([]string{"local"}, nil))
	say(f2, 1, 1, 1, "/env")
	f2.waitFor("the explanation", func() bool { return f2.sawText("not set up") })

	// With real choices, help offers it.
	f3 := newFake(t)
	k3 := newEnvKit(t)
	start(t, f3, []int64{1}, k3.factory(), k3.with([]string{"local", "sandbox"}, nil))
	say(f3, 1, 1, 1, "/help")
	f3.waitFor("the help", func() bool { return f3.sawText("/env shows or changes where this chat's shell and file tools run.") })
}

func TestEnvIsAllowlisted(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	start(t, f, []int64{1}, k.factory(), k.with([]string{"local", "sandbox"}, nil))
	say(f, 1, 99, 99, "/env sandbox") // a stranger
	say(f, 2, 1, 1, "/whoami")
	f.waitFor("the owner's reply", func() bool { return f.sawText("Your Telegram user ID is 1") })
	if k.opened("sandbox") != 0 || len(f.texts()) != 1 {
		t.Fatalf("a stranger cannot start environments or get answers: opens=%d texts=%q", k.opened("sandbox"), f.texts())
	}
}

// /stop ends the wait, not the start: the environment keeps coming up for next time.
func TestEnvSwitchCanBeStopped(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	release := k.block("sandbox")
	start(t, f, []int64{1}, k.factory(), k.with([]string{"local", "sandbox"}, nil))

	say(f, 1, 1, 1, "/env sandbox")
	f.waitFor("the starting notice", func() bool { return f.sawText("Starting sandbox") })
	say(f, 2, 1, 1, "/stop")
	f.waitFor("the stop", func() bool { return f.sawText("Stopped waiting for sandbox. This chat stays on local.") })
	if f.sawText("Nothing is running.") {
		t.Fatal("stopping a pending switch is not 'nothing'")
	}

	say(f, 3, 1, 1, "still local?")
	f.waitFor("a reply in local", func() bool { return f.count("listed: in-local") == 1 })

	close(release) // it comes up after all
	deadline := time.Now().Add(2 * time.Second)
	for !k.pool.Running("sandbox") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	say(f, 4, 1, 1, "/env sandbox")
	f.waitFor("the instant switch", func() bool { return f.count("Switched to sandbox") == 1 })
	if f.count("Starting sandbox") != 1 {
		t.Fatalf("it is already running, so no second 'starting': %q", f.texts())
	}
}

func TestEnvNewerSwitchReplacesOneStillStarting(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	slow := k.block("slow")
	start(t, f, []int64{1}, k.factory(), k.with([]string{"local", "slow", "quick"}, nil))

	say(f, 1, 1, 1, "/env slow")
	f.waitFor("the starting notice", func() bool { return f.sawText("Starting slow") })
	say(f, 2, 1, 1, "/env quick")
	f.waitFor("the switch to quick", func() bool { return f.sawText("Switched to quick") })
	close(slow)
	time.Sleep(200 * time.Millisecond)
	if f.sawText("Switched to slow") {
		t.Fatal("a replaced request must not report or commit")
	}
	say(f, 3, 1, 1, "go")
	f.waitFor("a reply in quick", func() bool { return f.count("listed: in-quick") == 1 })
}

// Conversations come and go; where a chat's tools run does not.
func TestEnvChoiceSurvivesNewConversations(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	start(t, f, []int64{1}, k.factory(), k.with([]string{"local", "sandbox"}, nil))

	say(f, 1, 1, 1, "/env sandbox")
	f.waitFor("the switch", func() bool { return f.sawText("Switched to sandbox") })
	say(f, 2, 1, 1, "one")
	f.waitFor("a reply in the sandbox", func() bool { return f.count("listed: in-sandbox") == 1 })
	say(f, 3, 1, 1, "/new")
	f.waitFor("the new conversation", func() bool { return f.sawText("Started a new conversation.") })
	say(f, 4, 1, 1, "two")
	f.waitFor("a reply in the sandbox again", func() bool { return f.count("listed: in-sandbox") == 2 })

	if got := k.appliedNames(); strings.Join(got, ",") != "sandbox,sandbox" {
		t.Fatalf("each new agent must be moved to the chat's environment: %v", got)
	}
	if users := k.model.userMessages(); users[len(users)-1] != "two" {
		t.Fatalf("a brand-new conversation needs no note about a change: %q", users)
	}
}

func TestEnvSwitchDuringATaskDoesNotDisturbIt(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	k.model.gate = make(chan struct{})
	start(t, f, []int64{1}, k.factory(), k.with([]string{"local", "sandbox"}, nil))

	say(f, 1, 1, 1, "slow task")
	f.waitFor("the task to start", func() bool { return k.model.modelCalls() == 1 })
	say(f, 2, 1, 1, "/env sandbox")
	f.waitFor("the switch", func() bool { return f.sawText("Switched to sandbox") })
	close(k.model.gate)

	f.waitFor("the running task to finish where it began", func() bool { return f.count("listed: in-local") == 1 })
	say(f, 3, 1, 1, "next")
	f.waitFor("the next task in the sandbox", func() bool { return f.count("listed: in-sandbox") == 1 })
}

func TestEnvApprovalPromptSaysWhereACommandWillRun(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.txt")
	args, _ := json.Marshal(map[string]string{"path": target, "content": "x"})
	writer := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		if req.Messages[len(req.Messages)-1].Role == provider.RoleTool {
			return provider.Message{Role: provider.RoleAssistant, Content: "done"}, nil
		}
		return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Arguments: string(args)}}}, nil
	})

	for _, tc := range []struct {
		env  string
		want string
	}{{"local", "Allow write_file?"}, {"sandbox", "Allow write_file in sandbox?"}} {
		f := newFake(t)
		k := newEnvKit(t)
		k.model = nil
		factory := func(_ int64, approve func(string, string) bool) *agent.Agent {
			reg := tool.NewRegistry()
			tool.RegisterBuiltinsIn(reg, k.def)
			return &agent.Agent{Provider: writer, Model: "m", Tools: reg, Approve: approve}
		}
		start(t, f, []int64{1}, factory, k.with([]string{"local", "sandbox"}, nil))
		if tc.env != "local" {
			say(f, 1, 1, 1, "/env "+tc.env)
			f.waitFor("the switch", func() bool { return f.sawText("Switched to " + tc.env) })
		}
		say(f, 2, 1, 1, "write it")
		var prompt sentMsg
		f.waitFor("the approval prompt", func() bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, m := range f.sent {
				if m.HasKB {
					prompt = m
					return true
				}
			}
			return false
		})
		if !strings.HasPrefix(prompt.Text, tc.want+"\n") {
			t.Errorf("env %s: the prompt should begin %q, got %q", tc.env, tc.want, prompt.Text)
		}
	}
}

type chatFunc func(ctx context.Context, req provider.Request) (provider.Message, error)

func (f chatFunc) Chat(ctx context.Context, req provider.Request) (provider.Message, error) {
	return f(ctx, req)
}
