package main

import (
	"slices"
	"testing"

	"talaria/internal/config"
)

func TestBuildXNeedsAToken(t *testing.T) {
	if got := buildX(&config.Config{}, envFn()); got != nil {
		t.Fatalf("no token, no X service: %+v", got)
	}
	inline := buildX(&config.Config{X: config.X{Token: "inline"}}, envFn("TALARIA_X_TOKEN", "t"))
	if inline == nil || inline.Client.Token != "inline" {
		t.Fatalf("what Settings saved wins: %+v", inline)
	}
	fromEnv := buildX(&config.Config{X: config.X{TokenEnv: "XTOK"}}, envFn("XTOK", "b"))
	if fromEnv == nil || fromEnv.Client.Token != "b" {
		t.Fatalf("the environment is the fallback: %+v", fromEnv)
	}
}

func TestXTokenIsASecret(t *testing.T) {
	cfg := &config.Config{X: config.X{Token: "x-token-value"}}
	if got := xSecrets(cfg, envFn()); !slices.Contains(got, "x-token-value") {
		t.Fatalf("the pasted token must be scrubbed: %v", got)
	}
	if got := xSecrets(&config.Config{}, envFn()); len(got) != 0 {
		t.Fatalf("nothing set, nothing secret: %v", got)
	}
}
