package browser

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"talaria/internal/execenv"
	"talaria/internal/tool"
)

// This test drives a real Chromium in the real workspace container, so it is
// opt-in. Set it up first, then run it:
//
//	tal workspace up
//	TALARIA_TEST_WORKSPACE=1 go test ./internal/browser -run Real -v
const realSite = `
import http.server, urllib.parse, html
PAGE = b"""<title>Real</title><h1>Form</h1><form action="/done" method="get">
<label>Name <input name="name"></label>
<label>Country <select name="country"><option>France</option><option>Spain</option></select></label>
<label><input type="checkbox" name="agree"> I agree</label>
<input type="password" name="pw" placeholder="Password">
<button>Send</button></form><a href="/cookie">cookie</a>"""
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        u = urllib.parse.urlparse(self.path); q = urllib.parse.parse_qs(u.query)
        self.send_response(200); self.send_header("Content-Type", "text/html")
        if u.path == "/done": self.send_header("Set-Cookie", "who=%s; Max-Age=3600; Path=/" % q.get("name", ["?"])[0])
        self.end_headers()
        if u.path == "/": self.wfile.write(PAGE)
        elif u.path == "/done": self.wfile.write(("<title>Done</title><p>got %s</p>" % html.escape(str(sorted((k, v[0]) for k, v in q.items() if k != "pw")))).encode())
        else: self.wfile.write(("<title>Cookie</title><p>%s</p>" % html.escape(self.headers.get("Cookie", "none"))).encode())
http.server.HTTPServer(("127.0.0.1", 18765), H).serve_forever()
`

func TestRealChromiumInTheWorkspace(t *testing.T) {
	if os.Getenv("TALARIA_TEST_WORKSPACE") == "" {
		t.Skip("set TALARIA_TEST_WORKSPACE=1 with the workspace running (tal workspace up) to drive a real Chromium")
	}
	ctx := context.Background()
	env, err := execenv.Open(ctx, "workspace", execenv.WorkspaceSpec(), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	srv := exec.Command("docker", "exec", "-i", execenv.WorkspaceContainer, "python3", "-c", realSite)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		srv.Process.Kill()
		exec.Command("docker", "exec", execenv.WorkspaceContainer, "pkill", "-f", "18765").Run()
	}()
	time.Sleep(time.Second)

	newManager := func() (*Manager, *tool.Registry) {
		m := &Manager{Env: env.(execenv.Starter), Opts: Options{ProfileDir: "/workspace/.browser-test-profile", NoSandbox: true, AllowPrivate: true}}
		reg := tool.NewRegistry()
		Register(reg, m, "real")
		return m, reg
	}
	run := func(reg *tool.Registry, name, args string) string {
		t.Helper()
		tl, _ := reg.Get(name)
		res, err := tl.Execute(ctx, []byte(args))
		if err != nil {
			t.Fatalf("%s %s: %v", name, args, err)
		}
		return res.Text
	}

	m, reg := newManager()
	out := run(reg, "browser_open", `{"url":"http://127.0.0.1:18765/"}`)
	for _, want := range []string{`field "Name"`, `drop-down list "Country" = "France" (options: France | Spain)`, `checkbox "I agree" (not checked)`, `password field "Password" (empty)`, `button "Send"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	run(reg, "browser_type", `{"ref":1,"text":"Ada"}`)
	run(reg, "browser_select", `{"ref":2,"value":"Spain"}`)
	run(reg, "browser_click", `{"ref":3}`)
	out = run(reg, "browser_type", `{"ref":4,"text":"hunter2hunter2"}`)
	if !strings.Contains(out, "filled in; the value is hidden") || strings.Contains(out, "hunter2") {
		t.Fatalf("%s", out)
	}
	out = run(reg, "browser_click", `{"ref":5}`)
	if !strings.Contains(out, "Page: Done") || !strings.Contains(out, "'agree', 'on'") || !strings.Contains(out, "'country', 'Spain'") || !strings.Contains(out, "'name', 'Ada'") {
		t.Fatalf("%s", out)
	}
	if strings.Contains(out, "hunter2") {
		t.Fatalf("the password must not appear in the page address shown: %s", out)
	}
	tl, _ := reg.Get("browser_screenshot")
	if res, err := tl.Execute(ctx, []byte(`{}`)); err != nil || len(res.Images) != 1 || len(res.Images[0].Data) < 500 || string(res.Images[0].Data[:2]) != "\xff\xd8" {
		t.Fatalf("%v", err)
	}

	// The browser is stopped and started again: the login cookie is still there.
	m.Close()
	m2, reg2 := newManager()
	defer m2.Close()
	out = run(reg2, "browser_open", `{"url":"http://127.0.0.1:18765/cookie"}`)
	if !strings.Contains(out, "who=Ada") {
		t.Fatalf("a cookie must survive the browser restarting: %s", out)
	}
}
