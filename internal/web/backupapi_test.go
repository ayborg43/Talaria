package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/backup"
	"talaria/internal/config"
	"talaria/internal/digest"
	"talaria/internal/provider"
	"talaria/internal/usage"
)

func (r *rig) raw(method, path string, body io.Reader) (*http.Response, []byte) {
	r.t.Helper()
	req, _ := http.NewRequest(method, r.ts.URL+path, body)
	req.Header.Set("X-Talaria", "1")
	resp, err := r.c.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestBackupDownloadAndRestoreUpload(t *testing.T) {
	r := newRig(t)
	home := filepath.Join(t.TempDir(), "home")
	os.MkdirAll(home, 0o700)
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"x":1}`), 0o600)
	os.WriteFile(filepath.Join(home, "vault.key"), []byte("SECRET-KEY"), 0o600)
	r.srv.Home = home

	if resp, _ := r.do("GET", "/api/backup", nil); resp.StatusCode != 401 {
		t.Fatalf("a backup needs a sign-in: %d", resp.StatusCode)
	}
	r.login()
	resp, arc := r.do("GET", "/api/backup", nil)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), "talaria-backup-") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	if bytes.Contains(arc, []byte("SECRET-KEY")) {
		t.Fatal("the key is in plain bytes")
	}
	if n, _, err := backup.Verify(bytes.NewReader(arc)); err != nil || n == 0 {
		t.Fatalf("the download is not a backup: %d %v", n, err)
	}

	// Junk is refused and leaves nothing waiting.
	if resp, body := r.raw("POST", "/api/restore", strings.NewReader("junk")); resp.StatusCode != 400 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(home, backup.PendingName)); !os.IsNotExist(err) {
		t.Fatal("junk was kept")
	}
	// A real backup waits for the next start, and changes nothing now.
	resp, body := r.raw("POST", "/api/restore", bytes.NewReader(arc))
	var out struct {
		OK      bool        `json:"ok"`
		Restore restoreView `json:"restore"`
		Restart bool        `json:"restart_needed"`
	}
	json.Unmarshal(body, &out)
	if resp.StatusCode != 200 || !out.OK || !out.Restart || !out.Restore.Pending {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "config.json")); string(b) != `{"x":1}` {
		t.Fatal("state changed before a restart")
	}
	_, body = r.do("GET", "/api/restore", nil)
	var v restoreView
	json.Unmarshal(body, &v)
	if !v.Pending {
		t.Fatalf("%s", body)
	}
	resp, body = r.raw("POST", "/api/restore?cancel=1", nil)
	json.Unmarshal(body, &v)
	if resp.StatusCode != 200 || v.Pending {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestBackupIsOffWithoutAHome(t *testing.T) {
	r := newRig(t)
	r.login()
	if resp, _ := r.do("GET", "/api/backup", nil); resp.StatusCode != 501 {
		t.Fatal(resp.StatusCode)
	}
	_, body := r.do("GET", "/api/restore", nil)
	var v restoreView
	json.Unmarshal(body, &v)
	if v.Available {
		t.Fatal("available without a home")
	}
}

func TestDigestEndpoints(t *testing.T) {
	r := newRig(t)
	r.login()
	_, body := r.do("GET", "/api/digest", nil)
	if strings.Contains(string(body), `"available":true`) {
		t.Fatalf("on without hooks: %s", body)
	}
	var set struct {
		Enabled bool
		At      string
		Weekly  bool
	}
	r.srv.Sources.Digest = func() DigestInfo { return DigestInfo{Enabled: set.Enabled, At: set.At, Weekly: set.Weekly} }
	r.srv.Sources.SetDigest = func(on bool, at string, weekly bool) error {
		set.Enabled, set.At, set.Weekly = on, at, weekly
		return nil
	}
	r.srv.Sources.DigestNow = func(span time.Duration) string {
		return digest.Build(digest.Input{Since: time.Now().Add(-span), Until: time.Now()}).Text
	}
	if resp, _ := r.do("POST", "/api/digest", map[string]any{"enabled": true, "at": "25:99"}); resp.StatusCode != 400 {
		t.Fatalf("a bad time was accepted: %d", resp.StatusCode)
	}
	if resp, body := r.do("POST", "/api/digest", map[string]any{"enabled": true, "at": "07:15", "weekly": true}); resp.StatusCode != 200 || !set.Enabled || set.At != "07:15" || !set.Weekly {
		t.Fatalf("%d %s %+v", resp.StatusCode, body, set)
	}
	_, body = r.do("GET", "/api/digest?hours=24", nil)
	if !strings.Contains(string(body), "Nothing happened on its own in the last 24 hours") {
		t.Fatalf("%s", body)
	}
}

func TestUsageEndpointsAndPrices(t *testing.T) {
	r := newRig(t)
	r.login()
	if _, body := r.do("GET", "/api/usage", nil); !strings.Contains(string(body), `"available":false`) {
		t.Fatalf("%s", body)
	}
	dir := t.TempDir()
	r.srv.Usage = usage.Open(filepath.Join(dir, "usage.json"), time.UTC)
	r.srv.Cfg = &config.Config{Profiles: map[string]config.Profile{"strong": {Model: "big"}, "cheap": {Model: "small"}}}
	r.srv.ConfigPath = filepath.Join(dir, "config.json")
	r.srv.Usage.Add(provider.Spent{Profile: "strong", Model: "big", Usage: provider.Usage{In: 1_000_000, Out: 100_000}, Label: provider.Label{Source: "chat", Kind: "code"}})

	if resp, body := r.do("POST", "/api/usage", map[string]any{"profile": "nope", "price_in": 1}); resp.StatusCode != 400 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := r.do("POST", "/api/usage", map[string]any{"profile": "strong", "price_in": -3}); resp.StatusCode != 400 {
		t.Fatalf("a negative price was accepted: %d", resp.StatusCode)
	}
	resp, body := r.do("POST", "/api/usage", map[string]any{"profile": "strong", "price_in": 3, "price_out": 15})
	var got struct {
		Report  usage.Report `json:"report"`
		Summary string       `json:"summary"`
	}
	json.Unmarshal(body, &got)
	if resp.StatusCode != 200 || got.Report.Total.Cost != 4.5 || !strings.Contains(got.Summary, "$4.50") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if saved, err := config.Load(r.srv.ConfigPath); err != nil {
		t.Fatalf("the saved config does not load: %v", err)
	} else if in, out, ok := saved.Price("strong"); !ok || in != 3 || out != 15 {
		t.Fatalf("the price was not saved: %v %v %v", in, out, ok)
	}
	// Both blank forgets it.
	if resp, _ := r.do("POST", "/api/usage", map[string]any{"profile": "strong"}); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if _, _, ok := r.srv.Cfg.Price("strong"); ok {
		t.Fatal("a cleared price is still there")
	}
	// With no profiles in the config, the implicit "default" can be priced.
	r.srv.Cfg = &config.Config{}
	r.srv.Model = "some-model"
	if resp, body := r.do("POST", "/api/usage", map[string]any{"profile": "default", "price_in": 1, "price_out": 2}); resp.StatusCode != 200 || !strings.Contains(string(body), `"price_in":1`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestTrajectoryExportIsScrubbedJSONL(t *testing.T) {
	r := newRig(t)
	r.login()
	if resp, _ := r.do("GET", "/api/trajectories", nil); resp.StatusCode != 404 {
		t.Fatalf("without conversations: %d", resp.StatusCode)
	}
	convos, err := OpenConvos(filepath.Join(t.TempDir(), "chats.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.srv.Convos = convos
	r.srv.Scrub = func(s string) string { return strings.ReplaceAll(s, "HUNTER2", "[redacted]") }
	id := convos.New()
	convos.Save(id, nil, []provider.Message{
		{Role: provider.RoleUser, Content: "the password is HUNTER2"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "a", Name: "shell", Arguments: `{"command":"ls"}`}}},
		{Role: provider.RoleTool, ToolCallID: "a", Content: "files"},
		{Role: provider.RoleAssistant, Content: "done"},
	})
	resp, body := r.do("GET", "/api/trajectories?min_tools=1", nil)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "ndjson") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
	if strings.Contains(string(body), "HUNTER2") || !strings.Contains(string(body), `"tool_calls"`) {
		t.Fatalf("%s", body)
	}
	if resp, _ := r.do("GET", "/api/trajectories?since=yesterday", nil); resp.StatusCode != 400 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestSpendingLimitsSavedAndShown(t *testing.T) {
	r := newRig(t)
	r.login()
	dir := t.TempDir()
	r.srv.Cfg = &config.Config{Profiles: map[string]config.Profile{"strong": {Model: "big"}}, Prices: map[string]config.ModelPrice{"strong": {In: 3}}}
	r.srv.ConfigPath = filepath.Join(dir, "config.json")
	r.srv.Usage = usage.Open(filepath.Join(dir, "usage.json"), time.UTC)
	cfg := r.srv.Cfg
	r.srv.Spend = &usage.Cap{Ledger: r.srv.Usage, Prices: r.srv.usagePrices(),
		Limits: func() usage.Limits { return usage.Limits{Day: cfg.Limits.SpendPerDay, Month: cfg.Limits.SpendPerMonth} }}
	r.srv.Usage.Add(provider.Spent{Profile: "strong", Usage: provider.Usage{In: 1_000_000}}) // $3

	if resp, _ := r.do("POST", "/api/usage/limits", map[string]any{"day": -1}); resp.StatusCode != 400 {
		t.Fatalf("a negative limit was accepted: %d", resp.StatusCode)
	}
	resp, body := r.do("POST", "/api/usage/limits", map[string]any{"day": 2, "month": 50})
	var got struct {
		Spend usage.Status `json:"spend"`
	}
	json.Unmarshal(body, &got)
	if resp.StatusCode != 200 || !got.Spend.Paused || got.Spend.DayLimit != 2 || got.Spend.MonthLimit != 50 || got.Spend.Day != 3 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if saved, err := config.Load(r.srv.ConfigPath); err != nil || saved.Limits.SpendPerDay != 2 || saved.Limits.SpendPerMonth != 50 {
		t.Fatalf("not saved: %v %+v", err, saved)
	}
	// Blank removes them.
	if _, body := r.do("POST", "/api/usage/limits", map[string]any{}); strings.Contains(string(body), `"paused":true`) {
		t.Fatalf("%s", body)
	}
}
