package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/browser"
	"talaria/internal/cron"
	"talaria/internal/execenv"
	"talaria/internal/goal"
	"talaria/internal/vault"
)

type noControl struct{}

func (noControl) Wake()                             {}
func (noControl) Runs(string, int64) []cron.RunInfo { return nil }
func (noControl) Cancel(string, int64, string) bool { return false }

func toolSet(rt *Runtime, o AgentOptions) map[string]bool {
	a := rt.NewAgent(o)
	out := map[string]bool{}
	for _, s := range a.Tools.Specs() {
		out[s.Name] = true
	}
	return out
}

func TestBackgroundAndGoalToolsAreOfferedWhereTheyBelong(t *testing.T) {
	goals := &goal.Store{Path: filepath.Join(t.TempDir(), "goals.json")}
	rt := openWith(t, Config{Env: execenv.Local{}, Goals: goals})
	chat := AgentOptions{ChatID: 5, Gateway: "discord", Tasks: noControl{}}

	tools := toolSet(rt, chat)
	for _, want := range []string{"task_start", "task_list", "task_cancel", "goal_create", "goal_list", "goal_get", "goal_update", "cron_add"} {
		if !tools[want] {
			t.Errorf("a chat should have %s", want)
		}
	}

	// Unattended work may update what it is working on, and start nothing.
	unattended := chat
	unattended.NoCron = true
	tools = toolSet(rt, unattended)
	for _, never := range []string{"task_start", "goal_create", "cron_add"} {
		if tools[never] {
			t.Errorf("unattended work must not have %s", never)
		}
	}
	for _, want := range []string{"goal_update", "goal_get", "goal_list"} {
		if !tools[want] {
			t.Errorf("a check-in needs %s", want)
		}
	}

	// The command line has no chat to report to.
	tools = toolSet(rt, AgentOptions{})
	for name := range tools {
		if strings.HasPrefix(name, "task_") || strings.HasPrefix(name, "goal_") {
			t.Errorf("%s without a chat", name)
		}
	}

	// No scheduler to run tasks on: no task tools. Goals off: no goal tools.
	noTasks := chat
	noTasks.Tasks = nil
	if toolSet(rt, noTasks)["task_start"] {
		t.Error("task_start with nothing to run it")
	}
	off := openWith(t, Config{Env: execenv.Local{}})
	if tools := toolSet(off, chat); tools["goal_create"] || tools["goal_list"] {
		t.Error("goal tools with goals off")
	}
}

func TestGoalsAreScopedToTheChatTheAgentIsIn(t *testing.T) {
	goals := &goal.Store{Path: filepath.Join(t.TempDir(), "goals.json")}
	rt := openWith(t, Config{Env: execenv.Local{}, Goals: goals})
	a := rt.NewAgent(AgentOptions{ChatID: 5, Gateway: "discord", Tasks: noControl{}})
	tl, _ := a.Tools.Get("goal_create")
	if _, err := tl.Run(context.Background(), []byte(`{"title":"t","objective":"o"}`)); err != nil {
		t.Fatal(err)
	}
	if gs, _ := goals.List("discord", 5); len(gs) != 1 {
		t.Fatalf("%+v", gs)
	}
	if gs, _ := goals.List("telegram", 5); len(gs) != 0 {
		t.Fatalf("a chat is its service's: %+v", gs)
	}
	jobs, _ := rt.Cron.List()
	if len(jobs) != 1 || jobs[0].Kind != cron.KindGoal || jobs[0].Gateway != "discord" {
		t.Fatalf("the goal is scheduled on the chat's own service: %+v", jobs)
	}
}

func TestVaultToolsFollowTheBrowser(t *testing.T) {
	v, err := vault.Create(filepath.Join(t.TempDir(), "v.enc"), "a long enough passphrase")
	if err != nil {
		t.Fatal(err)
	}
	ws := wsEnv{container: execenv.WorkspaceContainer}

	// With a vault, in the workspace: both tools.
	rt := openWith(t, Config{Env: ws, Vault: v})
	if tools := toolSet(rt, AgentOptions{}); !tools["vault_list"] || !tools["browser_fill"] {
		t.Fatalf("%v", tools)
	}
	if !rt.HasVault() {
		t.Fatal("HasVault")
	}
	// Without a vault: neither.
	bare := openWith(t, Config{Env: ws})
	if tools := toolSet(bare, AgentOptions{}); tools["vault_list"] || tools["browser_fill"] {
		t.Fatalf("%v", tools)
	}
	if bare.HasVault() {
		t.Fatal("no vault")
	}
	// With a vault but no browser (this machine): saved logins have nothing to fill.
	local := openWith(t, Config{Env: execenv.Local{}, Vault: v})
	if tools := toolSet(local, AgentOptions{}); tools["vault_list"] || tools["browser_fill"] {
		t.Fatalf("%v", tools)
	}
	// Moving an agent out of the workspace takes the vault tools with the browser.
	a := rt.NewAgent(AgentOptions{})
	rt.UseEnvironment(a, execenv.Local{})
	for _, s := range a.Tools.Specs() {
		if strings.HasPrefix(s.Name, browser.Prefix) || strings.HasPrefix(s.Name, "vault_") {
			t.Fatalf("%s is left behind", s.Name)
		}
	}
}
