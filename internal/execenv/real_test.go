package execenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These tests use the real docker program, and so are off unless asked for:
//
//	TALARIA_TEST_DOCKER_IMAGE=redis:7 go test ./internal/execenv -run Real
//
// Name an image you already have: nothing is pulled unless Docker decides to.
// Any image with a POSIX sh works. Each test starts one container and removes it.

func realImage(t *testing.T) string {
	img := os.Getenv("TALARIA_TEST_DOCKER_IMAGE")
	if img == "" {
		t.Skip("set TALARIA_TEST_DOCKER_IMAGE to an image you have locally to run the real Docker tests")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	return img
}

func containerExists(name string) bool {
	out, _ := exec.Command("docker", "ps", "-a", "--filter", "name=^"+name+"$", "--format", "{{.Names}}").Output()
	return strings.TrimSpace(string(out)) == name
}

func TestRealDockerConformance(t *testing.T) {
	img := realImage(t)
	host := t.TempDir()
	env, err := Open(context.Background(), "real", Spec{
		Type: "docker", Image: img, Workdir: "/work",
		Mounts: []string{host + ":/mnt/shared"},
		Memory: "256m", CPUs: 1, Network: "none",
	}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	name := env.(*dockerEnv).container
	defer func() {
		env.Close()
		if containerExists(name) {
			t.Errorf("container %s was left behind", name)
		}
	}()

	describeAll(t, env, img, "/mnt/shared", "/work", "no network access")
	conformance(t, env, "/tmp/talaria-conformance")

	ctx := context.Background()
	t.Run("workdir and relative paths", func(t *testing.T) {
		if res, _ := env.Shell(ctx, "pwd", 10*time.Second); strings.TrimSpace(res.Output) != "/work" {
			t.Fatalf("pwd: %q", res.Output)
		}
		env.WriteFile(ctx, "relative.txt", []byte("here"))
		if res, _ := env.Shell(ctx, "cat /work/relative.txt", 10*time.Second); res.Output != "here" {
			t.Fatalf("a relative path starts in the workdir: %q", res.Output)
		}
	})

	t.Run("a mount shares files with the host", func(t *testing.T) {
		if err := env.WriteFile(ctx, "/mnt/shared/from-container.txt", []byte("hello host")); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(host + "/from-container.txt"); err != nil || string(b) != "hello host" {
			t.Fatalf("the host should see what the container wrote: %q %v", b, err)
		}
		os.WriteFile(host+"/from-host.txt", []byte("hello container"), 0o644)
		if data, _, err := env.ReadFile(ctx, "/mnt/shared/from-host.txt", 100); err != nil || string(data) != "hello container" {
			t.Fatalf("%q %v", data, err)
		}
	})

	t.Run("limits are applied", func(t *testing.T) {
		name := env.(*dockerEnv).container
		out, err := exec.Command("docker", "inspect", "--format",
			"{{.HostConfig.NetworkMode}} {{.HostConfig.Memory}} {{.HostConfig.NanoCpus}} {{.HostConfig.PidsLimit}} {{.HostConfig.Init}} {{.HostConfig.SecurityOpt}}", name).Output()
		if err != nil {
			t.Fatal(err)
		}
		got := strings.TrimSpace(string(out))
		if got != "none 268435456 1000000000 1024 true [no-new-privileges]" {
			t.Fatalf("container settings: %q", got)
		}
		if res, _ := env.Shell(ctx, "wget -q -T 2 -O- http://example.com 2>&1 | head -c 100; echo; getent hosts example.com | head -c 50", 15*time.Second); strings.Contains(res.Output, "<html") {
			t.Fatalf("network none must mean no network: %q", res.Output)
		}
	})
}

func TestRealDockerAttach(t *testing.T) {
	img := realImage(t)
	name := "talaria-attach-test-" + randomHex(3)
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "--entrypoint", "", img, "tail", "-f", "/dev/null").CombinedOutput(); err != nil {
		t.Fatalf("setting up the container: %v: %s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", name).Run()

	env, err := Open(context.Background(), "attached", Spec{Type: "docker", Container: name, Workdir: "/tmp"}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	conformance(t, env, "/tmp/talaria-attach")
	env.Close()
	if !containerExists(name) {
		t.Fatal("closing must not remove a container that was only attached to")
	}
	if out, err := exec.Command("docker", "exec", name, "ls", "/tmp/talaria-attach/plain.txt").CombinedOutput(); err != nil {
		t.Fatalf("what was written should still be there: %s", out)
	}
}

func TestRealDockerFailures(t *testing.T) {
	img := realImage(t)
	ctx := context.Background()

	if _, err := Open(ctx, "x", Spec{Type: "docker", Container: "talaria-no-such-container"}, os.Getenv); err == nil || !strings.Contains(err.Error(), "cannot find the container") {
		t.Fatalf("got %v", err)
	}
	_, err := Open(ctx, "x", Spec{Type: "docker", Image: "talaria-no-such-image-xyz:0"}, os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "cannot start a container") {
		t.Fatalf("got %v", err)
	}
	// A bad mount must be reported, and must not leave a container behind.
	out, _ := exec.Command("docker", "ps", "-a", "--filter", "label="+labelManaged, "--format", "{{.Names}}").Output()
	before := strings.TrimSpace(string(out))
	if _, err := Open(ctx, "x", Spec{Type: "docker", Image: img, Mounts: []string{"/talaria/does/not/exist/anywhere:/x:ro"}, Network: "none"}, os.Getenv); err == nil {
		t.Log("(this Docker creates a missing mount source, so the failure case is not testable here)")
	}
	out, _ = exec.Command("docker", "ps", "-a", "--filter", "label="+labelManaged, "--format", "{{.Names}}").Output()
	if after := strings.TrimSpace(string(out)); after != before {
		t.Fatalf("a failed start left containers behind: before %q, after %q", before, after)
	}
}
