package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Fence keeps the agent's file tools away from places that hold Talaria's own
// secrets and settings, and from the usual homes of the user's credentials. A
// model steered by something it read could otherwise read the vault's key file,
// or write the file that records what it may do without asking.
//
// It is a fence round the file tools only. A shell command runs as the user and
// is not confined by it: that is why shell asks first, and why a sandbox (the
// workspace container) is the real boundary.
type Fence struct {
	// Paths are denied for reading, listing and writing, with everything below them.
	Paths []string
	// EnvDeny names environment variables a shell command is not given, on top of
	// any TALARIA_ variable that looks like a secret.
	EnvDeny []string
}

// ErrFenced is what the file tools return for a protected path.
var ErrFenced = errors.New("that is where Talaria keeps its own secrets and settings, or where your credentials live: the agent may not read or change it. Do it yourself in a terminal if you mean to")

var secretLike = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSWORD|PASSWD|PASSPHRASE|CREDENTIAL)S?$`)

func fold(p string) string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(p) // these file systems are usually case-insensitive: ~/.TALARIA is ~/.talaria
	}
	return p
}

// resolve makes a path absolute and follows symlinks as far as it exists; the
// part that does not exist yet is appended unresolved.
func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	rest := ""
	cur := abs
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// Check returns ErrFenced if path is inside a protected one.
func (f *Fence) Check(path string) error {
	if f == nil || len(f.Paths) == 0 {
		return nil
	}
	got := fold(resolve(path))
	for _, p := range f.Paths {
		if p == "" {
			continue
		}
		for _, form := range []string{resolve(p), filepath.Clean(p)} { // as the link points, and as written
			form = fold(form)
			if got == form || strings.HasPrefix(got, form+string(filepath.Separator)) {
				return fmt.Errorf("%w (%s)", ErrFenced, path)
			}
		}
	}
	return nil
}

// Environ is what a shell command is given: this process's environment without
// Talaria's own secrets.
func (f *Fence) Environ() []string {
	env := os.Environ()
	if f == nil {
		return env
	}
	deny := map[string]bool{}
	for _, n := range f.EnvDeny {
		deny[n] = true
	}
	out := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if deny[name] || (strings.HasPrefix(name, "TALARIA_") && secretLike.MatchString(name)) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// DefaultProtected are the places holding credentials that most people have,
// relative to the user's home directory.
var DefaultProtected = []string{
	".ssh", ".aws", ".gnupg", ".netrc", ".npmrc", ".pypirc", ".kube",
	".docker/config.json", ".config/gcloud", ".config/gh", ".azure",
}

// ProtectedPaths builds the list: Talaria's own state and config, the vault's
// key file, the usual credential places under userHome, and anything extra the
// user named.
func ProtectedPaths(talariaHome, configPath, keyFile, userHome string, extra []string) []string {
	paths := []string{talariaHome, configPath, keyFile}
	for _, rel := range DefaultProtected {
		if userHome != "" {
			paths = append(paths, filepath.Join(userHome, rel))
		}
	}
	for _, e := range extra {
		if strings.HasPrefix(e, "~/") && userHome != "" {
			e = filepath.Join(userHome, e[2:])
		}
		paths = append(paths, e)
	}
	var out []string
	for _, p := range paths {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
