package guard

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/agent"
)

func mustPolicy(t *testing.T, rules map[string]string, noRemember []string) *Policy {
	t.Helper()
	p, err := ParsePolicy(rules, noRemember)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPolicyRulePrecedence(t *testing.T) {
	p := mustPolicy(t, map[string]string{
		"mcp_*":         "ask",
		"mcp_mail_*":    "deny",
		"mcp_mail_list": "allow",
		"read_file":     "allow",
	}, nil)
	g := &Guard{Policy: p}
	s := g.For("chat", "telegram", 1)
	for tool, want := range map[string]agent.Action{
		"mcp_mail_list": agent.Allow, // an exact name beats any family
		"mcp_mail_send": agent.Deny,  // the longer family beats the shorter
		"mcp_files_get": agent.Ask,
		"read_file":     agent.Allow,
	} {
		if got := s.Verdict(tool, false); got.Action != want || got.Via != "rule" {
			t.Errorf("%s: %+v, want %v by rule", tool, got, want)
		}
	}
	// With no rule, the tool's own default stands.
	if v := s.Verdict("write_file", true); v.Action != agent.Ask || v.Via != "auto" {
		t.Errorf("%+v", v)
	}
	if v := s.Verdict("list_dir", false); v.Action != agent.Allow || v.Via != "auto" {
		t.Errorf("%+v", v)
	}
	// A rule may also make an ordinarily free tool ask, or an asking tool free.
	g2 := &Guard{Policy: mustPolicy(t, map[string]string{"list_dir": "ask", "write_file": "allow"}, nil)}
	s2 := g2.For("cli", "", 0)
	if s2.Verdict("list_dir", false).Action != agent.Ask || s2.Verdict("write_file", true).Action != agent.Allow {
		t.Fatal("rules override a tool's default in both directions")
	}
}

func TestPolicyRejectsBadRules(t *testing.T) {
	for name, rules := range map[string]map[string]string{
		"unknown verdict": {"shell": "maybe"},
		"star in middle":  {"mcp_*_send": "deny"},
		"two stars":       {"a**": "deny"},
		"blank name":      {"": "deny"},
		"padded name":     {" shell": "deny"},
		"misspelt tool":   {"wriet_file": "deny"},
		"misspelt family": {"wriet_*": "deny"},
		"unknown tool":    {"send_email": "deny"},
		"bare prefix":     {"read": "deny"},
	} {
		if _, err := ParsePolicy(rules, nil); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
	if _, err := ParsePolicy(map[string]string{"shell": " DENY "}, nil); err != nil {
		t.Errorf("verdicts are case-insensitive: %v", err)
	}
	// A connected service's tools cannot be listed at load, and a lone *
	// covers everything on purpose; all of these are accepted.
	for _, name := range []string{"mcp_mail_send", "mcp_*", "mcp_mail_*", "browser_*", "*", "read_file"} {
		if _, err := ParsePolicy(map[string]string{name: "deny"}, nil); err != nil {
			t.Errorf("%s should be accepted: %v", name, err)
		}
	}
}

// The rule-name check only works if knownTools names every tool Talaria
// registers. Registrations live across packages, so this test reads them back
// out of the source: every Name given to a registry must be known, and nothing
// else may be.
func TestKnownToolsMatchesTheRegisteredTools(t *testing.T) {
	root := ".."
	registered := map[string]bool{}
	nameRe := regexp.MustCompile(`(?m)^\s*Name:\s*"([a-z][a-z0-9_]*)"`)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), "Register(") {
			return nil
		}
		for _, m := range nameRe.FindAllStringSubmatch(string(src), -1) {
			registered[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(registered) == 0 {
		t.Fatal("found no registrations; the extractor is broken")
	}
	for name := range registered {
		if !knownTools[name] {
			t.Errorf("tool %q is registered but not known to the rule check", name)
		}
	}
	for name := range knownTools {
		if !registered[name] {
			t.Errorf("tool %q is known to the rule check but never registered", name)
		}
	}
}

func TestGrantsAllowOnlyTheChatAndToolTheyWereMadeFor(t *testing.T) {
	dir := t.TempDir()
	g := &Guard{Policy: mustPolicy(t, nil, []string{"shell"}), Grants: &Grants{Path: filepath.Join(dir, "grants.json")}}
	a := g.For("chat", "telegram", 1)
	if a.Verdict("write_file", true).Action != agent.Ask {
		t.Fatal("nothing is granted yet")
	}
	if err := a.Remember("write_file"); err != nil {
		t.Fatal(err)
	}
	if v := a.Verdict("write_file", true); v.Action != agent.Allow || v.Via != "grant" {
		t.Fatalf("%+v", v)
	}
	for name, s := range map[string]*Scope{
		"another chat":    g.For("chat", "telegram", 2),
		"another service": g.For("chat", "discord", 1),
		"the CLI":         g.For("cli", "", 0),
		"a job":           g.For("job:x", "telegram", 1), // unattended: no grants
	} {
		if s.Verdict("write_file", true).Action != agent.Ask {
			t.Errorf("%s must still be asked", name)
		}
	}
	if a.Verdict("skill_delete", true).Action != agent.Ask {
		t.Error("a grant covers one tool")
	}
	if fi, _ := os.Stat(g.Grants.Path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

// A rule written in the config always wins over what a user clicked.
func TestRulesBeatGrants(t *testing.T) {
	dir := t.TempDir()
	grants := &Grants{Path: filepath.Join(dir, "g.json")}
	grants.Add("telegram:1", "write_file")
	g := &Guard{Policy: mustPolicy(t, map[string]string{"write_file": "deny"}, nil), Grants: grants}
	if v := g.For("chat", "telegram", 1).Verdict("write_file", true); v.Action != agent.Deny {
		t.Fatalf("%+v", v)
	}
}

func TestRememberable(t *testing.T) {
	g := &Guard{Policy: mustPolicy(t, map[string]string{"cron_add": "ask", "read_*": "allow"}, nil), Grants: &Grants{Path: filepath.Join(t.TempDir(), "g")}}
	p := ChatPermissions{G: g, Gateway: "telegram"}
	for tool, want := range map[string]bool{
		"skill_view": true,
		"shell":      false, // kept out by default
		"cron_add":   false, // the config says always ask
		"read_file":  false, // the config already decides
	} {
		if got := p.Rememberable(tool); got != want {
			t.Errorf("%s: %v, want %v", tool, got, want)
		}
	}
	// The config can allow remembering shell, or forbid more.
	g.Policy = mustPolicy(t, nil, []string{})
	if !p.Rememberable("shell") {
		t.Error("an empty no_remember list lets shell be remembered")
	}
	g.Policy = mustPolicy(t, nil, []string{"write_file"})
	if p.Rememberable("write_file") || !p.Rememberable("shell") {
		t.Error("no_remember replaces the default")
	}
	// Nowhere to keep grants: nothing is offered.
	g.Grants = nil
	if p.Rememberable("write_file") {
		t.Error("without a grants file there is nothing to remember into")
	}
}

func TestGrantsFileIsRereadWhenItChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "g.json")
	g := &Grants{Path: path}
	g.Add("telegram:1", "write_file")
	if !g.Allowed("telegram:1", "write_file") {
		t.Fatal("not granted")
	}
	// Another process (tal policy forget) edits the file.
	other := &Grants{Path: path}
	if n, err := other.Reset("telegram:1"); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if g.Allowed("telegram:1", "write_file") {
		t.Fatal("the running gateway must notice the grant is gone")
	}
	// An unreadable file grants nothing.
	os.WriteFile(path, []byte("{not json"), 0o600)
	if g.Allowed("telegram:1", "write_file") {
		t.Fatal("a corrupt file must fail closed")
	}
	if err := g.Add("telegram:1", "x"); err == nil {
		t.Fatal("and must not be silently overwritten")
	}
}

func TestChatPermissionsDescribeAndReset(t *testing.T) {
	g := &Guard{Policy: mustPolicy(t, map[string]string{"shell": "ask"}, nil), Grants: &Grants{Path: filepath.Join(t.TempDir(), "g")}}
	p := ChatPermissions{G: g, Gateway: "discord"}
	p.Remember(7, "write_file")
	p.Remember(8, "skill_save")
	d := p.Describe(7)
	if !strings.Contains(d, "shell: ask") || !strings.Contains(d, "write_file") || strings.Contains(d, "skill_save") {
		t.Fatalf("%q", d)
	}
	if n, err := p.Reset(7); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if !strings.Contains(p.Describe(7), "none") || !strings.Contains(p.Describe(8), "skill_save") {
		t.Fatal("reset affects only that chat")
	}
}

func TestAuditLogRecordsEveryCall(t *testing.T) {
	g := &Guard{Audit: &Log{Path: filepath.Join(t.TempDir(), "audit.log")}, Redact: NewRedactor([]string{"hunter2hunter2"})}
	s := g.For("chat", "telegram", 5)
	s.Record(agent.Entry{Tool: "shell", Args: `{"command":"echo hunter2hunter2"}`, Allowed: true, Via: "prompt", Status: "ok", Result: "hunter2hunter2\n", Took: 12 * time.Millisecond})
	s.Record(agent.Entry{Tool: "write_file", Args: `{"path":"x"}`, Via: "rule", Rule: "write_file = deny", Status: "not run", Result: "error: the permission rules do not allow this tool"})
	g.For("chat", "telegram", 6).Record(agent.Entry{Tool: "read_file", Allowed: true, Via: "auto", Status: "ok"})

	recs, err := g.Audit.Recent(Filter{Gateway: "telegram", Chat: 5}, 10)
	if err != nil || len(recs) != 2 {
		t.Fatalf("%+v %v", recs, err)
	}
	if recs[0].Tool != "shell" || !recs[0].Allowed || recs[0].Via != "prompt" || recs[0].Millis != 12 || recs[0].Chat != 5 {
		t.Fatalf("%+v", recs[0])
	}
	if recs[1].Allowed || recs[1].Rule != "write_file = deny" {
		t.Fatalf("%+v", recs[1])
	}
	raw, _ := os.ReadFile(g.Audit.Path)
	if strings.Contains(string(raw), "hunter2hunter2") {
		t.Fatalf("a secret reached the log: %s", raw)
	}
	if fi, _ := os.Stat(g.Audit.Path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}

	if denied, _ := g.Audit.Recent(Filter{Denied: true}, 10); len(denied) != 1 || denied[0].Tool != "write_file" {
		t.Fatalf("%+v", denied)
	}
	if one, _ := g.Audit.Recent(Filter{}, 1); len(one) != 1 || one[0].Tool != "read_file" {
		t.Fatalf("n limits to the newest: %+v", one)
	}
	if got := Format(recs[1]); !strings.Contains(got, "REFUSED") || !strings.Contains(got, "telegram:5") || !strings.Contains(got, "write_file = deny") {
		t.Fatalf("%q", got)
	}
}

func TestAuditLogKeepsArgumentsAndResultsShort(t *testing.T) {
	g := &Guard{Audit: &Log{Path: filepath.Join(t.TempDir(), "audit.log")}}
	g.For("cli", "", 0).Record(agent.Entry{Tool: "write_file", Args: strings.Repeat("a", 5000), Result: strings.Repeat("b", 5000), Allowed: true, Status: "ok"})
	recs, _ := g.Audit.Recent(Filter{}, 1)
	if n := len([]rune(recs[0].Args)); n > maxArgsShown+1 {
		t.Fatalf("args kept %d runes", n)
	}
	if n := len([]rune(recs[0].Result)); n > maxResultKept+1 {
		t.Fatalf("result kept %d runes", n)
	}
	if recs[0].Bytes != 5000 {
		t.Fatalf("the full size is still noted: %d", recs[0].Bytes)
	}
}

func TestAuditLogRotates(t *testing.T) {
	l := &Log{Path: filepath.Join(t.TempDir(), "audit.log"), MaxBytes: 2000}
	for i := 0; i < 40; i++ {
		l.Append(Record{Time: time.Now(), Origin: "cli", Tool: "t", Args: strings.Repeat("x", 80), Allowed: true})
	}
	if _, err := os.Stat(l.Path + ".1"); err != nil {
		t.Fatal("the log should have been rotated")
	}
	if fi, _ := os.Stat(l.Path); fi.Size() > 4000 {
		t.Fatalf("the live log grew to %d bytes", fi.Size())
	}
	recs, _ := l.Recent(Filter{}, 0)
	if len(recs) < 10 || len(recs) > 40 {
		t.Fatalf("recent history should span the rotation: %d records", len(recs))
	}
}

func TestAuditLogToleratesAHalfWrittenLine(t *testing.T) {
	l := &Log{Path: filepath.Join(t.TempDir(), "audit.log")}
	l.Append(Record{Time: time.Now(), Origin: "cli", Tool: "a", Allowed: true})
	f, _ := os.OpenFile(l.Path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"time":"2026-`) // a crash mid-write
	f.Close()
	l.Append(Record{Time: time.Now(), Origin: "cli", Tool: "b", Allowed: true})
	recs, err := l.Recent(Filter{}, 10)
	if err != nil || len(recs) != 2 {
		t.Fatalf("%+v %v", recs, err)
	}
}

func TestAuditLogIsSafeForConcurrentWriters(t *testing.T) {
	l := &Log{Path: filepath.Join(t.TempDir(), "audit.log")}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				l.Append(Record{Time: time.Now(), Origin: "cli", Tool: "t", Allowed: true})
			}
		}()
	}
	wg.Wait()
	if recs, _ := l.Recent(Filter{}, 0); len(recs) != 200 {
		t.Fatalf("%d records", len(recs))
	}
	raw, _ := os.ReadFile(l.Path)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("interleaved write: %q", line)
		}
	}
}

func TestRedactorHidesSecretsAndTheirEscapedForms(t *testing.T) {
	r := NewRedactor([]string{"s3cret-Value/with+chars&more", "short", "  ", "another-secret-key-1234"})
	for name, in := range map[string]string{
		"plain":    "key is s3cret-Value/with+chars&more ok",
		"json":     string(must(json.Marshal(map[string]string{"k": "s3cret-Value/with+chars&more"}))),
		"urlquery": "?k=" + "s3cret-Value%2Fwith%2Bchars%26more",
		"second":   "another-secret-key-1234",
	} {
		out := r.Scrub(in)
		if strings.Contains(out, "s3cret") || strings.Contains(out, "another-secret") || !strings.Contains(out, Mask) {
			t.Errorf("%s: %q", name, out)
		}
	}
	// Too-short values would mangle ordinary text, so they are not treated as secrets.
	if got := r.Scrub("a short answer"); got != "a short answer" {
		t.Fatalf("%q", got)
	}
	// Nothing to hide: text passes through untouched.
	if got := (*Redactor)(nil).Scrub("x"); got != "x" {
		t.Fatal("a nil Redactor scrubs nothing")
	}
}

func TestRedactorLongestSecretWinsOverOneInsideIt(t *testing.T) {
	r := NewRedactor([]string{"abcdefgh", "abcdefgh-and-more-1234"})
	if got := r.Scrub("x abcdefgh-and-more-1234 y"); got != "x "+Mask+" y" {
		t.Fatalf("%q", got)
	}
}

func TestRedactorRecognisesTokenShapes(t *testing.T) {
	r := NewRedactor(nil)
	for name, secret := range map[string]string{
		"openai":    "sk-proj-abcdefghijklmnopqrstuvwx",
		"anthropic": "sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
		"github":    "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"slack":     "xoxb-1234567890-abcdefghij",
		"aws":       "AKIAABCDEFGHIJKLMNOP",
		"google":    "AIzaSyA-abcdefghijklmnopqrstuvwxyz01234",
		"jwt":       "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnop",
		"telegram":  "123456789:AAEhBOweik6ad9r_QXMENQjcrTu-Qly7yOk",
		"discord":   "MTIzNDU2Nzg5MDEyMzQ1Njc4" + ".GabcDE." + "abcdefghijklmnopqrstuvwxyz0", // split so a secret scanner does not mistake this fixture for a token
		"bearer":    "Bearer abcdefghijklmnopqrstuvwxyz0123456789",
		"pem":       "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----",
	} {
		out := r.Scrub("before " + secret + " after")
		if out != "before "+Mask+" after" {
			t.Errorf("%s: %q", name, out)
		}
	}
	// And leaves ordinary text and code alone.
	for _, plain := range []string{
		"the sky is blue", "git commit -m 'fix: sk-learn import'", "see https://example.com/path?x=1", "version 1.2.3-rc1",
		"12:34:56 is the time", "AKIA is a prefix", "token_count = 42", "a Bearer of news",
	} {
		if got := r.Scrub(plain); got != plain {
			t.Errorf("%q was changed to %q", plain, got)
		}
	}
}

func TestEnvSecrets(t *testing.T) {
	got := EnvSecrets([]string{
		"OPENAI_API_KEY=sk-aaaaaaaaaaaaaaaa", "GITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxx", "DB_PASSWORD=correct horse",
		"PATH=/usr/bin:/bin", "HOME=/home/me", "SHORT_KEY=abc", "MY_SECRETS=twelve-chars!", "KEYBOARD=qwertyuiop", "NOEQUALS",
	})
	want := map[string]bool{"sk-aaaaaaaaaaaaaaaa": true, "ghp_xxxxxxxxxxxxxxxxxxxx": true, "correct horse": true, "twelve-chars!": true}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for _, v := range got {
		if !want[v] {
			t.Errorf("unexpected %q", v)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestRedactorLearnsNewSecretsWhileInUse(t *testing.T) {
	r := NewRedactor([]string{"first-secret-value"})
	if got := r.Scrub("x later-secret-value y"); got != "x later-secret-value y" {
		t.Fatalf("not yet known: %q", got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r.Scrub("some text with first-secret-value in it")
				r.Add("later-secret-value")
			}
		}()
	}
	wg.Wait()
	if got := r.Scrub("x later-secret-value and first-secret-value y"); got != "x "+Mask+" and "+Mask+" y" {
		t.Fatalf("%q", got)
	}
	(*Redactor)(nil).Add("ignored") // harmless
	r.Add("short", "")              // ignored: too short to be a secret
	if got := r.Scrub("short"); got != "short" {
		t.Fatalf("%q", got)
	}
}

func TestNeverRememberAddsToWhatIsKeptOutOfAlwaysAllow(t *testing.T) {
	g := &Guard{Policy: mustPolicy(t, nil, nil), Grants: &Grants{Path: filepath.Join(t.TempDir(), "g")}}
	p := ChatPermissions{G: g, Gateway: "telegram"}
	if !p.Rememberable("mcp_mail_send_email") {
		t.Fatal("remembering is allowed to start with")
	}
	g.Policy.NeverRemember("mcp_mail_send_email", "mcp_mail_delete_event")
	if p.Rememberable("mcp_mail_send_email") || p.Rememberable("mcp_mail_delete_event") || !p.Rememberable("mcp_mail_create_draft") {
		t.Fatal("only the tools named are kept out")
	}
	(*Policy)(nil).NeverRemember("x") // harmless
	// A rule still decides first.
	g.Policy = mustPolicy(t, map[string]string{"mcp_mail_*": "allow"}, nil)
	if p.Rememberable("mcp_mail_create_draft") {
		t.Fatal("a rule has already decided")
	}
}

func TestToolsWithLastingEffectsAreNeverRememberedByDefault(t *testing.T) {
	g := &Guard{Policy: mustPolicy(t, nil, nil), Grants: &Grants{Path: filepath.Join(t.TempDir(), "g")}}
	p := ChatPermissions{G: g, Gateway: "telegram"}
	for _, tool := range []string{"shell", "write_file", "skill_save", "cron_add", "task_start", "goal_create", "watch_create", "watch_update", "goal_update"} {
		if p.Rememberable(tool) {
			t.Errorf("%s: a choice to always allow it would let it run later, or unattended, unseen", tool)
		}
	}
	if !p.Rememberable("skill_delete") {
		t.Error("the list is not everything")
	}
}

// The CLI and the gateway share the grants file through separate handles
// (separate locks, possibly separate processes). Concurrent edits must lose
// neither.
func TestConcurrentGrantEditsFromTwoHandlesLoseNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	a := &Grants{Path: path}
	b := &Grants{Path: path}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); a.Add("telegram:1", fmt.Sprintf("tool_a_%d", i)) }()
		go func() { defer wg.Done(); b.Add("telegram:1", fmt.Sprintf("tool_b_%d", i)) }()
	}
	wg.Wait()
	if got := a.List("telegram:1"); len(got) != 40 {
		t.Fatalf("%d grants survived, want 40: %v", len(got), got)
	}
}

// A grant is a line in a file. A file the agent was able to write, or one made
// before a tool was added to no_remember, must not get round that list.
func TestAGrantForAToolThatCannotBeRememberedIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	grants := &Grants{Path: path}
	for _, tool := range []string{"shell", "write_file", "skill_delete"} {
		if err := grants.Add("telegram:1", tool); err != nil {
			t.Fatal(err)
		}
	}
	g := &Guard{Policy: mustPolicy(t, nil, nil), Grants: grants}
	s := g.For("chat", "telegram", 1)
	for _, tool := range []string{"shell", "write_file"} {
		if v := s.Verdict(tool, true); v.Action != agent.Ask {
			t.Errorf("%s: %+v", tool, v)
		}
	}
	if v := s.Verdict("skill_delete", true); v.Action != agent.Allow || v.Via != "grant" {
		t.Errorf("an ordinary grant still works: %+v", v)
	}
}
