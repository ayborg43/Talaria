package main

import (
	"fmt"

	"talaria/internal/config"
)

// opsSettings says where to serve health and metrics, and the token /metrics needs. The
// environment can set both, so a container needs no config file for them:
// TALARIA_OPS_LISTEN overrides ops.listen, and TALARIA_OPS_TOKEN is the token unless
// ops.token_env names another variable. An empty listen address means off.
func opsSettings(cfg *config.Config, getenv func(string) string) (listen, token string, err error) {
	listen = cfg.Ops.Listen
	if v := getenv("TALARIA_OPS_LISTEN"); v != "" {
		listen = v
	}
	name := cfg.Ops.TokenEnv
	if name == "" {
		name = "TALARIA_OPS_TOKEN"
	}
	token = getenv(name)
	if cfg.Ops.TokenEnv != "" && token == "" && listen != "" {
		return "", "", fmt.Errorf("ops.token_env names $%s, which is not set", cfg.Ops.TokenEnv)
	}
	return listen, token, nil
}
