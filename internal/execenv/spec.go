package execenv

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Spec describes an execution environment in the config file.
//
// A docker environment either starts a container of its own from image (and
// removes it when Talaria stops) or attaches to a running container by name.
// An ssh environment runs commands on a remote host through the system ssh
// program, so your ~/.ssh/config, keys and agent apply.
type Spec struct {
	Type    string `json:"type"`              // "docker" or "ssh"
	Workdir string `json:"workdir,omitempty"` // where relative paths and commands start
	User    string `json:"user,omitempty"`    // docker: the user inside the container; ssh: the login name

	// docker
	Image      string            `json:"image,omitempty"`     // start a container from this image
	Container  string            `json:"container,omitempty"` // or attach to this running container
	Mounts     []string          `json:"mounts,omitempty"`    // "host:container[:ro]"
	Network    string            `json:"network,omitempty"`   // e.g. "none"; default is Docker's own
	Memory     string            `json:"memory,omitempty"`    // e.g. "1g"
	CPUs       float64           `json:"cpus,omitempty"`
	Env        map[string]string `json:"env,omitempty"`      // non-secret variables set in the container
	PassEnv    []string          `json:"pass_env,omitempty"` // variables copied from Talaria's environment, by name
	DockerArgs []string          `json:"docker_args,omitempty"`

	// ssh
	Host         string   `json:"host,omitempty"` // a host name or an alias from ~/.ssh/config
	Port         int      `json:"port,omitempty"`
	IdentityFile string   `json:"identity_file,omitempty"`
	SSHArgs      []string `json:"ssh_args,omitempty"` // extra ssh options, e.g. ["-J", "bastion"]
}

var (
	envNameRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
	varNameRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	memoryRE    = regexp.MustCompile(`^[0-9]+[bkmgBKMG]?$`)
	networkRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	hostRE      = regexp.MustCompile(`^[A-Za-z0-9_:][A-Za-z0-9_.@:-]*$`)
	containerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// LocalName is the built-in environment: this machine. It cannot be redefined.
const LocalName = "local"

// The workspace is a built-in environment too: the persistent container made
// by `tal workspace up`. A config file may define its own "workspace" instead.
const (
	WorkspaceName      = "workspace"
	WorkspaceContainer = "talaria-workspace"
	WorkspaceDir       = "/workspace"
)

// WorkspaceSpec attaches to the workspace container.
func WorkspaceSpec() Spec {
	return Spec{Type: "docker", Container: WorkspaceContainer, Workdir: WorkspaceDir}
}

// ValidName reports whether name can name an environment.
func ValidName(name string) bool { return envNameRE.MatchString(name) && name != LocalName }

type mount struct{ host, container, mode string }

func parseMount(m string) (mount, error) {
	parts := strings.Split(m, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return mount{}, fmt.Errorf("mount %q: use host:container or host:container:ro", m)
	}
	mt := mount{host: parts[0], container: parts[1]}
	if len(parts) == 3 {
		mt.mode = parts[2]
	}
	switch {
	case mt.host == "":
		return mount{}, fmt.Errorf("mount %q: the host path is empty", m)
	case !strings.HasPrefix(mt.container, "/"):
		return mount{}, fmt.Errorf("mount %q: the container path must be absolute", m)
	case mt.mode != "" && mt.mode != "ro" && mt.mode != "rw":
		return mount{}, fmt.Errorf("mount %q: the mode must be ro or rw", m)
	}
	// Two mounts that give the agent control of the host are refused outright.
	// Anyone who really wants one can say so in docker_args.
	if hp := path.Clean(strings.ReplaceAll(mt.host, `\`, "/")); hp == "/" || strings.HasSuffix(hp, "docker.sock") {
		return mount{}, fmt.Errorf("mount %q: this would give the agent control of the host, so it is not allowed here", m)
	}
	return mt, nil
}

// Validate checks a spec. name is its key in the config.
func (s Spec) Validate(name string) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("environment %q: "+format, append([]any{name}, a...)...)
	}
	if !ValidName(name) {
		if name == LocalName {
			return bad("%q is built in and cannot be defined", LocalName)
		}
		return bad("use letters, digits, hyphens and underscores (max 32)")
	}

	switch s.Type {
	case "docker":
		return s.validateDocker(bad)
	case "ssh":
		return s.validateSSH(bad)
	case "":
		return bad(`give a type: "docker" or "ssh"`)
	}
	return bad(`unknown type %q (use "docker" or "ssh")`, s.Type)
}

func (s Spec) validateDocker(bad func(string, ...any) error) error {
	switch {
	case s.Image != "" && s.Container != "":
		return bad("give image (start a container) or container (attach to one), not both")
	case s.Image == "" && s.Container == "":
		return bad("give an image to start a container from, or the name of a running container")
	case s.Host != "" || s.Port != 0 || s.IdentityFile != "" || len(s.SSHArgs) > 0:
		return bad("host, port, identity_file and ssh_args only apply to ssh")
	case s.Workdir != "" && !strings.HasPrefix(s.Workdir, "/"):
		return bad("workdir must be an absolute path inside the container")
	}
	if s.Container != "" {
		if !containerRE.MatchString(s.Container) {
			return bad("container %q is not a valid container name", s.Container)
		}
		if len(s.Mounts) > 0 || s.Network != "" || s.Memory != "" || s.CPUs != 0 || len(s.Env) > 0 || len(s.PassEnv) > 0 || len(s.DockerArgs) > 0 {
			return bad("mounts, network, memory, cpus, env, pass_env and docker_args set up a new container, so they only apply with image")
		}
		return nil
	}

	if strings.HasPrefix(s.Image, "-") || strings.ContainsAny(s.Image, " \t\n") {
		return bad("image %q is not a valid image name", s.Image)
	}
	for _, m := range s.Mounts {
		if _, err := parseMount(m); err != nil {
			return bad("%v", err)
		}
	}
	if s.Network != "" && !networkRE.MatchString(s.Network) {
		return bad("network %q is not valid", s.Network)
	}
	if s.Memory != "" && !memoryRE.MatchString(s.Memory) {
		return bad(`memory %q is not valid: use a number with an optional b, k, m or g, such as "512m"`, s.Memory)
	}
	if s.CPUs < 0 {
		return bad("cpus cannot be negative")
	}
	for k := range s.Env {
		if !varNameRE.MatchString(k) {
			return bad("env name %q is not valid", k)
		}
	}
	for _, k := range s.PassEnv {
		if !varNameRE.MatchString(k) {
			return bad("pass_env name %q is not valid", k)
		}
	}
	return nil
}

func (s Spec) validateSSH(bad func(string, ...any) error) error {
	switch {
	case s.Host == "":
		return bad("give the host to connect to")
	case !hostRE.MatchString(s.Host):
		return bad("host %q is not valid", s.Host)
	case s.Image != "" || s.Container != "" || len(s.Mounts) > 0 || s.Network != "" || s.Memory != "" || s.CPUs != 0 ||
		len(s.Env) > 0 || len(s.PassEnv) > 0 || len(s.DockerArgs) > 0:
		return bad("image, container, mounts, network, memory, cpus, env, pass_env and docker_args only apply to docker")
	case s.Port < 0 || s.Port > 65535:
		return bad("port must be between 1 and 65535")
	case s.Workdir != "" && !strings.HasPrefix(s.Workdir, "/") && s.Workdir != "~" && !strings.HasPrefix(s.Workdir, "~/"):
		return bad("workdir must be an absolute path or start with ~/")
	case strings.ContainsAny(s.User, " \t\n@") || strings.HasPrefix(s.User, "-"):
		return bad("user %q is not valid", s.User)
	}
	return nil
}

// Warnings lists risky-but-valid settings.
func (s Spec) Warnings(name string) []string {
	var w []string
	if s.Type == "docker" && s.Network == "host" {
		w = append(w, fmt.Sprintf("environment %q uses the host network, which removes the container's network isolation", name))
	}
	for _, a := range s.DockerArgs {
		if a == "--privileged" || strings.HasPrefix(a, "--pid=host") || strings.HasPrefix(a, "--cap-add") {
			w = append(w, fmt.Sprintf("environment %q: docker_args %q weakens the container's isolation", name, a))
		}
	}
	return w
}

var secretKeyRE = regexp.MustCompile(`(?i)auth|key|token|secret|password|credential`)

// HasInlineSecret reports whether env sets a secret-looking variable to a
// literal value; pass_env copies it from the environment without writing it down.
func (s Spec) HasInlineSecret() bool {
	for k, v := range s.Env {
		if v != "" && secretKeyRE.MatchString(k) {
			return true
		}
	}
	return false
}

// Summary is a one-line description that needs no connection.
func (s Spec) Summary() string {
	switch {
	case s.Type == "docker" && s.Container != "":
		return "docker: attach to container " + s.Container
	case s.Type == "docker":
		return "docker: container from " + s.Image
	case s.Type == "ssh":
		target := s.Host
		if s.User != "" {
			target = s.User + "@" + s.Host
		}
		if s.Port != 0 {
			target += ":" + strconv.Itoa(s.Port)
		}
		return "ssh: " + target
	}
	return s.Type
}
