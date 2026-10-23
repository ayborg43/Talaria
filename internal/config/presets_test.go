package config

import (
	"testing"

	"talaria/internal/provider"
)

// Every preset must fill the settings form with something valid: a known
// provider type, a model suggestion, and a key variable when one is needed.
func TestPresetsAreValidStartingPoints(t *testing.T) {
	presets := Presets()
	if len(presets) == 0 {
		t.Fatal("no presets")
	}
	seen := map[string]bool{}
	for _, p := range presets {
		if p.Label == "" {
			t.Error("a preset without a label")
		}
		if seen[p.Label] {
			t.Errorf("the %q preset twice", p.Label)
		}
		seen[p.Label] = true
		switch p.Type {
		case provider.TypeOpenAI, provider.TypeAnthropic, provider.TypeDeepSeek:
		default:
			t.Errorf("%s: unknown type %q", p.Label, p.Type)
		}
		if p.Model == "" {
			t.Errorf("%s: no model suggestion", p.Label)
		}
		if p.NeedsKey && p.KeyEnv == "" {
			t.Errorf("%s: needs a key but names no variable", p.Label)
		}
		if p.Hint == "" {
			t.Errorf("%s: no hint", p.Label)
		}
	}
}
