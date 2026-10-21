package execenv

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// conformance is the behaviour every environment must have, whether it is this
// machine, a container or a remote host. dir is an existing, writable absolute
// directory in the environment's own filesystem.
func conformance(t *testing.T, env Env, dir string) {
	ctx := context.Background()
	shell := func(cmd string) ShellResult {
		t.Helper()
		res, err := env.Shell(ctx, cmd, 20*time.Second)
		if err != nil {
			t.Fatalf("shell %q: %v", cmd, err)
		}
		return res
	}

	t.Run("shell output and exit codes", func(t *testing.T) {
		if res := shell("echo hello; echo oops >&2; echo again"); res.Output != "hello\noops\nagain\n" || res.ExitCode != 0 {
			t.Fatalf("stdout and stderr arrive together, in order: %+v", res)
		}
		if res := shell("echo partial; exit 3"); res.ExitCode != 3 || res.Output != "partial\n" || res.TimedOut {
			t.Fatalf("%+v", res)
		}
		if res := shell("cat"); res.Output != "" || res.ExitCode != 0 { // standard input is empty, so nothing waits on it
			t.Fatalf("%+v", res)
		}
		if res := shell(`printf '%s' "a b" 'c'"'"'d' "\$HOME" '$HOME' '` + "`x`" + `'`); res.Output != "a bc'd$HOME$HOME`x`" {
			t.Fatalf("quoting must survive the trip: %q", res.Output)
		}
		if res := shell("echo line1\necho line2"); res.Output != "line1\nline2\n" {
			t.Fatalf("a multi-line command: %q", res.Output)
		}
		if res := shell("echo 'héllo wörld ✓'"); res.Output != "héllo wörld ✓\n" {
			t.Fatalf("unicode: %q", res.Output)
		}
	})

	t.Run("a command that runs too long is stopped", func(t *testing.T) {
		start := time.Now()
		res, err := env.Shell(ctx, "echo started; sleep 30; echo never", 700*time.Millisecond)
		if err != nil || !res.TimedOut {
			t.Fatalf("%+v %v", res, err)
		}
		if strings.Contains(res.Output, "never") || time.Since(start) > 10*time.Second {
			t.Fatalf("it should have been stopped after about a second, took %v", time.Since(start))
		}
	})

	t.Run("cancelling stops a command", func(t *testing.T) {
		cctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		defer cancel()
		start := time.Now()
		if _, err := env.Shell(cctx, "sleep 30", time.Minute); err == nil {
			t.Fatal("expected an error")
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("took %v", time.Since(start))
		}
	})

	t.Run("output is capped without blocking the command", func(t *testing.T) {
		res := shell("yes x | head -c 200000; echo done")
		if !res.Truncated || len(res.Output) != MaxOutput {
			t.Fatalf("truncated=%v, %d bytes kept (want %d)", res.Truncated, len(res.Output), MaxOutput)
		}
	})

	t.Run("files round trip", func(t *testing.T) {
		binary := make([]byte, 256)
		for i := range binary {
			binary[i] = byte(i)
		}
		cases := map[string][]byte{
			"plain.txt":                    []byte("hello\nworld\n"),
			"empty.txt":                    {},
			"binary.bin":                   binary,
			"sp ace/it's \"q\" $x `y`.txt": []byte("odd name"),
			"deep/er/still/file.txt":       []byte("made the parents"),
			"unicode-✓-é.txt":              []byte("héllo ✓"),
			"no-newline.txt":               []byte("x"),
		}
		for name, content := range cases {
			path := dir + "/" + name
			if err := env.WriteFile(ctx, path, content); err != nil {
				t.Fatalf("write %q: %v", name, err)
			}
			got, truncated, err := env.ReadFile(ctx, path, 1<<20)
			if err != nil || truncated || !bytes.Equal(got, content) {
				t.Fatalf("read %q back: %d bytes (want %d), truncated=%v, err=%v", name, len(got), len(content), truncated, err)
			}
		}
		// Replacing a file replaces it completely.
		path := dir + "/replace.txt"
		env.WriteFile(ctx, path, []byte("a much longer first version"))
		env.WriteFile(ctx, path, []byte("short"))
		if got, _, _ := env.ReadFile(ctx, path, 100); string(got) != "short" {
			t.Fatalf("got %q", got)
		}
		// The shell sees the same file.
		if res := shell("cat '" + dir + "/plain.txt'"); res.Output != "hello\nworld\n" {
			t.Fatalf("%q", res.Output)
		}
	})

	t.Run("reading is limited", func(t *testing.T) {
		path := dir + "/long.txt"
		env.WriteFile(ctx, path, []byte(strings.Repeat("0123456789", 10)))
		got, truncated, err := env.ReadFile(ctx, path, 25)
		if err != nil || !truncated || string(got) != "0123456789012345678901234" {
			t.Fatalf("%q truncated=%v err=%v", got, truncated, err)
		}
		if got, truncated, _ := env.ReadFile(ctx, path, 100); truncated || len(got) != 100 {
			t.Fatalf("a file exactly at the limit is not truncated: %d truncated=%v", len(got), truncated)
		}
	})

	t.Run("missing files are errors", func(t *testing.T) {
		if _, _, err := env.ReadFile(ctx, dir+"/nope.txt", 100); err == nil || !strings.Contains(strings.ToLower(err.Error()), "no such file") {
			t.Fatalf("reading a missing file: %v", err)
		}
		if _, err := env.ListDir(ctx, dir+"/nowhere"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "no such file") {
			t.Fatalf("listing a missing directory: %v", err)
		}
	})

	t.Run("directories are listed", func(t *testing.T) {
		d := dir + "/listing"
		env.WriteFile(ctx, d+"/b.txt", []byte("b"))
		env.WriteFile(ctx, d+"/a.txt", []byte("a"))
		env.WriteFile(ctx, d+"/.hidden", []byte("h"))
		env.WriteFile(ctx, d+"/subdir/inner.txt", []byte("i"))
		got, err := env.ListDir(ctx, d)
		if err != nil || strings.Join(got, "|") != ".hidden|a.txt|b.txt|subdir/" {
			t.Fatalf("%q %v", got, err)
		}
		env.WriteFile(ctx, d+"/subdir/inner.txt", []byte("i")) // keep the directory
		os.Remove(d + "/x")                                    // (no-op: the environment may be remote)
		if res := shell("mkdir -p '" + dir + "/emptydir'"); res.ExitCode != 0 {
			t.Fatal(res.Output)
		}
		if got, err := env.ListDir(ctx, dir+"/emptydir"); err != nil || len(got) != 0 {
			t.Fatalf("an empty directory lists nothing: %q %v", got, err)
		}
	})
}

func describeAll(t *testing.T, env Env, wantInDescription ...string) {
	t.Helper()
	d := env.Describe()
	for _, w := range wantInDescription {
		if !strings.Contains(d, w) {
			t.Errorf("description %q should mention %q", d, w)
		}
	}
	_ = fmt.Sprint(d)
}
