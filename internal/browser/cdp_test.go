package browser

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/execenv"
)

// scriptedBrowser is a process the client talks to: cat-like, with the test
// writing the browser's side through the returned function.
//
// The scripts use bash (read -d), which sh is on macOS but not on Debian and Ubuntu, where sh is dash.
func clientOnShell(t *testing.T, script string) (*client, *execenv.Process) {
	t.Helper()
	p, err := execenv.Local{}.Start(context.Background(), "exec bash -c '"+strings.ReplaceAll(script, "'", `'\''`)+"'")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return newClient(p), p
}

// echoServer answers every command with its own id, ended by NUL.
const echoServer = `while IFS= read -r -d '' msg; do
  id=${msg#*\"id\":}; id=${id%%,*}
  printf '{"id":%s,"result":{"ok":true}}\0' "$id"
done`

func TestCallsAreMatchedToTheirAnswers(t *testing.T) {
	c, _ := clientOnShell(t, echoServer)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := c.call(context.Background(), "Test.method", map[string]any{"n": i}, "")
			if err != nil || !strings.Contains(string(raw), "true") {
				t.Errorf("%s %v", raw, err)
			}
		}()
	}
	wg.Wait()
}

func TestErrorsFromTheBrowserCarryTheCommandName(t *testing.T) {
	c, _ := clientOnShell(t, `read -r -d '' msg; printf '{"id":1,"error":{"code":-32601,"message":"no such method"}}\0'; cat >/dev/null`)
	_, err := c.call(context.Background(), "Page.nothing", nil, "")
	var ce *cdpError
	if err == nil || !strings.Contains(err.Error(), "Page.nothing") || !strings.Contains(err.Error(), "no such method") {
		t.Fatalf("%v", err)
	}
	_ = ce
}

func TestEventsReachOnlyTheirSession(t *testing.T) {
	c, _ := clientOnShell(t, `sleep 0.3; printf '{"method":"A.one","sessionId":"S1","params":{}}\0{"method":"B.two","sessionId":"S2","params":{}}\0{"method":"C.three","params":{}}\0'; cat >/dev/null`)
	var mu sync.Mutex
	var mine, all []string
	stop := c.listen("S1", func(ev event) { mu.Lock(); mine = append(mine, ev.Method); mu.Unlock() })
	defer stop()
	stopAll := c.listen("", func(ev event) { mu.Lock(); all = append(all, ev.Method); mu.Unlock() })
	defer stopAll()
	time.Sleep(700 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(mine, ",") != "A.one" {
		t.Fatalf("a tab sees only its own events: %v", mine)
	}
	if len(all) != 3 {
		t.Fatalf("an unfiltered listener sees all: %v", all)
	}
}

// Events the browser sent before it answered a command have been handled when
// the command returns: a page that says it started loading is known to be
// loading by whoever asked it to navigate.
func TestEventsAreHandledBeforeTheAnswerThatFollowsThem(t *testing.T) {
	c, _ := clientOnShell(t, `read -r -d '' msg; printf '{"method":"Page.frameStartedLoading","sessionId":"S1","params":{}}\0{"id":1,"result":{}}\0'; cat >/dev/null`)
	seen := false
	c.listen("S1", func(event) { time.Sleep(50 * time.Millisecond); seen = true }) // a slow handler must still finish first
	if _, err := c.call(context.Background(), "Page.navigate", nil, "S1"); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("the answer overtook the event before it")
	}
}

// A message may be larger than any buffer, and may arrive in pieces.
func TestLargeAndSplitMessagesAreReadWhole(t *testing.T) {
	c, _ := clientOnShell(t, `read -r -d '' msg
big=$(head -c 3000000 /dev/zero | tr '\0' 'x')
printf '{"id":1,"result":{"data":"%s"}}' "$big"; sleep 0.2; printf '\0'; cat >/dev/null`)
	raw, err := c.call(context.Background(), "Page.captureScreenshot", nil, "")
	if err != nil || len(raw) < 3000000 {
		t.Fatalf("%d bytes, %v", len(raw), err)
	}
}

func TestCallsFailWhenTheBrowserExits(t *testing.T) {
	c, _ := clientOnShell(t, `read -r -d '' msg; echo "segfault in renderer" >&2; exit 9`)
	_, err := c.call(context.Background(), "Page.navigate", nil, "")
	if err == nil || !strings.Contains(err.Error(), "the browser exited") || !strings.Contains(err.Error(), "segfault in renderer") {
		t.Fatalf("%v", err)
	}
	// And so does everything after.
	if _, err := c.call(context.Background(), "Page.navigate", nil, ""); err == nil {
		t.Fatal("a dead browser answers nothing")
	}
}

func TestACallStopsWaitingWhenItsContextEnds(t *testing.T) {
	c, _ := clientOnShell(t, `cat >/dev/null`) // reads, never answers
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.call(ctx, "Page.navigate", nil, ""); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("%v after %s", err, time.Since(start))
	}
	c.mu.Lock()
	n := len(c.pending)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("an abandoned call must not be kept: %d", n)
	}
}

func TestMessagesCarryIDsMethodsAndSessions(t *testing.T) {
	c, p := clientOnShell(t, `tee /dev/stderr >/dev/null`) // what arrives is echoed to its standard error
	go c.call(context.Background(), "Page.navigate", map[string]any{"url": "https://x.test/"}, "S9")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && p.Stderr() == "" {
		time.Sleep(20 * time.Millisecond)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimRight(p.Stderr(), "\x00")), &m); err != nil {
		t.Fatalf("%q: %v", p.Stderr(), err)
	}
	if m["method"] != "Page.navigate" || m["sessionId"] != "S9" || m["id"] != float64(1) || m["params"].(map[string]any)["url"] != "https://x.test/" {
		t.Fatalf("%v", m)
	}
}
