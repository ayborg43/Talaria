package web

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/config"
	"talaria/internal/provider"
)

func fallbackRig(t *testing.T) *rig {
	r := newRig(t)
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.srv.Cfg = &config.Config{Profiles: map[string]config.Profile{
		"main": {Type: "openai", Model: "m1", APIKey: "k1"},
		"free": {Type: "openai", BaseURL: "https://api.iteracompute.com/v1", Model: "qwen/qwen3.8-27b", APIKey: "k2"},
		"nv":   {Type: "openai", BaseURL: "https://integrate.api.nvidia.com/v1", Model: "nvidia/nemotron-3-super-120b-a12b", APIKey: "k3"},
	}}
	r.srv.Health = provider.NewHealth()
	r.login()
	return r
}

type settingsReply struct {
	FallbackOrder []string `json:"fallback_order"`
	Presets       []struct{ Label string }
	Profiles      []struct {
		Name        string   `json:"name"`
		Notes       string   `json:"notes"`
		Fallbacks   []string `json:"fallbacks"`
		Failing     string   `json:"failing"`
		FailingHard bool     `json:"failing_hard"`
	} `json:"profiles"`
}

func readSettings(t *testing.T, r *rig) settingsReply {
	t.Helper()
	_, b := r.do("GET", "/api/settings", nil)
	var v settingsReply
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTheFallbackOrderIsSavedInOrderAndChecked(t *testing.T) {
	r := fallbackRig(t)
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"fallback_order": []string{"nv", "free", "nv"}}); resp.StatusCode != 200 {
		t.Fatalf("save: %d", resp.StatusCode)
	}
	if got := strings.Join(readSettings(t, r).FallbackOrder, ","); got != "nv,free" {
		t.Errorf("order: %s", got)
	}
	if got := strings.Join(r.srv.Cfg.FallbackChain("main"), ","); got != "nv,free" {
		t.Errorf("what main falls back to: %s", got)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"fallback_order": []string{"ghost"}}); resp.StatusCode != 400 {
		t.Errorf("a profile that does not exist was accepted: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"fallback_order": []string{}}); resp.StatusCode != 200 || len(readSettings(t, r).FallbackOrder) != 0 {
		t.Error("the list could not be emptied")
	}
	// the old single fallback_profile shows as the first of the list
	r.srv.Cfg.FallbackProfile = "free"
	if got := readSettings(t, r).FallbackOrder; len(got) != 1 || got[0] != "free" {
		t.Errorf("legacy fallback_profile: %v", got)
	}
}

func TestAProfileKeepsItsNotesAndItsOwnFallbacks(t *testing.T) {
	r := fallbackRig(t)
	body := map[string]any{"name": "free", "notes": "  my free one:\n fine for summaries  ", "fallbacks": []string{"nv", "free", "main"}}
	if resp, _ := r.do("POST", "/api/settings", body); resp.StatusCode != 200 {
		t.Fatalf("save: %d", resp.StatusCode)
	}
	var free struct {
		Notes     string
		Fallbacks []string
	}
	for _, p := range readSettings(t, r).Profiles {
		if p.Name == "free" {
			free.Notes, free.Fallbacks = p.Notes, p.Fallbacks
		}
	}
	if free.Notes != "my free one: fine for summaries" || strings.Join(free.Fallbacks, ",") != "nv,main" {
		t.Errorf("%+v", free)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"name": "free", "fallbacks": []string{"ghost"}}); resp.StatusCode != 400 {
		t.Errorf("an unknown fallback: %d", resp.StatusCode)
	}
	long := strings.Repeat("x", 500)
	r.do("POST", "/api/settings", map[string]any{"name": "nv", "notes": long})
	if n := r.srv.Cfg.Profiles["nv"].Notes; len([]rune(n)) != 200 {
		t.Errorf("notes are %d characters", len([]rune(n)))
	}
}

func TestSettingsSaysWhichModelIsFailingAndWhyAndEditingClearsIt(t *testing.T) {
	r := fallbackRig(t)
	// a chain that gave up on main because its key was refused
	main := provider.Link{Name: "main", Model: "m1", Provider: refusing{}}
	ok := provider.Link{Name: "free", Model: "m2", Provider: refusing{ok: true}}
	provider.Chain{Links: []provider.Link{main, ok}, Health: r.srv.Health}.Chat(t.Context(), provider.Request{})
	for _, p := range readSettings(t, r).Profiles {
		if p.Name == "main" && (!strings.Contains(p.Failing, "key was refused") || !p.FailingHard) {
			t.Errorf("main: %+v", p)
		}
		if p.Name == "free" && p.Failing != "" {
			t.Errorf("free should be fine: %+v", p)
		}
	}
	// pasting a new key is the fix, so the model is tried afresh
	r.do("POST", "/api/settings", map[string]any{"name": "main", "api_key": "new-key"})
	for _, p := range readSettings(t, r).Profiles {
		if p.Name == "main" && p.Failing != "" {
			t.Errorf("an edited profile still shows its old failure: %+v", p)
		}
	}
}

func TestTheNewProvidersAreOfferedAsPresets(t *testing.T) {
	r := fallbackRig(t)
	labels := ""
	for _, p := range readSettings(t, r).Presets {
		labels += p.Label + "|"
	}
	if !strings.Contains(labels, "Itera Compute|") || !strings.Contains(labels, "NVIDIA|") {
		t.Errorf("presets: %s", labels)
	}
}

// refusing answers like a service that refused the key, or, with ok set, like one that works.
type refusing struct{ ok bool }

func (p refusing) Chat(context.Context, provider.Request) (provider.Message, error) {
	if p.ok {
		return provider.Message{Content: "fine"}, nil
	}
	return provider.Message{}, &provider.StatusError{Status: 401, Err: errors.New("provider returned 401")}
}
