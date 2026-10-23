package main

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/app"
	"talaria/internal/config"
	"talaria/internal/execenv"
	"talaria/internal/guard"
	"talaria/internal/vault"
)

// scripted answers the prompts it is asked, in order, and notes what was asked.
type scripted struct {
	answers []string
	asked   []string
}

func (s *scripted) next(prompt string) (string, error) {
	s.asked = append(s.asked, prompt)
	if len(s.answers) == 0 {
		return "", errors.New("asked for more than the test provided: " + prompt)
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a, nil
}
func (s *scripted) line(p string) (string, error)   { return s.next(p) }
func (s *scripted) secret(p string) (string, error) { return s.next(p) }

const passphrase = "a long enough passphrase"

func tal(t *testing.T, cfg *config.Config, home string, ans *scripted, args ...string) (string, error) {
	t.Helper()
	vault.ForTests(t)
	t.Setenv("TALARIA_VAULT_PASSPHRASE", "")
	var out bytes.Buffer
	err := runVault(cfg, home, args, ans, &out)
	return out.String(), err
}

func TestVaultInitAddListRemove(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{}

	if out, err := tal(t, cfg, home, &scripted{answers: []string{"short", "short"}}, "init"); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := tal(t, cfg, home, &scripted{answers: []string{passphrase, "something else entirely"}}, "init"); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(vaultPath(home)); err == nil {
		t.Fatal("a failed init must leave no vault")
	}
	out, err := tal(t, cfg, home, &scripted{answers: []string{passphrase, passphrase}}, "init")
	if err != nil || !strings.Contains(out, "TALARIA_VAULT_PASSPHRASE") || !strings.Contains(out, "tal vault add") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := tal(t, cfg, home, &scripted{answers: []string{passphrase, passphrase}}, "init"); err == nil {
		t.Fatal("init must not overwrite")
	}

	// Add: the name first, then flags; the password is asked for without being shown.
	ask := &scripted{answers: []string{"hunter2-very-secret", passphrase}}
	out, err = tal(t, cfg, home, ask, "add", "github", "-site", "github.com", "-site", "gist.github.com", "-user", "ada")
	if err != nil || !strings.Contains(out, `saved "github"`) || strings.Contains(out, "hunter2") {
		t.Fatalf("%q %v", out, err)
	}
	if ask.asked[0] != "password: " {
		t.Fatalf("%v", ask.asked)
	}
	// Without -user, the username is asked for (visibly), then the password.
	ask = &scripted{answers: []string{"grace", "grace-pass-123", passphrase}}
	if _, err := tal(t, cfg, home, ask, "add", "second", "-site", "http://localhost:3000"); err != nil {
		t.Fatal(err)
	}
	if ask.asked[0] != "username: " || ask.asked[1] != "password: " {
		t.Fatalf("%v", ask.asked)
	}
	ask = &scripted{answers: []string{"sk-live-abc123def456", passphrase}}
	if _, err := tal(t, cfg, home, ask, "add", "api", "-kind", "secret", "-site", "api.example.com"); err != nil || ask.asked[0] != "value: " {
		t.Fatalf("%v %v", err, ask.asked)
	}

	out, err = tal(t, cfg, home, &scripted{answers: []string{passphrase}}, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"github", "login", "github.com, gist.github.com", "user ada", "second", "http://localhost:3000", "api", "secret", "api.example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, leak := range []string{"hunter2", "grace-pass", "sk-live"} {
		if strings.Contains(out, leak) {
			t.Fatalf("`tal vault list` must never print a secret:\n%s", out)
		}
	}
	raw, _ := os.ReadFile(vaultPath(home))
	if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "github") {
		t.Fatal("the vault file is readable")
	}

	if _, err := tal(t, cfg, home, &scripted{answers: []string{passphrase}}, "remove", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := tal(t, cfg, home, &scripted{answers: []string{passphrase}}, "remove", "second"); err == nil {
		t.Fatal("no longer there")
	}
}

func TestVaultAddChecksBeforeAskingForThePassphrase(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{}
	tal(t, cfg, home, &scripted{answers: []string{passphrase, passphrase}}, "init")
	for name, args := range map[string][]string{
		"no site":        {"add", "x", "-user", "u"},
		"bad site":       {"add", "x", "-user", "u", "-site", "*.example.com"},
		"bad name":       {"add", "has space", "-user", "u", "-site", "a.example.com"},
		"no name":        {"add"},
		"flag as name":   {"add", "-site", "a.example.com"},
		"secret w/ user": {"add", "k", "-kind", "secret", "-user", "u", "-site", "a.example.com"},
	} {
		ask := &scripted{answers: []string{"u", "pw-value-123", passphrase}}
		if _, err := tal(t, cfg, home, ask, args...); err == nil {
			t.Errorf("%s should be refused", name)
		}
		for _, p := range ask.asked {
			if strings.Contains(p, "passphrase") {
				t.Errorf("%s: the passphrase was asked for although the credential was not valid", name)
			}
		}
	}
}

func TestVaultWrongPassphraseAndMissingVault(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{}
	if _, err := tal(t, cfg, home, &scripted{}, "list"); !errors.Is(err, vault.ErrNoVault) {
		t.Fatalf("%v", err)
	}
	tal(t, cfg, home, &scripted{answers: []string{passphrase, passphrase}}, "init")
	if _, err := tal(t, cfg, home, &scripted{answers: []string{"not the passphrase"}}, "list"); !errors.Is(err, vault.ErrWrongPassphrase) {
		t.Fatalf("%v", err)
	}
	if _, err := tal(t, cfg, home, &scripted{}, "explode"); err == nil || !strings.Contains(err.Error(), "unknown vault command") {
		t.Fatalf("%v", err)
	}
}

func TestVaultKeyFileLetsAGatewayOpenItUnattended(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{}
	keyFile := filepath.Join(t.TempDir(), "sub", "vault.key")
	out, err := tal(t, cfg, home, &scripted{}, "init", "-keyfile", keyFile)
	if err != nil || !strings.Contains(out, `"key_file"`) || !strings.Contains(out, keyFile) {
		t.Fatalf("%q %v", out, err)
	}
	if fi, err := os.Stat(keyFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the key file is private: %v %v", fi, err)
	}
	raw, _ := os.ReadFile(keyFile)
	if len(strings.TrimSpace(string(raw))) != 64 {
		t.Fatalf("a random 256-bit key, in hex: %q", raw)
	}
	// With the key file named in the config, nothing is asked.
	cfg.Vault.KeyFile = keyFile
	if _, err := tal(t, cfg, home, &scripted{answers: []string{"pw-value-1234567"}}, "add", "k", "-kind", "secret", "-site", "a.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := tal(t, cfg, home, &scripted{}, "list"); err != nil {
		t.Fatalf("no prompt should be needed: %v", err)
	}
	// And with no way to ask, a missing passphrase is an error that says what to do.
	cfg2 := &config.Config{}
	if _, err := openVault(cfg2, home, nil, nil); err == nil || !strings.Contains(err.Error(), "TALARIA_VAULT_PASSPHRASE") {
		t.Fatalf("%v", err)
	}
	// A key file will not be overwritten.
	if _, err := tal(t, cfg, t.TempDir(), &scripted{}, "init", "-keyfile", keyFile); err == nil {
		t.Fatal("overwrote a key file")
	}
}

func TestVaultInitLeavesNoKeyBehindWhenItFails(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{}
	tal(t, cfg, home, &scripted{answers: []string{passphrase, passphrase}}, "init")
	keyFile := filepath.Join(t.TempDir(), "k")
	if _, err := tal(t, cfg, home, &scripted{}, "init", "-keyfile", keyFile); err == nil {
		t.Fatal("a vault already exists")
	}
	if _, err := os.Stat(keyFile); err == nil {
		t.Fatal("a key for a vault that was not made must not be left lying about")
	}
}

func TestVaultPasswd(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{}
	tal(t, cfg, home, &scripted{answers: []string{passphrase, passphrase}}, "init")
	tal(t, cfg, home, &scripted{answers: []string{"pw-value-1234567", passphrase}}, "add", "k", "-kind", "secret", "-site", "a.example.com")
	if _, err := tal(t, cfg, home, &scripted{answers: []string{passphrase, "another long passphrase", "mismatch xxxxxxxxx"}}, "passwd"); err == nil {
		t.Fatal("mismatch")
	}
	out, err := tal(t, cfg, home, &scripted{answers: []string{passphrase, "another long passphrase", "another long passphrase"}}, "passwd")
	if err != nil || !strings.Contains(out, "TALARIA_VAULT_PASSPHRASE") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := tal(t, cfg, home, &scripted{answers: []string{passphrase}}, "list"); !errors.Is(err, vault.ErrWrongPassphrase) {
		t.Fatalf("the old passphrase is dead: %v", err)
	}
	if _, err := tal(t, cfg, home, &scripted{answers: []string{"another long passphrase"}}, "list"); err != nil {
		t.Fatal(err)
	}
}

// loadVault is what a running Talaria does at startup.
func TestLoadVaultKeepsTheSecretsOutOfWhatTheModelSees(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{}
	tal(t, cfg, home, &scripted{answers: []string{passphrase, passphrase}}, "init")
	tal(t, cfg, home, &scripted{answers: []string{"hunter2-very-secret", passphrase}}, "add", "gh", "-user", "ada", "-site", "github.com")

	vault.ForTests(t)
	g := &guard.Guard{Redact: guard.NewRedactor(nil)}
	var buf bytes.Buffer
	log := newLogger(&buf, func(string) string { return "" })

	t.Setenv("TALARIA_VAULT_PASSPHRASE", passphrase)
	v := loadVault(cfg, home, g, nil, log)
	if v == nil || buf.Len() != 0 {
		t.Fatalf("%v %q", v, buf.String())
	}
	if got := g.Redact.Scrub("the password is hunter2-very-secret ok"); got != "the password is [redacted] ok" {
		t.Fatalf("every vault secret is known to the redactor from the start: %q", got)
	}
	// One added while running is known as soon as it is seen.
	other, _ := vault.Open(vaultPath(home), passphrase, nil)
	other.Add(vault.Credential{Name: "late", Username: "u", Secret: "added-while-running", Sites: []string{"late.example"}})
	v.List()
	if got := g.Redact.Scrub("added-while-running"); got != "[redacted]" {
		t.Fatalf("%q", got)
	}

	// A vault that cannot be opened is a warning, not a reason to stop.
	t.Setenv("TALARIA_VAULT_PASSPHRASE", "the wrong one entirely")
	buf.Reset()
	if v := loadVault(cfg, home, g, nil, log); v != nil || strings.Count(buf.String(), "\n") != 1 || !strings.Contains(buf.String(), "locked") {
		t.Fatalf("%v %q", v, buf.String())
	}
	// And no vault at all is not worth a word.
	buf.Reset()
	if v := loadVault(cfg, t.TempDir(), g, nil, log); v != nil || buf.Len() != 0 {
		t.Fatalf("%v %q", v, buf.String())
	}
	// Asked at a terminal when the passphrase is not set.
	t.Setenv("TALARIA_VAULT_PASSPHRASE", "")
	if v := loadVault(cfg, home, g, &scripted{answers: []string{passphrase}}, log); v == nil {
		t.Fatal("the interactive command line may ask")
	}
	if v := loadVault(cfg, home, g, nil, log); v != nil || strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("a gateway cannot: %v %q", v, buf.String())
	}
}

func TestAgencyNoteMentionsTheVaultOnlyWhenThereIsOne(t *testing.T) {
	vault.ForTests(t)
	home := t.TempDir()
	cfg := &config.Config{}
	tal(t, cfg, home, &scripted{answers: []string{passphrase, passphrase}}, "init")
	t.Setenv("TALARIA_VAULT_PASSPHRASE", passphrase)
	v := loadVault(cfg, home, &guard.Guard{Redact: guard.NewRedactor(nil)}, nil, slog.New(slog.DiscardHandler))
	rt := app.Open(app.Config{Home: t.TempDir(), Provider: &spy{}, Model: "m", Env: execenv.Local{}, Vault: v})
	defer rt.Close()
	if n := agencyNote(rt); !strings.Contains(n, "browser_fill") || !strings.Contains(n, "never ask them to type a password") {
		t.Fatalf("%s", n)
	}
}
