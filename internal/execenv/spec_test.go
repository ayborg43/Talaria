package execenv

import (
	"strings"
	"testing"
)

func TestSpecValidate(t *testing.T) {
	good := map[string]Spec{
		"image":          {Type: "docker", Image: "python:3.12-slim"},
		"image full":     {Type: "docker", Image: "ghcr.io/me/dev:1", Workdir: "/work", User: "1000", Network: "none", Memory: "1g", CPUs: 0.5, Mounts: []string{"/home/me/proj:/work", "/data:/data:ro", "cache:/cache"}, Env: map[string]string{"LANG": "C"}, PassEnv: []string{"TOKEN"}, DockerArgs: []string{"--cap-drop", "ALL"}},
		"attach":         {Type: "docker", Container: "dev", Workdir: "/srv", User: "bob"},
		"ssh":            {Type: "ssh", Host: "build.example.com"},
		"ssh alias full": {Type: "ssh", Host: "build", User: "ci", Port: 2222, IdentityFile: "~/.ssh/id_ed25519", SSHArgs: []string{"-J", "bastion"}, Workdir: "~/proj"},
		"ssh abs dir":    {Type: "ssh", Host: "10.0.0.5", Workdir: "/srv/app"},
		"ssh ipv6":       {Type: "ssh", Host: "::1"},
	}
	for name, s := range good {
		if err := s.Validate("env"); err != nil {
			t.Errorf("%s should be valid: %v", name, err)
		}
	}

	bad := map[string]struct {
		s    Spec
		want string
	}{
		"no type":          {Spec{}, "give a type"},
		"unknown type":     {Spec{Type: "vm"}, "unknown type"},
		"docker nothing":   {Spec{Type: "docker"}, "image to start a container from"},
		"docker both":      {Spec{Type: "docker", Image: "a", Container: "b"}, "not both"},
		"docker ssh field": {Spec{Type: "docker", Image: "a", Host: "h"}, "only apply to ssh"},
		"docker rel dir":   {Spec{Type: "docker", Image: "a", Workdir: "work"}, "absolute path inside the container"},
		"image like flag":  {Spec{Type: "docker", Image: "--privileged"}, "not a valid image"},
		"image space":      {Spec{Type: "docker", Image: "a b"}, "not a valid image"},
		"attach + mounts":  {Spec{Type: "docker", Container: "c", Mounts: []string{"/a:/b"}}, "only apply with image"},
		"attach + network": {Spec{Type: "docker", Container: "c", Network: "none"}, "only apply with image"},
		"bad container":    {Spec{Type: "docker", Container: "a b"}, "not a valid container"},
		"mount no colon":   {Spec{Type: "docker", Image: "a", Mounts: []string{"/only"}}, "host:container"},
		"mount rel target": {Spec{Type: "docker", Image: "a", Mounts: []string{"/a:b"}}, "must be absolute"},
		"mount bad mode":   {Spec{Type: "docker", Image: "a", Mounts: []string{"/a:/b:rx"}}, "ro or rw"},
		"mount empty host": {Spec{Type: "docker", Image: "a", Mounts: []string{":/b"}}, "host path is empty"},
		"mount root":       {Spec{Type: "docker", Image: "a", Mounts: []string{"/:/host"}}, "control of the host"},
		"mount root dots":  {Spec{Type: "docker", Image: "a", Mounts: []string{"/tmp/..:/host"}}, "control of the host"},
		"mount socket":     {Spec{Type: "docker", Image: "a", Mounts: []string{"/var/run/docker.sock:/var/run/docker.sock"}}, "control of the host"},
		"bad network":      {Spec{Type: "docker", Image: "a", Network: "--net"}, "network"},
		"bad memory":       {Spec{Type: "docker", Image: "a", Memory: "lots"}, "memory"},
		"negative cpus":    {Spec{Type: "docker", Image: "a", CPUs: -1}, "cpus"},
		"bad env name":     {Spec{Type: "docker", Image: "a", Env: map[string]string{"A=B": "x"}}, "env name"},
		"bad pass_env":     {Spec{Type: "docker", Image: "a", PassEnv: []string{"A B"}}, "pass_env name"},
		"ssh no host":      {Spec{Type: "ssh"}, "give the host"},
		"ssh flag host":    {Spec{Type: "ssh", Host: "-oProxyCommand=evil"}, "not valid"},
		"ssh space host":   {Spec{Type: "ssh", Host: "a b"}, "not valid"},
		"ssh docker field": {Spec{Type: "ssh", Host: "h", Image: "x"}, "only apply to docker"},
		"ssh bad port":     {Spec{Type: "ssh", Host: "h", Port: 70000}, "port"},
		"ssh rel dir":      {Spec{Type: "ssh", Host: "h", Workdir: "proj"}, "absolute path or start with ~/"},
		"ssh user flag":    {Spec{Type: "ssh", Host: "h", User: "-oEvil"}, "user"},
		"ssh user at":      {Spec{Type: "ssh", Host: "h", User: "a@b"}, "user"},
	}
	for name, tc := range bad {
		err := tc.s.Validate("env")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, tc.want)
		}
	}

	for _, name := range []string{"", "local", "has space", "-lead", strings.Repeat("a", 33)} {
		if err := (Spec{Type: "ssh", Host: "h"}).Validate(name); err == nil {
			t.Errorf("the name %q should be rejected", name)
		}
	}
	if err := (Spec{Type: "ssh", Host: "h"}).Validate("local"); err == nil || !strings.Contains(err.Error(), "built in") {
		t.Errorf("local is built in: %v", err)
	}
}

func TestSpecWarnings(t *testing.T) {
	if w := (Spec{Type: "docker", Image: "a", Network: "host"}).Warnings("e"); len(w) != 1 || !strings.Contains(w[0], "network isolation") {
		t.Fatalf("%v", w)
	}
	if w := (Spec{Type: "docker", Image: "a", DockerArgs: []string{"--privileged"}}).Warnings("e"); len(w) != 1 {
		t.Fatalf("%v", w)
	}
	if w := (Spec{Type: "docker", Image: "a", Network: "none", DockerArgs: []string{"--cap-drop", "ALL"}}).Warnings("e"); len(w) != 0 {
		t.Fatalf("a hardened container is not a warning: %v", w)
	}
}

func TestOpenRejectsInvalidSpecsBeforeTouchingAnything(t *testing.T) {
	if _, err := Open(t.Context(), "x", Spec{Type: "docker", Image: "a", Mounts: []string{"/:/host"}}, func(string) string { return "" }); err == nil {
		t.Fatal("expected a validation error")
	}
}
