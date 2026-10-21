package discord

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"talaria/internal/gateway/chat"
	"talaria/internal/provider"
)

func TestExtraCommandsAreRunHelpedAndKeptApartFromMessages(t *testing.T) {
	f := newFakeDiscord(t)
	var calls atomic.Int32
	var mu sync.Mutex
	var spoke []int64
	var gotArgs [][]string
	p := &scripted{replies: []provider.Message{{Role: provider.RoleAssistant, Content: "agent answer"}}}
	start(t, f, []int64{1}, echoAgent(p, &calls), func(g *Gateway) {
		g.Commands = map[string]chat.Command{
			"tasks": {Help: "lists what runs in the background", Run: func(chat int64, args []string) string {
				mu.Lock()
				gotArgs = append(gotArgs, args)
				mu.Unlock()
				return fmt.Sprintf("tasks for %d: %s", chat, strings.Join(args, ","))
			}},
		}
		g.OnUserMessage = func(chat int64) { mu.Lock(); spoke = append(spoke, chat); mu.Unlock() }
	})

	f.dm(1, "!help")
	f.waitFor("help", func() bool { return f.sawText("Talaria is ready") })
	if !f.sawText("!tasks lists what runs in the background.") {
		t.Fatalf("help lists the extra commands: %q", f.texts())
	}

	f.dm(1, "!tasks cancel abc123")
	f.waitFor("the command's answer", func() bool { return f.sawText("tasks for 1: cancel,abc123") })
	if calls.Load() != 0 {
		t.Fatal("a command is not a message for the agent")
	}
	mu.Lock()
	if len(spoke) != 0 {
		t.Fatalf("a command is not the user speaking: %v", spoke)
	}
	mu.Unlock()

	f.dm(1, "/tasks") // the slash form too
	f.waitFor("both forms", func() bool {
		n := 0
		for _, s := range f.texts() {
			if strings.HasPrefix(s, "tasks for 1") {
				n++
			}
		}
		return n == 2
	})

	f.dm(1, "!unknown thing") // not a command: a message
	f.waitFor("the agent's answer", func() bool { return f.sawText("agent answer") })
	mu.Lock()
	defer mu.Unlock()
	if len(spoke) != 1 || spoke[0] != 1 {
		t.Fatalf("an ordinary message tells the hook the user is there: %v", spoke)
	}
}

func TestStrangersCannotRunExtraCommands(t *testing.T) {
	f := newFakeDiscord(t)
	ran := false
	start(t, f, []int64{1}, echoAgent(&scripted{}, new(atomic.Int32)), func(g *Gateway) {
		g.Commands = map[string]chat.Command{"tasks": {Help: "x", Run: func(int64, []string) string { ran = true; return "secret list" }}}
	})
	f.dm(99, "!tasks")
	f.dm(1, "!whoami")
	f.waitFor("the owner's answer", func() bool { return f.sawText("Your Discord user ID is 1") })
	time.Sleep(100 * time.Millisecond)
	if ran || f.sawText("secret list") {
		t.Fatal("a command of the engine's is for allowed users only")
	}
}

// /stop stops what the conversation is doing; with nothing running there, it
// stops the background tasks instead.
func TestStopReachesBackgroundTasksWhenNothingElseIsRunning(t *testing.T) {
	f := newFakeDiscord(t)
	stopped := atomic.Int32{}
	count := atomic.Int32{}
	count.Store(2)
	start(t, f, []int64{1}, echoAgent(&scripted{}, new(atomic.Int32)), func(g *Gateway) {
		g.StopBackground = func(chat int64) int { stopped.Add(1); return int(count.Swap(0)) }
	})
	f.dm(1, "!stop")
	f.waitFor("the report", func() bool { return f.sawText("Stopped the background task(s)") })
	f.dm(1, "!stop")
	f.waitFor("nothing left", func() bool { return f.sawText("Nothing is running.") })
	if stopped.Load() != 2 {
		t.Fatalf("%d", stopped.Load())
	}
}
