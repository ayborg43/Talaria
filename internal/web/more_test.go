package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"talaria/internal/config"
	"talaria/internal/execenv"
	"talaria/internal/files"
	"talaria/internal/memory"
)

// fakeBox is a computer apart from this machine, kept in a map.
type fakeBox struct {
	local bool
	files map[string]string
	ran   []string
}

func (b *fakeBox) Describe() string { return "the test container" }
func (b *fakeBox) Remote() bool     { return !b.local }
func (b *fakeBox) Close()           {}
func (b *fakeBox) Shell(_ context.Context, cmd string, _ time.Duration) (execenv.ShellResult, error) {
	b.ran = append(b.ran, cmd)
	if strings.HasPrefix(cmd, "mkdir") {
		return execenv.ShellResult{}, nil
	}
	return execenv.ShellResult{Output: "ran: " + cmd + " token=sk-secret-value\n", ExitCode: 3}, nil
}
func (b *fakeBox) ReadFile(_ context.Context, p string, limit int) ([]byte, bool, error) {
	v, ok := b.files[p]
	if !ok {
		return nil, false, errors.New("no such file")
	}
	if len(v) > limit {
		return []byte(v[:limit]), true, nil
	}
	return []byte(v), false, nil
}
func (b *fakeBox) WriteFile(_ context.Context, p string, data []byte) error {
	b.files[p] = string(data)
	return nil
}
func (b *fakeBox) ListDir(_ context.Context, p string) ([]string, error) {
	if p != "." {
		return nil, errors.New("no such folder")
	}
	return []string{"docs/", "notes.txt"}, nil
}

func decode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
}

func TestTheNewScreensNeedASignIn(t *testing.T) {
	r := newRig(t)
	for _, rt := range []struct{ m, p string }{
		{"GET", "/api/profile"}, {"POST", "/api/profile"}, {"GET", "/api/notices"}, {"POST", "/api/notices"}, {"POST", "/api/tasks"},
		{"GET", "/api/files/x/form"}, {"POST", "/api/files/x/form"},
		{"GET", "/api/computer"}, {"POST", "/api/computer/run"}, {"GET", "/api/computer/files"}, {"GET", "/api/computer/file"},
		{"POST", "/api/computer/file"}, {"POST", "/api/computer/transfer"},
		{"GET", "/api/browser"}, {"GET", "/api/browser/shot"}, {"POST", "/api/browser/act"},
	} {
		if resp, body := r.do(rt.m, rt.p, map[string]string{}); resp.StatusCode != 401 {
			t.Errorf("%s %s without signing in: %d %s", rt.m, rt.p, resp.StatusCode, body)
		}
	}
}

func TestProfileAndNotesAreEditedFromTheApp(t *testing.T) {
	r := newRig(t)
	r.srv.Notes = &memory.Notes{Dir: t.TempDir()}
	r.login()
	if resp, body := r.do("POST", "/api/profile", map[string]any{"profile": map[string]string{"name": "Ayo", "tone": "warm and brief", "avatar": "A"}}); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	r.do("POST", "/api/profile", map[string]any{"note": map[string]any{"target": "user", "text": "prefers mornings"}})
	_, body := r.do("POST", "/api/profile", map[string]any{"note": map[string]any{"target": "user", "text": "lives in Lagos"}})
	var got struct {
		Profile memory.Profile
		User    notesView
	}
	decode(t, body, &got)
	if got.Profile.Name != "Ayo" || len(got.User.Entries) != 2 || got.User.Limit == 0 {
		t.Fatalf("%+v", got)
	}
	// An edit names the note by place and text; a stale one is refused.
	if resp, _ := r.do("POST", "/api/profile", map[string]any{"note": map[string]any{"target": "user", "index": 0, "old": "something else", "text": "x"}}); resp.StatusCode != 400 {
		t.Fatalf("a stale edit answered %d", resp.StatusCode)
	}
	_, body = r.do("POST", "/api/profile", map[string]any{"note": map[string]any{"target": "user", "index": 0, "old": "prefers mornings", "text": ""}})
	decode(t, body, &got)
	if len(got.User.Entries) != 1 || got.User.Entries[0] != "lives in Lagos" {
		t.Fatalf("after removing: %+v", got.User)
	}
	if !strings.Contains(r.srv.Notes.Prompt(), "The user asks to be called: Ayo") {
		t.Error("the profile did not reach the prompt")
	}
}

type pushSpy struct{ got []string }

func (r *pushSpy) Notify(title, body, tag, path string) { r.got = append(r.got, tag+":"+title) }

func TestNotificationsAreKeptAndAKindCanBeMuted(t *testing.T) {
	r := newRig(t)
	push := &pushSpy{}
	r.srv.Notices = &Notices{Path: filepath.Join(t.TempDir(), "n.json"), Next: push}
	r.login()
	r.srv.Notices.Notify("Needs your OK", "shell", "approval", "/#agents")
	if resp, body := r.do("POST", "/api/notices", map[string]any{"prefs": map[string]bool{"approvals": true, "replies": false}}); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	r.srv.Notices.Notify("Talaria", "done", "reply", "/")
	if len(push.got) != 1 || push.got[0] != "approval:Needs your OK" {
		t.Fatalf("pushed %v: a muted kind must not buzz the phone", push.got)
	}
	_, body := r.do("GET", "/api/notices", nil)
	var got struct {
		Items []Notice
		Prefs NoticePrefs
	}
	decode(t, body, &got)
	if len(got.Items) != 2 || got.Items[0].Tag != "reply" || got.Prefs.Replies || !got.Prefs.Approvals {
		t.Fatalf("the muted one is still kept to read, newest first: %+v", got)
	}
	for i := 0; i < maxNotices+10; i++ {
		r.srv.Notices.Notify("t", "b", "reply", "/")
	}
	if items, _ := r.srv.Notices.List(); len(items) != maxNotices {
		t.Fatalf("kept %d", len(items))
	}
	_, body = r.do("POST", "/api/notices", map[string]any{"clear": true})
	decode(t, body, &got)
	if len(got.Items) != 0 || got.Prefs.Replies {
		t.Fatalf("clearing empties the list and keeps the preferences: %+v", got)
	}
}

func TestAFinishedTaskIsRunAgainFromTheApp(t *testing.T) {
	r := newRig(t)
	var retried []string
	r.srv.Sources.Tasks = func() []TaskInfo {
		return []TaskInfo{{ID: "ab12", Name: "research", Status: "error: timed out", Failed: true}}
	}
	r.srv.Sources.RetryTask = func(id string) error {
		if id != "ab12" {
			return errors.New("no finished task with that id is kept")
		}
		retried = append(retried, id)
		return nil
	}
	r.login()
	_, body := r.do("GET", "/api/agents", nil)
	if !strings.Contains(string(body), `"research"`) {
		t.Fatalf("the finished task is not listed: %s", body)
	}
	if resp, _ := r.do("POST", "/api/tasks", map[string]string{"retry": "zz"}); resp.StatusCode != 400 {
		t.Fatalf("an unknown task answered %d", resp.StatusCode)
	}
	if resp, body := r.do("POST", "/api/tasks", map[string]string{"retry": "ab12"}); resp.StatusCode != 200 || len(retried) != 1 {
		t.Fatalf("%d %s %v", resp.StatusCode, body, retried)
	}
}

func TestTheTerminalIsOnlyOfferedOnAComputerOfItsOwn(t *testing.T) {
	r := newRig(t)
	r.login()
	local := &fakeBox{local: true, files: map[string]string{}}
	for _, env := range []execenv.Env{nil, local} {
		r.srv.Env = env
		_, body := r.do("GET", "/api/computer", nil)
		var got struct {
			Available bool
			Reason    string
		}
		decode(t, body, &got)
		if got.Available || got.Reason == "" {
			t.Fatalf("offered with env %v: %s", env, body)
		}
		if resp, _ := r.do("POST", "/api/computer/run", map[string]string{"command": "id"}); resp.StatusCode != 501 {
			t.Fatalf("a command was accepted with env %v: %d", env, resp.StatusCode)
		}
	}
	if len(local.ran) != 0 {
		t.Fatalf("commands ran on the server itself: %v", local.ran)
	}
}

func TestTheComputerRunsCommandsKeepsReceiptsAndEditsFiles(t *testing.T) {
	r := newRig(t)
	box := &fakeBox{files: map[string]string{"notes.txt": "hello", "pic.bin": "\x00\x01\x02"}}
	r.srv.Env = box
	r.srv.ReceiptsPath = filepath.Join(t.TempDir(), "receipts.json")
	r.srv.Scrub = func(s string) string { return strings.ReplaceAll(s, "sk-secret-value", "[redacted]") }
	r.srv.Files = &files.Store{Dir: t.TempDir()}
	r.login()

	resp, body := r.do("POST", "/api/computer/run", map[string]string{"command": "  ls -la "})
	var rc Receipt
	decode(t, body, &rc)
	if resp.StatusCode != 200 || rc.Exit != 3 || !strings.Contains(rc.Output, "ran: ls -la") || strings.Contains(rc.Output, "sk-secret-value") {
		t.Fatalf("%d %+v", resp.StatusCode, rc)
	}
	_, body = r.do("GET", "/api/computer", nil)
	var got struct {
		Available bool
		Where     string
		Receipts  []Receipt
	}
	decode(t, body, &got)
	if !got.Available || got.Where != "the test container" || len(got.Receipts) != 1 || got.Receipts[0].Command != "ls -la" {
		t.Fatalf("%+v", got)
	}
	if strings.Contains(got.Receipts[0].Output, "sk-secret-value") {
		t.Fatal("a secret was saved in a receipt")
	}
	if resp, _ := r.do("POST", "/api/computer/run", map[string]string{"command": " "}); resp.StatusCode != 400 {
		t.Fatalf("an empty command answered %d", resp.StatusCode)
	}

	_, body = r.do("GET", "/api/computer/files", nil)
	if !strings.Contains(string(body), `{"name":"docs","dir":true}`) || !strings.Contains(string(body), `{"name":"notes.txt"}`) {
		t.Fatalf("listing: %s", body)
	}
	_, body = r.do("GET", "/api/computer/file?path=notes.txt", nil)
	if !strings.Contains(string(body), `"text":"hello"`) {
		t.Fatalf("reading: %s", body)
	}
	if resp, _ := r.do("GET", "/api/computer/file?path=pic.bin", nil); resp.StatusCode != 415 {
		t.Fatalf("a binary file was shown as text: %d", resp.StatusCode)
	}
	if resp, body := r.do("POST", "/api/computer/file", map[string]any{"path": "docs/new.txt", "text": "written"}); resp.StatusCode != 200 || box.files["docs/new.txt"] != "written" {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := r.do("POST", "/api/computer/file", map[string]any{"path": "it's here", "dir": true}); resp.StatusCode != 200 || box.ran[len(box.ran)-1] != `mkdir -p -- 'it'\''s here'` {
		t.Fatalf("making a folder ran %q", box.ran[len(box.ran)-1])
	}
	if resp, _ := r.do("POST", "/api/computer/file", map[string]any{"path": "a\nb", "text": "x"}); resp.StatusCode != 400 {
		t.Fatalf("a path with a line break answered %d", resp.StatusCode)
	}

	// A PDF moves out to Documents and back into a folder; anything else is refused by the store.
	box.files["report.pdf"] = string(files.GeneratePDF("Report", "numbers"))
	resp, body = r.do("POST", "/api/computer/transfer", map[string]string{"keep": "report.pdf"})
	var kept struct{ ID, Name string }
	decode(t, body, &kept)
	if resp.StatusCode != 200 || kept.ID == "" || kept.Name != "report.pdf" {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := r.do("POST", "/api/computer/transfer", map[string]string{"keep": "notes.txt"}); resp.StatusCode != 400 {
		t.Fatalf("a text file was kept as a document: %d", resp.StatusCode)
	}
	if resp, body := r.do("POST", "/api/computer/transfer", map[string]string{"put": kept.ID, "path": "docs"}); resp.StatusCode != 200 || box.files["docs/report.pdf"] != box.files["report.pdf"] {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestAKeptPDFIsShownInsideTheAppAndNowhereElse(t *testing.T) {
	r := newRig(t)
	r.srv.Files = &files.Store{Dir: t.TempDir()}
	f, err := r.srv.Files.Save("slip.pdf", files.GeneratePDF("Slip", "sign here"), files.OriginUpload)
	if err != nil {
		t.Fatal(err)
	}
	r.login()
	resp, _ := r.do("GET", "/api/files/"+f.ID, nil)
	if !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("a plain download changed: %v", resp.Header)
	}
	resp, _ = r.do("GET", "/api/files/"+f.ID+"?inline=1", nil)
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "inline") || resp.Header.Get("X-Frame-Options") != "SAMEORIGIN" ||
		!strings.Contains(csp, "frame-ancestors 'self'") || !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
		t.Fatalf("inline headers: %v", resp.Header)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("the type is not pinned: %v", resp.Header)
	}
	// A PDF with no form says so rather than failing.
	resp, body := r.do("GET", "/api/files/"+f.ID+"/form", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"fields":[]`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := r.do("POST", "/api/files/"+f.ID+"/form", map[string]any{"values": map[string]string{}}); resp.StatusCode != 400 {
		t.Fatalf("filling nothing answered %d", resp.StatusCode)
	}
}

func TestTakingControlNeedsATabTheAgentHasOpen(t *testing.T) {
	r := newRig(t)
	r.login()
	_, body := r.do("GET", "/api/browser", nil)
	if !strings.Contains(string(body), `"tabs":[]`) {
		t.Fatalf("%s", body)
	}
	for _, id := range []string{"", "0.agent-1", "x.y", "-1.a"} {
		if resp, _ := r.do("GET", "/api/browser/shot?id="+id, nil); resp.StatusCode != 404 {
			t.Errorf("shot of %q: %d", id, resp.StatusCode)
		}
		if resp, _ := r.do("POST", "/api/browser/act", map[string]any{"id": id, "kind": "click", "x": 1, "y": 1}); resp.StatusCode != 404 {
			t.Errorf("act on %q: %d", id, resp.StatusCode)
		}
	}
}

func TestSideChatsAndTheMainConversationOverHTTP(t *testing.T) {
	r := newRig(t)
	convos, err := OpenConvos(filepath.Join(t.TempDir(), "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.srv.Convos = convos
	r.login()
	main := convos.New()
	r.hub.AddUser("plan the trip")
	if resp, body := r.do("POST", "/api/chats", map[string]any{"side": true}); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if msgs, _, _ := r.state(); len(msgs) != 0 {
		t.Fatalf("a side chat starts blank, got %d messages", len(msgs))
	}
	_, body := r.do("GET", "/api/chats", nil)
	var got struct{ Chats []ConvoMeta }
	decode(t, body, &got)
	if len(got.Chats) != 2 || got.Chats[0].Side != "plan the trip" || !got.Chats[1].Main {
		t.Fatalf("%+v", got.Chats)
	}
	if resp, _ := r.do("POST", "/api/chats", map[string]any{"archive": main}); resp.StatusCode != 400 {
		t.Fatalf("archiving the main conversation answered %d", resp.StatusCode)
	}
	if resp, body := r.do("POST", "/api/chats", map[string]any{"open": "main"}); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if msgs, _, _ := r.state(); len(msgs) != 1 || msgs[0].Text != "plan the trip" || convos.ActiveID() != main {
		t.Fatalf("back to main shows %d messages", len(msgs))
	}
}

// The second script is held to the same rules as the first: it builds elements
// and text, never markup, keeps nothing on the phone, and is loaded before the
// script that calls into it.
func TestTheSecondScriptKeepsThePagesRules(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("static", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	html, js, sw := read("index.html"), read("more.js"), read("sw.js")
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "localStorage", "sessionStorage", "indexedDB", "setAttribute('style'"} {
		if strings.Contains(js, bad) {
			t.Errorf("more.js uses %s", bad)
		}
	}
	if i, j := strings.Index(html, `src="/more.js"`), strings.Index(html, `src="/app.js"`); i < 0 || j < i {
		t.Error("more.js must load before app.js, which calls its functions while starting")
	}
	if !strings.Contains(sw, "'/more.js'") {
		t.Error("the service worker does not keep more.js, so the app would not open offline")
	}
	for _, want := range []string{"function openComputer", "function openDoc", "function richResult", "function conversationsCard", "function paneYou", "function tasksCard", "URL.revokeObjectURL"} {
		if !strings.Contains(js, want) {
			t.Errorf("more.js lost %q", want)
		}
	}
	// A picture of the browser is asked for only while its screen is open: the timer is the panel's, and closing stops it.
	if !strings.Contains(js, "clearTimeout(PANEL.timer)") || !strings.Contains(js, "document.hidden") {
		t.Error("the browser picture would keep being fetched after its screen closed")
	}
}

// Light is the default: the page is dark only where the stylesheet is told to be, by a choice kept with the profile.
func TestLightIsTheDefaultAndDarkIsAChoice(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("static", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	css, html := read("app.css"), read("index.html")
	// A device set to dark must not turn the page dark by itself: every dark rule sits behind the attribute.
	for _, m := range regexp.MustCompile(`@media \(prefers-color-scheme:dark\)\{([^@]|@supports[^@])*`).FindAllString(css, -1) {
		if strings.Contains(m, ":root{") {
			t.Fatalf("a dark rule applies without the user's choice: %.120s", m)
		}
	}
	if !strings.Contains(css, ":root[data-theme=dark]{") || !strings.Contains(css, ":root[data-theme=auto]{") {
		t.Fatal("the dark theme cannot be chosen")
	}
	if strings.Contains(html, "prefers-color-scheme") || !strings.Contains(html, `<meta name="color-scheme" content="light">`) {
		t.Fatal("the page itself asks to follow the device")
	}

	r := newRig(t)
	r.srv.Notes = &memory.Notes{Dir: t.TempDir()}
	r.login()
	if _, body := r.do("GET", "/api/state", nil); !strings.Contains(string(body), `"theme":""`) {
		t.Fatalf("a new profile starts with no theme, which is light: %s", body)
	}
	if resp, body := r.do("POST", "/api/profile", map[string]any{"profile": map[string]string{"theme": "dark"}}); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if _, body := r.do("GET", "/api/state", nil); !strings.Contains(string(body), `"theme":"dark"`) {
		t.Fatalf("the choice did not reach the page: %s", body)
	}
	if resp, _ := r.do("POST", "/api/profile", map[string]any{"profile": map[string]string{"theme": "neon"}}); resp.StatusCode != 400 {
		t.Fatalf("an unknown theme answered %d", resp.StatusCode)
	}
}

// A profile saved with no key it can use says so at once, and one with a pasted key does not.
func TestSavingAProfileWithNoUsableKeySaysSo(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.srv.Getenv = func(string) string { return "" }
	r.login()
	_, body := r.do("POST", "/api/settings", map[string]any{"name": "meta-muse", "type": "muse", "model": "muse-spark-1.3", "api_key_env": "MODEL_API_KEY"})
	if !strings.Contains(string(body), "$MODEL_API_KEY is not set on the server") {
		t.Fatalf("no warning: %s", body)
	}
	_, body = r.do("POST", "/api/settings", map[string]any{"name": "meta-muse", "api_key": "LLM_1607358788850350_abcdefghijklmnopqrstuv"})
	if strings.Contains(string(body), "warning") {
		t.Fatalf("a pasted key is a usable key: %s", body)
	}
	if got := r.srv.Cfg.Profiles["meta-muse"]; got.APIKey == "" || got.APIKeyEnv != "MODEL_API_KEY" {
		t.Fatalf("the pasted key was not kept beside the variable's name: %+v", got)
	}
	// With the variable set where Talaria runs, nothing needs pasting.
	r.srv.Getenv = func(k string) string { return map[string]string{"MODEL_API_KEY": "LLM_1_fromtheenvironment"}[k] }
	_, body = r.do("POST", "/api/settings", map[string]any{"name": "from-env", "type": "muse", "api_key_env": "MODEL_API_KEY"})
	if strings.Contains(string(body), "warning") {
		t.Fatalf("%s", body)
	}
}

// Settings can ask a profile's provider which models its key can use, and says whether the profile's own is among them.
func TestSettingsListsTheModelsAKeyCanUse(t *testing.T) {
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/models" || req.Header.Get("Authorization") != "Bearer LLM_1607358788850350_abcdefghijklmnopqrstuv" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[{"id":"muse-spark-1.2"},{"id":"muse-ember-1.0"}]}`))
	}))
	defer models.Close()
	r := newRig(t)
	r.srv.Cfg = &config.Config{Profiles: map[string]config.Profile{
		"meta-muse": {Type: "muse", BaseURL: models.URL + "/v1", Model: "muse-spark-1.3", APIKey: "LLM_1607358788850350_abcdefghijklmnopqrstuv"},
		"keyless":   {Type: "muse"},
	}}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.srv.Getenv = func(string) string { return "" }
	r.login()
	resp, body := r.do("POST", "/api/settings", map[string]string{"list_models": "meta-muse"})
	var got struct {
		Models []string
		Model  string
		Listed bool
	}
	decode(t, body, &got)
	if resp.StatusCode != 200 || strings.Join(got.Models, ",") != "muse-ember-1.0,muse-spark-1.2" || got.Model != "muse-spark-1.3" || got.Listed {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "LLM_1607") {
		t.Fatal("the key came back with the list")
	}
	// Picking one is an ordinary edit of the profile's model.
	if resp, _ := r.do("POST", "/api/settings", map[string]string{"name": "meta-muse", "model": "muse-spark-1.2"}); resp.StatusCode != 200 || r.srv.Cfg.Profiles["meta-muse"].Model != "muse-spark-1.2" {
		t.Fatalf("%d %+v", resp.StatusCode, r.srv.Cfg.Profiles["meta-muse"])
	}
	if resp, body := r.do("POST", "/api/settings", map[string]string{"list_models": "keyless"}); resp.StatusCode != 400 || !strings.Contains(string(body), "MODEL_API_KEY") {
		t.Fatalf("a profile with no key: %d %s", resp.StatusCode, body)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]string{"list_models": "nope"}); resp.StatusCode != 400 {
		t.Fatalf("an unknown profile: %d", resp.StatusCode)
	}
}
