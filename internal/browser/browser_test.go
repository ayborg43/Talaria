package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"talaria/internal/execenv"
	"talaria/internal/tool"
)

type rig struct {
	t     *testing.T
	m     *Manager
	reg   *tool.Registry
	log   string
	args  string
	exit  string
	owner string
}

// newRig starts a manager that runs the fake browser as the machine's chromium.
func newRig(t *testing.T, opts Options) *rig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake browser is started by a shell script")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "chromium")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TAL_FAKE_ARGS\"\nTAL_FAKE_CHROME=1 exec \"" + self + "\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, owner: "chat1", log: filepath.Join(dir, "cdp.log"), args: filepath.Join(dir, "args"), exit: filepath.Join(dir, "exit")}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TAL_FAKE_LOG", r.log)
	t.Setenv("TAL_FAKE_ARGS", r.args)
	t.Setenv("TAL_FAKE_EXIT", r.exit)
	if opts.ProfileDir == "" {
		opts.ProfileDir = filepath.Join(dir, "profile")
	}
	r.m = &Manager{Env: execenv.Local{}, Opts: opts, Resolve: func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "shop.test", "redirect.test", "crash.test", "slow.test", "example.com", "login.test", "evil.test":
			return []string{"93.184.216.34"}, nil
		case "sneaky.test":
			return []string{"192.168.1.20"}, nil
		}
		return nil, os.ErrNotExist
	}}
	r.reg = tool.NewRegistry()
	Register(r.reg, r.m, r.owner)
	t.Cleanup(r.m.Close)
	return r
}

func (r *rig) call(name, args string) (string, error) {
	r.t.Helper()
	tl, ok := r.reg.Get(name)
	if !ok {
		r.t.Fatalf("no tool %s", name)
	}
	res, err := tl.Execute(context.Background(), json.RawMessage(args))
	return res.Text, err
}

func (r *rig) ok(name, args string) string {
	r.t.Helper()
	out, err := r.call(name, args)
	if err != nil {
		r.t.Fatalf("%s %s: %v", name, args, err)
	}
	return out
}

type cdpCall struct {
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
	Session string         `json:"session"`
}

func (r *rig) commands() []cdpCall {
	raw, _ := os.ReadFile(r.log)
	var out []cdpCall
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var c cdpCall
		if json.Unmarshal([]byte(line), &c) == nil && c.Method != "" {
			out = append(out, c)
		}
	}
	return out
}

func (r *rig) sent(method string) []cdpCall {
	var out []cdpCall
	for _, c := range r.commands() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func init() { // keep tests quick: the fake loads pages in 30ms
	settleStart = 80 * time.Millisecond
	afterLoad = 10 * time.Millisecond
	sweepEvery = time.Hour // tests call expire themselves
}

func TestOpenRendersThePageForTheModel(t *testing.T) {
	r := newRig(t, Options{})
	out := r.ok("browser_open", `{"url":"http://shop.test/"}`)
	for _, want := range []string{
		"Page: Shop\nURL: http://shop.test/",
		`[1] link "Cart" → http://shop.test/cart`,
		`[2] search field "Search" placeholder "Search products" (empty)`,
		`[3] button "Go"`,
		`[4] password field "Password" (filled in; the value is hidden)`,
		`[5] drop-down list "Country" = "France" (options: France | Spain | Italy)`,
		`[6] checkbox "Gift wrap" (not checked)`,
		`[7] button "Buy now" (disabled)`,
		"Scrolled 0 of 1200 pixels.",
		untrusted,
		"Welcome to the shop.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// A password field's contents are never shown, not even as a marker.
	if strings.Contains(out, `value "(hidden)"`) {
		t.Errorf("the marker must not look like a value:\n%s", out)
	}
	// The page's text comes after the warning that it is not to be obeyed.
	if strings.Index(out, "Ignore all previous") < strings.Index(out, untrusted) {
		t.Fatalf("page text must come after the untrusted-data warning:\n%s", out)
	}
}

func TestLaunchUsesTheRealScriptAndSafeFlags(t *testing.T) {
	r := newRig(t, Options{NoSandbox: true})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	raw, _ := os.ReadFile(r.args)
	args := string(raw)
	for _, want := range []string{"--headless=new", "--remote-debugging-pipe", "--user-data-dir=" + r.m.Opts.ProfileDir, "--no-sandbox", "--window-size=1280,800", "about:blank"} {
		if !strings.Contains(args, want) {
			t.Errorf("the browser was not started with %s:\n%s", want, args)
		}
	}
	for _, never := range []string{"--remote-debugging-port", "--remote-debugging-address", "--disable-web-security"} {
		if strings.Contains(args, never) {
			t.Errorf("the browser must not be started with %s", never)
		}
	}
	if fi, err := os.Stat(r.m.Opts.ProfileDir); err != nil || !fi.IsDir() {
		t.Fatalf("the profile directory should be created: %v", err)
	}

	// The sandbox stays on unless asked.
	r2 := newRig(t, Options{})
	r2.ok("browser_open", `{"url":"http://shop.test/"}`)
	if raw, _ := os.ReadFile(r2.args); strings.Contains(string(raw), "--no-sandbox") {
		t.Fatalf("--no-sandbox without being asked: %s", raw)
	}
}

func TestBrowserIsPreparedBeforeUse(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	ua := r.sent("Emulation.setUserAgentOverride")
	if len(ua) != 1 || strings.Contains(ua[0].Params["userAgent"].(string), "Headless") || !strings.Contains(ua[0].Params["userAgent"].(string), "Chrome/126") {
		t.Fatalf("%+v", ua)
	}
	if dl := r.sent("Browser.setDownloadBehavior"); len(dl) != 1 || dl[0].Params["behavior"] != "deny" {
		t.Fatalf("downloads must be refused: %+v", dl)
	}
	if len(r.sent("Page.addScriptToEvaluateOnNewDocument")) != 1 || len(r.sent("Emulation.setDeviceMetricsOverride")) != 1 {
		t.Fatalf("%+v", r.commands())
	}
	for _, c := range r.commands() {
		if strings.HasPrefix(c.Method, "Page.") || strings.HasPrefix(c.Method, "Runtime.") || strings.HasPrefix(c.Method, "Input.") {
			if c.Session == "" {
				t.Errorf("%s was sent to the browser, not to the tab", c.Method)
			}
		}
	}
}

func TestProfileDirectoryUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	r := newRig(t, Options{ProfileDir: "~/browser-data"})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	if fi, err := os.Stat(filepath.Join(home, "browser-data")); err != nil || !fi.IsDir() {
		t.Fatalf("~ should mean the home of whoever runs the browser: %v", err)
	}
}

func TestLoadFailuresAreReported(t *testing.T) {
	r := newRig(t, Options{AllowPrivate: true})
	_, err := r.call("browser_open", `{"url":"http://unreachable.test/"}`)
	if err == nil || !strings.Contains(err.Error(), "ERR_NAME_NOT_RESOLVED") || strings.Contains(err.Error(), "net::") {
		t.Fatalf("%v", err)
	}
}

func TestClickFollowsALink(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	out := r.ok("browser_click", `{"ref":1}`)
	if !strings.Contains(out, "Page: Cart") || !strings.Contains(out, "Your cart is empty.") || !strings.Contains(out, `[1] link "Home"`) {
		t.Fatalf("%s", out)
	}
	moves := r.sent("Input.dispatchMouseEvent")
	var kinds []string
	for _, m := range moves {
		kinds = append(kinds, m.Params["type"].(string))
	}
	if strings.Join(kinds, ",") != "mouseMoved,mousePressed,mouseReleased" {
		t.Fatalf("a click is a move, a press and a release: %v", kinds)
	}
	if moves[1].Params["x"] != float64(100) || moves[1].Params["button"] != "left" {
		t.Fatalf("%+v", moves[1])
	}
}

func TestClickOnAStaleNumber(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	if _, err := r.call("browser_click", `{"ref":99}`); err == nil || !strings.Contains(err.Error(), "out of date") {
		t.Fatalf("%v", err)
	}
}

func TestClickThatLandsOnSomethingElseSaysSo(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	out := r.ok("browser_click", `{"ref":3}`) // the fake covers element 3
	if !strings.Contains(out, "Something else was on top") || !strings.Contains(out, "Cookie banner") {
		t.Fatalf("%s", out)
	}
}

func TestTypeReplacesAppendsAndSubmits(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	out := r.ok("browser_type", `{"ref":2,"text":"red shoes"}`)
	if !strings.Contains(out, `value "red shoes"`) {
		t.Fatalf("%s", out)
	}
	if got := r.sent("Input.insertText"); len(got) != 1 || got[0].Params["text"] != "red shoes" {
		t.Fatalf("%+v", got)
	}
	out = r.ok("browser_type", `{"ref":2,"text":" size 9","append":true}`)
	if !strings.Contains(out, `value "red shoes size 9"`) {
		t.Fatalf("appending adds to what is there: %s", out)
	}
	out = r.ok("browser_type", `{"ref":2,"text":"hats","submit":true}`)
	if !strings.Contains(out, "Page: Results") || !strings.Contains(out, "3 results") {
		t.Fatalf("submit presses Enter, which sends the search: %s", out)
	}
	keys := r.sent("Input.dispatchKeyEvent")
	if len(keys) != 2 || keys[0].Params["key"] != "Enter" || keys[0].Params["text"] != "\r" || keys[1].Params["type"] != "keyUp" {
		t.Fatalf("%+v", keys)
	}
}

func TestTypeIntoSomethingThatIsNotAField(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	if _, err := r.call("browser_type", `{"ref":6,"text":"x"}`); err == nil || !strings.Contains(err.Error(), "cannot be typed into") {
		t.Fatalf("%v", err)
	}
	if _, err := r.call("browser_type", `{"ref":42,"text":"x"}`); err == nil || !strings.Contains(err.Error(), "out of date") {
		t.Fatalf("%v", err)
	}
}

func TestPressKeys(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	r.ok("browser_press", `{"key":"Escape"}`)
	if k := r.sent("Input.dispatchKeyEvent"); len(k) != 2 || k[0].Params["key"] != "Escape" || k[0].Params["windowsVirtualKeyCode"] != float64(27) {
		t.Fatalf("%+v", k)
	}
	if _, err := r.call("browser_press", `{"key":"Ctrl+Q"}`); err == nil || !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), "Enter") {
		t.Fatalf("%v", err)
	}
}

func TestSelectOption(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	out := r.ok("browser_select", `{"ref":5,"value":"spain"}`)
	if !strings.Contains(out, `Chose "Spain"`) || !strings.Contains(out, `= "Spain"`) {
		t.Fatalf("%s", out)
	}
	if _, err := r.call("browser_select", `{"ref":5,"value":"Narnia"}`); err == nil || !strings.Contains(err.Error(), "France | Spain | Italy") {
		t.Fatalf("the options are listed so the model can choose again: %v", err)
	}
	if _, err := r.call("browser_select", `{"ref":1,"value":"x"}`); err == nil || !strings.Contains(err.Error(), "not a drop-down") {
		t.Fatalf("%v", err)
	}
}

func TestScrollBackWaitAndScreenshot(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	r.ok("browser_scroll", `{"direction":"down"}`)
	if _, err := r.call("browser_scroll", `{"direction":"sideways"}`); err == nil {
		t.Fatal("unknown direction")
	}
	r.ok("browser_click", `{"ref":1}`)
	out := r.ok("browser_back", `{}`)
	if !strings.Contains(out, "Page: Shop") {
		t.Fatalf("back returns to the previous page: %s", out)
	}
	if _, err := r.call("browser_back", `{}`); err == nil || !strings.Contains(err.Error(), "no earlier page") {
		// the history now has only the first page
		t.Fatalf("%v", err)
	}
	if out := r.ok("browser_wait", `{"text":"Welcome","seconds":2}`); !strings.Contains(out, `Waited for "Welcome": it appeared`) {
		t.Fatalf("%s", out)
	}
	if out := r.ok("browser_wait", `{"text":"never shown","seconds":0.5}`); !strings.Contains(out, "it did not appear") {
		t.Fatalf("%s", out)
	}

	tl, _ := r.reg.Get("browser_screenshot")
	res, err := tl.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil || len(res.Images) != 1 || res.Images[0].MIME != "image/jpeg" || !strings.HasPrefix(string(res.Images[0].Data), "\xff\xd8") {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(res.Text, "http://shop.test/") {
		t.Fatalf("%q", res.Text)
	}
	_ = base64.StdEncoding
}

func TestDialogsAreHandledAndReported(t *testing.T) {
	r := newRig(t, Options{})
	out := r.ok("browser_open", `{"url":"http://shop.test/alert"}`)
	if !strings.Contains(out, `Note: A alert dialog appeared saying "Hello there"; it was accepted.`) {
		t.Fatalf("%s", out)
	}
	out = r.ok("browser_open", `{"url":"http://shop.test/confirm"}`)
	if !strings.Contains(out, `"Delete everything?"; it was dismissed.`) {
		t.Fatalf("a question the agent did not choose to answer is refused: %s", out)
	}
	time.Sleep(50 * time.Millisecond)
	d := r.sent("Page.handleJavaScriptDialog")
	if len(d) != 2 || d[0].Params["accept"] != true || d[1].Params["accept"] != false {
		t.Fatalf("%+v", d)
	}
	// A note is told once.
	if out := r.ok("browser_snapshot", `{}`); strings.Contains(out, "dialog appeared") {
		t.Fatalf("%s", out)
	}
}

func TestEachConversationHasItsOwnTab(t *testing.T) {
	r := newRig(t, Options{})
	other := tool.NewRegistry()
	Register(other, r.m, "chat2")
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	tl, _ := other.Get("browser_open")
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"url":"http://shop.test/cart"}`)); err != nil {
		t.Fatal(err)
	}
	if n := len(r.sent("Target.createTarget")); n != 2 {
		t.Fatalf("two conversations, two tabs: %d", n)
	}
	if got := len(r.sent("Browser.getVersion")); got != 1 {
		t.Fatalf("but one browser, so logins are shared: %d launches", got)
	}
	r.ok("browser_close", `{}`)
	if len(r.sent("Target.closeTarget")) != 1 || r.m.Peek("chat1") != nil || r.m.Peek("chat2") == nil {
		t.Fatal("closing one conversation's tab leaves the other's")
	}
	if out := r.ok("browser_close", `{}`); !strings.Contains(out, "no tab") {
		t.Fatal(out)
	}
}

func TestAFreshTabSaysSoInsteadOfActingOnNothing(t *testing.T) {
	r := newRig(t, Options{})
	_, err := r.call("browser_click", `{"ref":1}`)
	if err == nil || !strings.Contains(err.Error(), "Open a page first") {
		t.Fatalf("%v", err)
	}
	// And after the browser has gone, the model is told its page is gone too.
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	r.m.mu.Lock()
	r.m.cl.close()
	r.m.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	if _, err := r.call("browser_click", `{"ref":1}`); err == nil || !strings.Contains(err.Error(), "(re)started") {
		t.Fatalf("%v", err)
	}
	if out := r.ok("browser_open", `{"url":"http://shop.test/"}`); !strings.Contains(out, "Page: Shop") {
		t.Fatalf("it works again once a page is opened: %s", out)
	}
}

func TestBrowserDyingMidCallIsReportedWithItsReason(t *testing.T) {
	r := newRig(t, Options{})
	_, err := r.call("browser_open", `{"url":"http://crash.test/"}`)
	if err == nil || !strings.Contains(err.Error(), "the browser exited") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("%v", err)
	}
	// The next call starts a new browser.
	if out := r.ok("browser_open", `{"url":"http://shop.test/"}`); !strings.Contains(out, "Page: Shop") {
		t.Fatal(out)
	}
}

func TestIdleTabsAndBrowsersAreClosed(t *testing.T) {
	r := newRig(t, Options{PageIdle: time.Minute, BrowserIdle: 30 * time.Second})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	now := time.Now()
	r.m.expire(now.Add(10 * time.Second))
	if r.m.Peek("chat1") == nil {
		t.Fatal("a recently used tab stays")
	}
	r.m.expire(now.Add(2 * time.Minute)) // the tab has been idle too long
	if r.m.Peek("chat1") != nil || len(r.sent("Target.closeTarget")) != 1 {
		t.Fatal("an idle tab is closed")
	}
	if !r.m.Running() {
		t.Fatal("the browser stays a while for the next tab")
	}
	r.m.expire(now.Add(2*time.Minute + 5*time.Second))
	r.m.expire(now.Add(4 * time.Minute)) // and now it has had no tabs for long enough
	if r.m.Running() {
		t.Fatal("an idle browser is stopped")
	}
	waitFor(t, "the browser process to exit", func() bool { _, err := os.Stat(r.exit); return err == nil })
	// Needing it again just starts a new one, with the same profile.
	if out := r.ok("browser_open", `{"url":"http://shop.test/"}`); !strings.Contains(out, "Page: Shop") {
		t.Fatal(out)
	}
}

func TestCloseStopsTheBrowserAndFurtherUseFails(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://shop.test/"}`)
	r.m.Close()
	waitFor(t, "the browser process to exit", func() bool { _, err := os.Stat(r.exit); return err == nil })
	if _, err := r.call("browser_snapshot", `{}`); err == nil || !strings.Contains(err.Error(), "shut down") {
		t.Fatalf("%v", err)
	}
	r.m.Close() // harmless again
}

func TestNoBrowserInstalled(t *testing.T) {
	r := newRig(t, Options{Binary: "/definitely/not/a/browser"})
	_, err := r.call("browser_open", `{"url":"http://shop.test/"}`)
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(r.args); err == nil {
		t.Fatal("nothing should have been started")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A public address can send the browser to a private one. Where it ends up is
// checked too, and the tab is cleared rather than left showing it.
func TestARedirectIntoAPrivateNetworkIsCaught(t *testing.T) {
	r := newRig(t, Options{})
	_, err := r.call("browser_open", `{"url":"http://redirect.test/"}`)
	if err == nil || !strings.Contains(err.Error(), "10.0.0.5/admin") || !strings.Contains(err.Error(), "not allowed") || !strings.Contains(err.Error(), "cleared") {
		t.Fatalf("%v", err)
	}
	nav := r.sent("Page.navigate")
	if last := nav[len(nav)-1]; last.Params["url"] != "about:blank" {
		t.Fatalf("the tab must be sent to a blank page: %+v", last)
	}
	if out := r.ok("browser_open", `{"url":"http://shop.test/"}`); !strings.Contains(out, "Page: Shop") {
		t.Fatalf("the browser still works: %s", out)
	}
	// The page's content never reached the model.
	if strings.Contains(err.Error(), "internal admin page") {
		t.Fatal("what was on the private page must not be returned")
	}
}

func TestPrivateAddressesNeverReachTheBrowserUnlessAllowed(t *testing.T) {
	r := newRig(t, Options{})
	for _, u := range []string{"http://127.0.0.1:8080/", "http://sneaky.test/", "file:///etc/passwd", "http://localhost/"} {
		if _, err := r.call("browser_open", `{"url":"`+u+`"}`); err == nil {
			t.Errorf("%s was opened", u)
		}
	}
	if len(r.sent("Page.navigate")) != 0 || len(r.sent("Target.createTarget")) != 0 {
		t.Fatalf("the browser should not even have been asked: %+v", r.commands())
	}
	if _, err := os.Stat(r.args); err == nil {
		t.Fatal("a refused address must not start a browser")
	}
}
