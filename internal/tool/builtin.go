package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"talaria/internal/execenv"
)

const (
	maxFileRead  = 256 << 10
	shellTimeout = 60 * time.Second
)

// RegisterBuiltins adds the core file and shell tools, running on this machine.
func RegisterBuiltins(r *Registry) { RegisterBuiltinsIn(r, execenv.Local{}) }

// RegisterBuiltinsIn adds the core file and shell tools, running in env: this
// machine, a Docker container, or a remote host. current_time is always the
// local clock.
func RegisterBuiltinsIn(r *Registry, env execenv.Env) {
	where := ""
	if env.Remote() { // say so in each tool, so the model never assumes it is on the user's machine
		where = " This runs in " + env.Describe() + ". That is not the user's computer."
	}

	r.Register(Tool{
		Name:        "read_file",
		Description: "Read a UTF-8 text file and return its contents (truncated at 256 KiB)." + where,
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Path string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			data, truncated, err := env.ReadFile(ctx, a.Path, maxFileRead)
			if err != nil {
				return "", err
			}
			if truncated {
				return string(data) + "\n[truncated]", nil
			}
			return string(data), nil
		},
	})

	r.Register(Tool{
		Name:        "current_time",
		Description: "Return the current local date, time, weekday and timezone.",
		Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			return time.Now().Format("Monday 2006-01-02 15:04:05 MST (-07:00)"), nil
		},
	})

	r.Register(Tool{
		Name:        "list_dir",
		Description: "List the entries of a directory. Directories end with '/'." + where,
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Path string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			names, err := env.ListDir(ctx, a.Path)
			if err != nil {
				return "", err
			}
			var out strings.Builder
			for _, n := range names {
				out.WriteString(n + "\n")
			}
			return out.String(), nil
		},
	})

	r.Register(Tool{
		Name:          "write_file",
		Description:   "Write content to a file, creating parent directories and overwriting any existing file." + where,
		Schema:        json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
		NeedsApproval: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Path, Content string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			if err := env.WriteFile(ctx, a.Path, []byte(a.Content)); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path), nil
		},
	})

	r.Register(Tool{
		Name:          "shell",
		Description:   "Run a shell command with sh -c (60s timeout) and return combined stdout and stderr." + where,
		Schema:        json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
		NeedsApproval: true,
		Egress:        always, // a command can send anything anywhere
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Command string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			res, err := env.Shell(ctx, a.Command, shellTimeout)
			if err != nil {
				return "", err
			}
			text := res.Output
			if res.Truncated {
				text += "\n[truncated]"
			}
			switch {
			case res.TimedOut:
				text += fmt.Sprintf("\n[timed out after %s]", shellTimeout)
			case res.ExitCode == -1:
				text += "\n[exit: killed by a signal]"
			case res.ExitCode != 0:
				text += fmt.Sprintf("\n[exit: exit status %d]", res.ExitCode)
			}
			return text, nil
		},
	})
}

// always is an Egress check for a tool whose every call could send something out.
func always(json.RawMessage) bool { return true }

// Always is the same, for tools in other packages.
var Always = always
