package main

import (
	"strings"

	"talaria/internal/config"
	"talaria/internal/x"
)

// buildX signs the agent into X when an access token is set, in Settings or
// the environment. Nil means no X account, and the X tools stay off.
func buildX(cfg *config.Config, getenv func(string) string) *x.Service {
	if cfg.XToken(getenv) == "" {
		return nil
	}
	return &x.Service{Client: &x.Client{Token: cfg.XToken(getenv)}}
}

// xSecrets is the X access token, so it can be kept out of anything the model
// sees or that is stored.
func xSecrets(cfg *config.Config, getenv func(string) string) []string {
	if t := cfg.XToken(getenv); strings.TrimSpace(t) != "" {
		return []string{t}
	}
	return nil
}
