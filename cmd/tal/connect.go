package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"talaria/internal/config"
	"talaria/internal/mcp"
	"talaria/internal/oauth"
	"talaria/internal/vault"
)

// mcpAuth gives each MCP server that signs in with OAuth its source of tokens,
// kept in the vault. With no vault there are none, and those servers say so.
func mcpAuth(cfg *config.Config, v *vault.Vault) map[string]mcp.TokenSource {
	if v == nil {
		return nil
	}
	auth := map[string]mcp.TokenSource{}
	for name, srv := range cfg.MCPServers {
		if srv.OAuth != nil && !srv.Disabled {
			auth[name] = &oauth.Source{Vault: v, Server: name}
		}
	}
	return auth
}

// connectDeps is what `tal connect` needs from outside, replaceable by tests.
type connectDeps struct {
	hc     *http.Client
	open   func(string) error
	paste  func(context.Context) (string, error)
	getenv func(string) string
}

func realConnectDeps(noBrowser bool) connectDeps {
	d := connectDeps{hc: oauth.NewHTTPClient(), getenv: os.Getenv}
	if !noBrowser {
		d.open = oauth.OpenBrowser
	}
	if isTerminal(os.Stdin) {
		d.paste = oauth.Stdin
	}
	return d
}

// oauthServers names the configured servers that sign in with OAuth.
func oauthServers(cfg *config.Config) []string {
	var names []string
	for n, s := range cfg.MCPServers {
		if s.OAuth != nil {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names
}

// runConnect handles `tal connect [NAME] [-no-browser]` and `tal disconnect NAME`.
func runConnect(ctx context.Context, cfg *config.Config, home string, args []string, ask prompter, out io.Writer, deps func(noBrowser bool) connectDeps) error {
	disconnect := len(args) > 0 && args[0] == "disconnect"
	if disconnect {
		args = args[1:]
	}
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	noBrowser := fs.Bool("no-browser", false, "print the sign-in address instead of opening it")
	name := ""
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		name, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return fmt.Errorf("usage: tal connect [NAME] [-no-browser]  |  tal disconnect NAME: %w", err)
	}

	if name == "" {
		if disconnect {
			return errors.New("usage: tal disconnect NAME")
		}
		return listConnections(cfg, home, ask, out)
	}
	srv, ok := cfg.MCPServers[name]
	switch {
	case !ok:
		return fmt.Errorf("no MCP server named %q (servers: %s)", name, orNone(mapKeys(cfg.MCPServers)))
	case srv.OAuth == nil:
		return fmt.Errorf("the MCP server %q does not use OAuth: add \"oauth\": {} to its entry in the config file to sign in to it", name)
	}
	v, err := openVault(cfg, home, ask, nil)
	if errors.Is(err, vault.ErrNoVault) {
		return errors.New("sign-ins are kept in the vault, which does not exist yet: create it with `tal vault init`, then run this again")
	}
	if err != nil {
		return err
	}

	if disconnect {
		c, ok := v.Get(oauth.CredentialName(name))
		if !ok {
			return fmt.Errorf("not signed in to %s", name)
		}
		d := deps(false)
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		revoked := oauth.Revoke(cctx, d.hc, c.OAuth)
		if err := v.Remove(oauth.CredentialName(name)); err != nil {
			return err
		}
		if revoked {
			fmt.Fprintf(out, "signed out of %s, and the service was told to forget the sign-in\n", name)
		} else {
			fmt.Fprintf(out, "signed out of %s (the service does not support being told, so the sign-in lapses on its own; remove Talaria in the service's own settings to end it at once)\n", name)
		}
		return nil
	}

	d := deps(*noBrowser)
	cfgOAuth := oauth.Config{
		ClientID: srv.OAuth.ClientID, Scopes: srv.OAuth.Scopes, RedirectPort: srv.OAuth.RedirectPort, AuthServer: srv.OAuth.AuthorizationServer,
	}
	if env := srv.OAuth.ClientSecretEnv; env != "" {
		if cfgOAuth.ClientSecret = d.getenv(env); cfgOAuth.ClientSecret == "" {
			return fmt.Errorf("oauth.client_secret_env names %s, which is not set", env)
		}
	}
	data, err := oauth.Login{HC: d.hc, ServerURL: srv.URL, Config: cfgOAuth, Out: out, Open: d.open, Paste: d.paste}.Run(ctx)
	if err != nil {
		return err
	}
	if err := oauth.Save(v, name, data); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nsigned in to %s.\n", name)

	// Prove it works, and show what the service offers and what will ask first.
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	m := mcp.StartAuth(cctx, map[string]mcp.Server{name: srv}, d.getenv, map[string]mcp.TokenSource{name: &oauth.Source{Vault: v, Server: name}})
	defer m.Close()
	st := m.Status()[0]
	if st.Err != nil {
		fmt.Fprintf(out, "but connecting with it failed: %v\n", st.Err)
		return nil
	}
	free, asks := 0, 0
	for _, t := range st.Tools {
		if t.NeedsApproval {
			asks++
		} else {
			free++
		}
	}
	fmt.Fprintf(out, "connected: %d tools (%d run without asking, %d ask first).\n", len(st.Tools), free, asks)
	if srv.Permissions == "" || srv.Permissions == mcp.PermAsk {
		fmt.Fprintf(out, "Every call asks, which is the default. To let reads run on their own, set \"permissions\": %q on this server (see `tal mcp list` for what each tool is taken to do).\n", mcp.PermReads)
	}
	return nil
}

func listConnections(cfg *config.Config, home string, ask prompter, out io.Writer) error {
	names := oauthServers(cfg)
	if len(names) == 0 {
		fmt.Fprintln(out, "no MCP server signs in with OAuth: add \"oauth\": {} to a server with a url in the config file, then run: tal connect NAME")
		return nil
	}
	v, err := openVault(cfg, home, ask, nil)
	if err != nil && !errors.Is(err, vault.ErrNoVault) {
		return err
	}
	for _, n := range names {
		state := "not signed in"
		switch {
		case v == nil:
			state = "not signed in (no vault yet: tal vault init)"
		default:
			if c, ok := v.Get(oauth.CredentialName(n)); ok && c.OAuth != nil {
				state = "signed in"
				if !c.OAuth.Expiry.IsZero() && time.Now().After(c.OAuth.Expiry) && c.OAuth.RefreshToken == "" {
					state = "sign-in expired"
				}
			}
		}
		fmt.Fprintf(out, "%-16s %s  (%s)\n", n, state, cfg.MCPServers[n].URL)
	}
	return nil
}

func mapKeys(m map[string]mcp.Server) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
