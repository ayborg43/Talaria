package web

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/config"
)

func rulesRig(t *testing.T) (*rig, *map[string]string) {
	t.Helper()
	r := newRig(t)
	r.srv.Cfg = &config.Config{}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	live := map[string]string{}
	r.srv.ApplyRules = func(rules map[string]string) error {
		for _, v := range rules {
			if v == "bogus" {
				return errors.New("rule is wrong")
			}
		}
		live = rules
		return nil
	}
	r.login()
	return r, &live
}

func readRules(t *testing.T, body []byte) rulesView {
	t.Helper()
	var wrap struct {
		Rules rulesView `json:"rules"`
	}
	var direct rulesView
	if err := json.Unmarshal(body, &wrap); err == nil && len(wrap.Rules.Categories) > 0 {
		return wrap.Rules
	}
	if err := json.Unmarshal(body, &direct); err != nil || len(direct.Categories) == 0 {
		t.Fatalf("not a rules view: %s", body)
	}
	return direct
}

func modeOf(v rulesView, id string) string {
	for _, c := range v.Categories {
		if c.ID == id {
			return c.Mode
		}
	}
	return "?"
}

func TestTheRulesPageSetsAKindOfActionAndAppliesItAtOnce(t *testing.T) {
	r, live := rulesRig(t)
	_, body := r.do("GET", "/api/rules", nil)
	v := readRules(t, body)
	if modeOf(v, "email") != "default" || !v.Live || len(v.Modes) != 5 {
		t.Fatalf("a fresh install: %+v", v)
	}
	resp, body := r.do("POST", "/api/rules", map[string]any{"category": "email", "mode": "handoff"})
	if resp.StatusCode != 200 || modeOf(readRules(t, body), "email") != "handoff" || strings.Contains(string(body), "restart_needed") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if (*live)["email_send"] != "handoff" || (*live)["email_reply"] != "handoff" {
		t.Fatalf("the running policy was not changed: %v", *live)
	}
	raw, _ := os.ReadFile(r.srv.ConfigPath)
	if !strings.Contains(string(raw), `"email_send": "handoff"`) {
		t.Fatalf("not saved: %s", raw)
	}
	// back to default removes the rules again, everywhere
	r.do("POST", "/api/rules", map[string]any{"category": "email", "mode": "default"})
	if _, has := (*live)["email_send"]; has {
		t.Fatalf("default left a rule behind: %v", *live)
	}
}

func TestTheRulesPageRefusesNonsenseAndKeepsWhatItCannotApply(t *testing.T) {
	r, live := rulesRig(t)
	for name, in := range map[string]map[string]any{
		"unknown kind": {"category": "everything", "mode": "allow"},
		"unknown mode": {"category": "email", "mode": "sometimes"},
		"empty":        {},
	} {
		if resp, _ := r.do("POST", "/api/rules", in); resp.StatusCode != 400 {
			t.Errorf("%s answered %d", name, resp.StatusCode)
		}
	}
	if len(*live) != 0 || len(r.srv.Cfg.Policy.Rules) != 0 {
		t.Fatalf("a refused request changed something: %v %v", *live, r.srv.Cfg.Policy.Rules)
	}
	// rules the config already holds by hand are shown, and left alone by a change to a category
	r.srv.Cfg.Policy.Rules = map[string]string{"mcp_mail_*": "ask", "email_send": "bogus"}
	if resp, _ := r.do("POST", "/api/rules", map[string]any{"category": "email", "mode": "ask"}); resp.StatusCode != 200 {
		t.Fatalf("a good change was refused: %d", resp.StatusCode)
	}
	_, body := r.do("GET", "/api/rules", nil)
	if v := readRules(t, body); v.Other["mcp_mail_*"] != "ask" || modeOf(v, "email") != "ask" {
		t.Fatalf("%+v", v)
	}
}

func TestWithNoLivePolicyAChangeSaysARestartIsNeeded(t *testing.T) {
	r, _ := rulesRig(t)
	r.srv.ApplyRules = nil
	resp, body := r.do("POST", "/api/rules", map[string]any{"category": "social", "mode": "deny"})
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"restart_needed":true`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}
