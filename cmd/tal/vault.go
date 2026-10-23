package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"talaria/internal/config"
	"talaria/internal/guard"
	"talaria/internal/vault"
)

// vaultPath is where the encrypted vault lives.
func vaultPath(home string) string { return filepath.Join(home, "vault.enc") }

// prompter asks the person at the terminal for things. Secrets are read
// without being shown.
type prompter interface {
	line(prompt string) (string, error)
	secret(prompt string) (string, error)
}

// terminal is the real prompter, on standard input and error.
type terminal struct{ in *bufio.Reader }

func newTerminal() *terminal { return &terminal{in: bufio.NewReader(os.Stdin)} }

func (t *terminal) line(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	s, err := t.in.ReadString('\n')
	if err != nil && s == "" {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func (t *terminal) secret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	tty := isTerminal(os.Stdin)
	if tty {
		off := exec.Command("stty", "-echo")
		off.Stdin = os.Stdin
		if off.Run() == nil {
			defer func() {
				on := exec.Command("stty", "echo")
				on.Stdin = os.Stdin
				on.Run()
				fmt.Fprintln(os.Stderr)
			}()
		}
	}
	s, err := t.in.ReadString('\n')
	if err != nil && s == "" {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// openVault unlocks the vault with the passphrase from the environment or the
// key file, or, if there is none and ask is set, by asking. A vault that does
// not exist is ErrNoVault.
func openVault(cfg *config.Config, home string, ask prompter, onLoad func([]string)) (*vault.Vault, error) {
	path := vaultPath(home)
	if _, err := os.Stat(path); err != nil {
		return nil, vault.ErrNoVault
	}
	pass, err := vault.Passphrase(os.Getenv, cfg.Vault.KeyFile)
	if err != nil {
		if ask == nil {
			return nil, err
		}
		if pass, err = ask.secret("vault passphrase: "); err != nil {
			return nil, err
		}
	}
	return vault.Open(path, pass, onLoad)
}

// loadVault opens the vault for a running Talaria. A missing vault is not a
// problem; one that cannot be unlocked is reported, and the agent goes on
// without saved logins.
func loadVault(cfg *config.Config, home string, g *guard.Guard, ask prompter, log *slog.Logger) *vault.Vault {
	v, err := openVault(cfg, home, ask, func(secrets []string) { g.Redact.Add(secrets...) })
	switch {
	case errors.Is(err, vault.ErrNoVault):
		return nil
	case err != nil:
		log.Warn(fmt.Sprintf("the vault is locked, so saved logins are not available: %v", err))
		return nil
	}
	return v
}

// runVault handles `tal vault init|add|list|remove|passwd`.
func runVault(cfg *config.Config, home string, args []string, ask prompter, out io.Writer) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	path := vaultPath(home)
	switch args[0] {
	case "init":
		fs := flag.NewFlagSet("vault init", flag.ContinueOnError)
		keyFile := fs.String("keyfile", "", "make a key file at this path and use it as the passphrase")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var pass string
		if *keyFile != "" {
			if _, err := os.Stat(*keyFile); err == nil {
				return fmt.Errorf("%s already exists", *keyFile)
			}
			raw := make([]byte, 32)
			if _, err := rand.Read(raw); err != nil {
				return err
			}
			pass = hex.EncodeToString(raw)
			if err := os.MkdirAll(filepath.Dir(*keyFile), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(*keyFile, []byte(pass+"\n"), 0o600); err != nil {
				return err
			}
		} else {
			p1, err := ask.secret(fmt.Sprintf("choose a passphrase (at least %d characters): ", vault.MinPassphrase))
			if err != nil {
				return err
			}
			p2, err := ask.secret("again: ")
			if err != nil {
				return err
			}
			if p1 != p2 {
				return errors.New("the two passphrases differ")
			}
			pass = p1
		}
		if _, err := vault.Create(path, pass); err != nil {
			if *keyFile != "" {
				os.Remove(*keyFile) // do not leave a key for a vault that was not made
			}
			return err
		}
		fmt.Fprintf(out, "created %s\n", path)
		if *keyFile != "" {
			fmt.Fprintf(out, "the passphrase is in %s (guard it: it opens the vault).\nTell Talaria where it is, so a gateway can open the vault:\n  \"vault\": {\"key_file\": %q}\n", *keyFile, *keyFile)
		} else {
			fmt.Fprintln(out, "a gateway cannot ask for it, so give it the passphrase in the TALARIA_VAULT_PASSPHRASE environment variable, or put it in a file (mode 0600) named by vault.key_file in the config.")
		}
		fmt.Fprintln(out, "add a login with: tal vault add NAME -site SITE -user USERNAME")
		return nil

	case "add":
		fs := flag.NewFlagSet("vault add", flag.ContinueOnError)
		user := fs.String("user", "", "the username (for a login)")
		kind := fs.String("kind", vault.KindLogin, `"login" (a username and password) or "secret" (a single value, such as an API key)`)
		var sites multiFlag
		fs.Var(&sites, "site", "where it may be used: a host like github.com (https), or an origin like http://localhost:3000; repeatable")
		// The name comes first, so flags can follow it: tal vault add github -site github.com
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return errors.New("usage: tal vault add NAME -site SITE [-site SITE] [-user USERNAME] [-kind login|secret]")
		}
		name := args[1]
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		c := vault.Credential{Name: name, Kind: *kind, Username: *user, Sites: sites}
		if c.Kind == vault.KindLogin && c.Username == "" {
			u, err := ask.line("username: ")
			if err != nil {
				return err
			}
			c.Username = strings.TrimSpace(u)
		}
		label := "password: "
		if c.Kind == vault.KindSecret {
			label = "value: "
		}
		secret, err := ask.secret(label)
		if err != nil {
			return err
		}
		c.Secret = secret
		if err := c.Validate(); err != nil { // before the passphrase is asked for
			return err
		}
		v, err := openVault(cfg, home, ask, nil)
		if err != nil {
			return err
		}
		if err := v.Add(c); err != nil {
			return err
		}
		fmt.Fprintf(out, "saved %q, for use on %s\n", c.Name, strings.Join(c.Sites, ", "))
		return nil

	case "list":
		v, err := openVault(cfg, home, ask, nil)
		if err != nil {
			return err
		}
		users := v.Usernames()
		infos := v.List()
		if len(infos) == 0 {
			fmt.Fprintln(out, "the vault is empty; add a login with: tal vault add NAME -site SITE -user USERNAME")
		}
		for _, i := range infos {
			who := ""
			if u := users[i.Name]; u != "" {
				who = "  user " + u
			}
			fmt.Fprintf(out, "%-20s %-7s for %s%s\n", i.Name, i.Kind, strings.Join(i.Sites, ", "), who)
		}
		return nil

	case "remove":
		if len(args) != 2 {
			return errors.New("usage: tal vault remove NAME")
		}
		v, err := openVault(cfg, home, ask, nil)
		if err != nil {
			return err
		}
		if err := v.Remove(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(out, "removed %q\n", args[1])
		return nil

	case "passwd":
		v, err := openVault(cfg, home, ask, nil)
		if err != nil {
			return err
		}
		p1, err := ask.secret(fmt.Sprintf("new passphrase (at least %d characters): ", vault.MinPassphrase))
		if err != nil {
			return err
		}
		p2, err := ask.secret("again: ")
		if err != nil {
			return err
		}
		if p1 != p2 {
			return errors.New("the two passphrases differ")
		}
		if err := v.ChangePassphrase(p1); err != nil {
			return err
		}
		fmt.Fprintln(out, "the passphrase is changed. Update TALARIA_VAULT_PASSPHRASE or the key file a gateway uses, and restart it.")
		return nil
	}
	return fmt.Errorf("unknown vault command %q (try: tal vault init, add, list, remove, passwd)", args[0])
}

// multiFlag is a flag that may be given more than once.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }
