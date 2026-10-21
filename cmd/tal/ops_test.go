package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/config"
)

func doctorOn(t *testing.T, cfgJSON string, env map[string]string, prep func(home string)) (string, int) {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".talaria")
	os.MkdirAll(home, 0o700)
	cfgPath := filepath.Join(home, "config.json")
	os.WriteFile(cfgPath, []byte(cfgJSON), 0o600)
	if prep != nil {
		prep(home)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	n := runDoctor(cfg, cfgPath, home, func(k string) string { return env[k] }, &out)
	return out.String(), n
}

func TestDoctorSaysAllIsWellForAGoodSetup(t *testing.T) {
	out, n := doctorOn(t, `{"profiles":{"m":{"type":"deepseek"}},"discord":{"token_env":"DT","allowed_users":["1234567890"]}}`,
		map[string]string{"DEEPSEEK_API_KEY": "sk-x", "DT": "token"}, nil)
	if n != 0 || !strings.Contains(out, "Nothing to fix") || !strings.Contains(out, "one person allowed") || !strings.Contains(out, "key from $DEEPSEEK_API_KEY") {
		t.Fatalf("%d problems:\n%s", n, out)
	}
	if strings.Contains(out, "sk-x") {
		t.Fatal("a key was printed")
	}
}

func TestDoctorFindsTheMistakesThatMatter(t *testing.T) {
	out, n := doctorOn(t, `{"profiles":{"m":{"type":"anthropic"}},
		"telegram":{"token_env":"TT","allowed_users":[11,22]},
		"policy":{"untrusted_content":"off","rules":{"shell":"allow"}},
		"mcp_servers":{"mail":{"url":"https://m.example/mcp","trusted":true}},
		"ops":{"listen":"0.0.0.0:9100"}}`,
		map[string]string{"TT": "token"}, func(home string) {
			os.Chmod(home, 0o755)
			os.WriteFile(filepath.Join(home, "vault.enc"), []byte("x"), 0o644)
		})
	for _, want := range []string{
		"is open to other users", "chmod 700",
		"vault.enc is readable by other users", "chmod 600",
		"needs an API key",
		"2 people are allowed in",
		"policy.untrusted_content is off",
		"marked trusted",
		"shell = allow",
		"not a local address",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if n < 7 {
		t.Fatalf("%d problems", n)
	}
}

func TestDoctorNotesAKeyFileBesideTheVault(t *testing.T) {
	var keyPath string
	out, _ := doctorOn(t, `{"profiles":{"m":{"base_url":"http://localhost:1/v1"}}}`, nil, func(home string) {
		keyPath = filepath.Join(home, "vault.key")
		os.WriteFile(keyPath, []byte("phrase"), 0o600)
	})
	_ = keyPath
	if strings.Contains(out, "key file is inside") {
		t.Fatal("only when the config names it")
	}
	home := filepath.Join(t.TempDir(), ".talaria")
	os.MkdirAll(home, 0o700)
	kp := filepath.Join(home, "vault.key")
	os.WriteFile(kp, []byte("phrase"), 0o644)
	cfgPath := filepath.Join(home, "config.json")
	os.WriteFile(cfgPath, []byte(`{"vault":{"key_file":"`+kp+`"},"profiles":{"m":{"base_url":"http://localhost:1/v1"}}}`), 0o600)
	cfg, _ := config.Load(cfgPath)
	var b bytes.Buffer
	n := runDoctor(cfg, cfgPath, home, func(string) string { return "" }, &b)
	if n == 0 || !strings.Contains(b.String(), "key file "+kp+" is readable") || !strings.Contains(b.String(), "next to the vault") {
		t.Fatalf("%d:\n%s", n, b.String())
	}
}

func TestSharedWarningOnlyForMoreThanOnePerson(t *testing.T) {
	var said []string
	say := func(m string) { said = append(said, m) }
	sharedWarning(0, say)
	sharedWarning(1, say)
	if len(said) != 0 {
		t.Fatal("one person is the intended use")
	}
	sharedWarning(2, say)
	if len(said) != 1 || !strings.Contains(said[0], "saved logins") || !strings.Contains(said[0], "apart") {
		t.Fatalf("%v", said)
	}
}

func TestBackupAndRestoreFromTheCommandLine(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".talaria")
	os.MkdirAll(filepath.Join(home, "memories"), 0o700)
	os.WriteFile(filepath.Join(home, "memories", "MEMORY.md"), []byte("likes tea"), 0o600)
	os.WriteFile(filepath.Join(home, "vault.enc"), []byte("ENCRYPTED"), 0o600)
	keyPath := filepath.Join(t.TempDir(), "elsewhere.key")
	os.WriteFile(keyPath, []byte("THE-PASSPHRASE"), 0o600)
	cfg := &config.Config{Vault: config.Vault{KeyFile: keyPath}}

	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	var out bytes.Buffer
	if err := runBackup(cfg, home, []string{"-o", archive}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wrote "+archive) || !strings.Contains(out.String(), "secret") || !strings.Contains(out.String(), "key file") {
		t.Fatalf("%s", out.String())
	}
	if fi, _ := os.Stat(archive); fi.Mode().Perm() != 0o600 {
		t.Fatalf("a backup holds a person's history: %v", fi.Mode())
	}
	if err := runBackup(cfg, home, []string{"-o", archive}, &out); err == nil {
		t.Fatal("an existing file is never overwritten")
	}
	if err := runBackup(cfg, home, []string{"extra"}, &out); err == nil {
		t.Fatal("usage")
	}

	fresh := filepath.Join(t.TempDir(), "new", ".talaria")
	out.Reset()
	if err := runRestore(fresh, []string{archive}, &out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(fresh, "memories", "MEMORY.md")); string(b) != "likes tea" {
		t.Fatal("restored")
	}
	if err := runRestore(fresh, []string{archive}, &out); err == nil || !strings.Contains(err.Error(), "-replace") {
		t.Fatalf("a second restore over data needs -replace: %v", err)
	}
	if err := runRestore(fresh, []string{archive, "-replace"}, &out); err != nil {
		t.Fatalf("the flag may follow the file name: %v", err)
	}
	matches, _ := filepath.Glob(fresh + ".before-restore-*")
	if len(matches) != 1 {
		t.Fatalf("the old state is kept: %v", matches)
	}
	if err := runRestore(fresh, nil, &out); err == nil {
		t.Fatal("usage")
	}
	if err := runRestore(fresh, []string{filepath.Join(t.TempDir(), "nothing")}, &out); err == nil {
		t.Fatal("no such file")
	}
}

func TestBackupLeavesOutTheVaultsKeyFileEvenWhenItIsInsideTheHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".talaria")
	os.MkdirAll(home, 0o700)
	os.WriteFile(filepath.Join(home, "vault.enc"), []byte("ENCRYPTED"), 0o600)
	os.WriteFile(filepath.Join(home, "my.key"), []byte("THE-PASSPHRASE"), 0o600)
	os.WriteFile(filepath.Join(home, "vault.key"), []byte("ANOTHER-PASSPHRASE"), 0o600)
	cfg := &config.Config{Vault: config.Vault{KeyFile: filepath.Join(home, "my.key")}}
	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := runBackup(cfg, home, []string{"-o", archive}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "r")
	if err := runRestore(dst, []string{archive}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"my.key", "vault.key"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err == nil {
			t.Errorf("%s is in the backup, next to the vault it opens", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "vault.enc")); err != nil {
		t.Fatal("the vault itself is kept")
	}
}
