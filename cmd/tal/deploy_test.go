package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// runEntrypoint runs the container's entrypoint with a stand-in `tal` that reports what it was given.
func runEntrypoint(t *testing.T, env []string, args ...string) (stdout, stderr string, code int, home string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "tal"), []byte("#!/bin/sh\necho \"tal $*\"\n"), 0o755)
	home = filepath.Join(t.TempDir(), "data")
	cmd := exec.Command("sh", append([]string{"../../docker/entrypoint.sh"}, args...)...)
	cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "TALARIA_HOME=" + home}, env...)
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return so.String(), se.String(), code, home
}

func TestTheContainerEntrypointStartsTalariaWithItsArguments(t *testing.T) {
	out, _, code, home := runEntrypoint(t, nil, "gateway")
	if code != 0 || strings.TrimSpace(out) != "tal gateway" {
		t.Fatalf("%d %q", code, out)
	}
	if fi, err := os.Stat(home); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the state folder is made private: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(home, "config.json")); err == nil {
		t.Fatal("no config is invented")
	}
	if out, _, _, _ := runEntrypoint(t, nil, "doctor", "-x"); strings.TrimSpace(out) != "tal doctor -x" {
		t.Fatalf("%q", out)
	}
}

func TestTheConfigFromTheEnvironmentIsWrittenAtEveryStart(t *testing.T) {
	json := `{"limits":{"model_calls_per_hour":500}}`
	_, errOut, code, home := runEntrypoint(t, []string{"TALARIA_CONFIG_JSON=" + json}, "gateway")
	if code != 0 || !strings.Contains(errOut, "wrote") {
		t.Fatalf("%d %q", code, errOut)
	}
	p := filepath.Join(home, "config.json")
	b, _ := os.ReadFile(p)
	if strings.TrimSpace(string(b)) != json {
		t.Fatalf("%q", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("the config is private: %v", fi.Mode())
	}
	// A second start with a changed setting replaces it: the platform's settings are the truth.
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "tal"), []byte("#!/bin/sh\n"), 0o755)
	cmd := exec.Command("sh", "../../docker/entrypoint.sh", "gateway")
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "TALARIA_HOME=" + home, `TALARIA_CONFIG_JSON={"limits":{"model_calls_per_hour":5}}`}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), `":5}`) {
		t.Fatalf("not replaced: %q", b)
	}
}

func TestTheContainerEntrypointRefusesAnUnwritableStateFolderAndSaysWhy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	ro := t.TempDir()
	os.Chmod(ro, 0o500)
	defer os.Chmod(ro, 0o700)
	_, errOut, code, _ := runEntrypoint(t, []string{"TALARIA_HOME=" + ro}, "gateway")
	if code != 78 || !strings.Contains(errOut, "cannot write to "+ro) || !strings.Contains(errOut, "volume") {
		t.Fatalf("%d %q", code, errOut)
	}
}

// The compose file and the example must name the same variables, and the compose file must carry every
// variable the program reads for a deployment; otherwise a setting works on a laptop and silently not on the server.
func TestTheComposeFileAndTheExampleAgree(t *testing.T) {
	compose, err := os.ReadFile("../../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	var passed []string
	for _, m := range regexp.MustCompile(`(?m)^\s+(TALARIA_[A-Z_]+|[A-Z]+_API_KEY):`).FindAllStringSubmatch(string(compose), -1) {
		passed = append(passed, m[1])
	}
	for _, want := range []string{"TALARIA_PROVIDER", "TALARIA_MODEL", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "DEEPSEEK_API_KEY",
		"TALARIA_DISCORD_TOKEN", "TALARIA_DISCORD_ALLOWED_USERS", "TALARIA_TELEGRAM_TOKEN", "TALARIA_TELEGRAM_ALLOWED_USERS",
		"TALARIA_VAULT_PASSPHRASE", "TALARIA_CONFIG_JSON", "TALARIA_OPS_LISTEN", "TALARIA_OPS_TOKEN", "TALARIA_CALENDARS", "TALARIA_TIMEZONE", "TALARIA_EMAIL", "TALARIA_EMAIL_PASSWORD", "TALARIA_EMAIL_SERVER", "TALARIA_EMAIL_USER"} {
		found := false
		for _, p := range passed {
			found = found || p == want
		}
		if !found {
			t.Errorf("the compose file does not pass %s to the container", want)
		}
		if !strings.Contains(string(example), want) {
			t.Errorf(".env.example does not mention %s", want)
		}
	}
	// What holds the container down.
	for _, want := range []string{"read_only: true", "cap_drop: [ALL]", "no-new-privileges:true", "talaria-data:/data", "talaria-work:/work", "${TALARIA_PROVIDER:?"} {
		if !strings.Contains(string(compose), want) {
			t.Errorf("the compose file lost %q", want)
		}
	}
	if strings.Contains(string(compose), "container_name") || strings.Contains(string(compose), "ports:") {
		t.Error("nothing needs a fixed container name or a published port")
	}
	// Every default key variable the program recognises is passed through, so a provider chosen by name finds its key.
	for _, name := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "DEEPSEEK_API_KEY"} {
		if !strings.Contains(string(compose), name+":") {
			t.Errorf("%s", name)
		}
	}
}

func TestTheDockerfileKeepsTheContainerSafeAndChecked(t *testing.T) {
	b, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	d := string(b)
	for _, want := range []string{"USER talaria", "adduser -D -u 10001", "HEALTHCHECK", "VOLUME [\"/data\", \"/work\"]", "CGO_ENABLED=0", "ENTRYPOINT [\"talaria-entrypoint\"]", "TALARIA_HOME=/data", "ARG GO_VERSION=1.27.3"} {
		if !strings.Contains(d, want) {
			t.Errorf("the Dockerfile lost %q", want)
		}
	}
	mod, _ := os.ReadFile("../../go.mod")
	if !strings.Contains(string(mod), "go 1.27.3") {
		t.Error("go.mod and the Dockerfile name different Go versions")
	}
	if ig, _ := os.ReadFile("../../.dockerignore"); !strings.Contains(string(ig), ".env") {
		t.Error("a .env file must never be copied into an image")
	}
}
