package runtime

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ContainerRuntime abstracts how agent processes are launched.
// PodmanRuntime wraps each agent in a rootless Podman container.
// BareExecRuntime runs agents directly on the host (fallback).
// KubernetesRuntime (future) creates Pods via client-go.
type ContainerRuntime interface {
	// Name returns "podman", "docker", or "bare".
	Name() string
	// Available returns nil if the runtime can be used on this machine.
	Available() error
	// AgentHomeDir returns (and creates) the per-agent persistent home
	// directory on the HOST that is bind-mounted into the container.
	AgentHomeDir(agentName string, stateDir string) (string, error)
	// SpawnArgs returns the full argv to execute, given the agent's
	// argv, env, cwd, home directory, and secrets directory. For bare
	// exec this is just argv. For Podman it wraps argv in `podman run ...`.
	// The secrets directory is materialized by the runtime (which has
	// access to the token vault); the container runtime just bind-mounts
	// it at /run/secrets.
	SpawnArgs(agentName, agentHomeDir, agentSecretsDir, cwd string, argv []string, env []string) ([]string, []string)
}

// DetectRuntime returns the best available ContainerRuntime.
// Order: Podman > Docker > BareExec.
func DetectRuntime() ContainerRuntime {
	p := &PodmanRuntime{}
	if p.Available() == nil {
		return p
	}
	d := &DockerRuntime{}
	if d.Available() == nil {
		return d
	}
	return &BareExecRuntime{}
}

// ─── Podman ──────────────────────────────────────────────────────────────────

// agentStorageRoot / agentRunRoot are the bind-mounted paths inside the
// chepherd container where the pre-loaded agent image lives.
// start.sh mounts AGENT_STORAGE to /var/lib/chepherd-agents; skopeo pre-
// populates ${AGENT_STORAGE}/storage before the container starts.
const (
	agentStorageRoot = "/var/lib/chepherd-agents/storage"
	agentRunRoot     = "/var/lib/chepherd-agents/run"
)

// PodmanRuntime spawns each agent as a Podman container managed by the
// chepherd container's own internal podman (running as root inside a
// --privileged outer container). The container is ephemeral (--rm); state
// persists via bind mounts.
type PodmanRuntime struct{}

func (r *PodmanRuntime) Name() string { return "podman" }

func (r *PodmanRuntime) Available() error {
	if _, err := exec.LookPath("podman"); err != nil {
		return fmt.Errorf("podman not in PATH")
	}
	if !imageExists("chepherd-agent:latest") {
		return fmt.Errorf("chepherd-agent:latest image not found; run: make agent-image")
	}
	return nil
}

// AgentHomeDir returns (and creates) the per-agent state directory on the
// host. v0.8 design: chepherd no longer copies credentials into the home
// dir directly — credentials are delivered via /run/secrets bind-mount,
// and the entrypoint script in the agent image links them into place.
// The home dir is just persistent storage (projects/, session files,
// claude-code's auto-save state). No chown hacks — Podman's `:U` mount
// flag handles UID remapping into the container's user namespace.
func (r *PodmanRuntime) AgentHomeDir(agentName, stateDir string) (string, error) {
	dir := filepath.Join(stateDir, "agents", agentName, "home")
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(filepath.Join(claudeDir, "projects"), 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// agentSecretsDirPath returns the on-host directory used as the source
// for the /run/secrets bind-mount. Materializing the directory's contents
// is the runtime's responsibility (it has access to the token vault);
// this helper just resolves and creates the path.
func agentSecretsDirPath(agentName, stateDir string) (string, error) {
	dir := filepath.Join(stateDir, "agents", agentName, "secrets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func (r *PodmanRuntime) SpawnArgs(agentName, agentHomeDir, agentSecretsDir, cwd string, argv []string, env []string) ([]string, []string) {
	// When running inside the chepherd container, explicit --root / --runroot
	// point to the pre-populated agent storage (written by skopeo). Outside the
	// container (dev mode), those paths don't exist — omit them so podman uses
	// its default storage on the host.
	podArgs := []string{"podman"}
	if _, err := os.Stat(agentStorageRoot); err == nil {
		podArgs = append(podArgs, "--root", agentStorageRoot, "--runroot", agentRunRoot)
	}
	podArgs = append(podArgs,
		"run", "--rm", "--interactive", "--tty",
		"--name", "chepherd-agent-"+agentName,
		// Bridge networking — slirp4netns is rootless-only inside the container.
		// Outside the container (dev mode), use host networking.
		"--network", "bridge",
		// Per-agent persistent home (claude session files, config).
		// :U remaps file ownership into the container's user namespace so
		// the in-container `agent` user (UID 1000) owns these paths
		// without us touching them from the host.
		"-v", agentHomeDir+":/home/agent:rw,U",
		// Working repo — read/write.
		"-v", cwd+":"+cwd+":rw",
		"--workdir", cwd,
	)

	// Per-agent secrets — mounted at /run/secrets so the entrypoint script
	// in the agent image can link /run/secrets/claude-credentials into the
	// agent's home (R4). Once the token vault (#131) lands, all token
	// material is written here from the vault at spawn time.
	if agentSecretsDir != "" {
		podArgs = append(podArgs, "-v", agentSecretsDir+":/run/secrets:ro,U")
	}

	// Mount MCP infrastructure: the session dir (contains .mcp.json), the
	// chepherd binary (run as subprocess by claude-code), and the Unix socket.
	// All three live at host paths that must be bind-mounted into the container.
	for _, mount := range mcpMounts(env) {
		podArgs = append(podArgs, "-v", mount)
	}

	// Inject env vars as -e KEY=VAL.
	for _, e := range env {
		podArgs = append(podArgs, "-e", e)
	}

	// Override HOME so claude-code writes to the mounted home dir.
	podArgs = append(podArgs, "-e", "HOME=/home/agent")
	// TERM=xterm-256color so claude-code uses 256-color escapes (dusty-pink
	// mascot, light-blue /usage bar). Default TERM=xterm forces the 16-color
	// fallback path which renders the mascot as harsh ANSI red + bars as
	// bright-white. xterm.js in the browser fully supports 256-color.
	podArgs = append(podArgs, "-e", "TERM=xterm-256color")
	podArgs = append(podArgs, "-e", "COLORTERM=truecolor")
	// claude-code ships an "auto-updater" that runs `npm i -g
	// @anthropic-ai/claude-code` on every start. In chepherd's containerised
	// agents this is wrong on three counts: (1) the agent user (UID 1000) has
	// no write perms on /usr/lib/node_modules so it always fails; (2) the
	// container is ephemeral — any successful update would be lost next
	// spawn; (3) the canonical update path is bumping the chepherd-agent
	// image tag, not mutating a running container. Disable the noisy banner.
	podArgs = append(podArgs, "-e", "DISABLE_AUTOUPDATER=1")

	podArgs = append(podArgs, "chepherd-agent:latest")
	podArgs = append(podArgs, argv...)

	// Podman manages its own env via -e flags; return empty env slice so
	// ptyhost doesn't double-inject.
	return podArgs, nil
}

// ─── Docker ──────────────────────────────────────────────────────────────────

type DockerRuntime struct{}

func (r *DockerRuntime) Name() string { return "docker" }
func (r *DockerRuntime) Available() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not in PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		return fmt.Errorf("docker daemon not running")
	}
	return nil
}
func (r *DockerRuntime) AgentHomeDir(agentName, stateDir string) (string, error) {
	return (&PodmanRuntime{}).AgentHomeDir(agentName, stateDir)
}
func (r *DockerRuntime) SpawnArgs(agentName, agentHomeDir, agentSecretsDir, cwd string, argv []string, env []string) ([]string, []string) {
	// Docker variant — same flags as Podman except no :U mount option
	// (Docker handles UID remapping differently).
	dockerArgs := []string{
		"docker", "run", "--rm", "--interactive", "--tty",
		"--name", "chepherd-agent-" + agentName,
		"--network", "bridge",
		"-v", agentHomeDir + ":/home/agent:rw",
		"-v", cwd + ":" + cwd + ":rw",
		"--workdir", cwd,
		"-e", "HOME=/home/agent",
		"-e", "TERM=xterm-256color",
		"-e", "COLORTERM=truecolor",
		"-e", "DISABLE_AUTOUPDATER=1",
	}
	if agentSecretsDir != "" {
		dockerArgs = append(dockerArgs, "-v", agentSecretsDir+":/run/secrets:ro")
	}
	for _, mount := range mcpMounts(env) {
		dockerArgs = append(dockerArgs, "-v", mount)
	}
	for _, e := range env {
		dockerArgs = append(dockerArgs, "-e", e)
	}
	dockerArgs = append(dockerArgs, "chepherd-agent:latest")
	dockerArgs = append(dockerArgs, argv...)
	return dockerArgs, nil
}

// ─── BareExec ────────────────────────────────────────────────────────────────

// BareExecRuntime runs agents directly on the host — no isolation.
// Used as a fallback when neither Podman nor Docker is available.
// Warns in the UI via the session's "container_runtime" field.
type BareExecRuntime struct{}

func (r *BareExecRuntime) Name() string { return "bare" }
func (r *BareExecRuntime) Available() error { return nil }
func (r *BareExecRuntime) AgentHomeDir(agentName, stateDir string) (string, error) {
	// On bare exec, use the real host home — no isolation.
	return os.UserHomeDir()
}
func (r *BareExecRuntime) SpawnArgs(agentName, agentHomeDir, agentSecretsDir, cwd string, argv []string, env []string) ([]string, []string) {
	return argv, env
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// mcpMounts returns the `-v host:container:mode` strings needed to give a
// container access to chepherd's MCP infrastructure. As of v0.8 the MCP
// transport is HTTP/WS over TCP — there is no Unix socket to bind-mount,
// so the agent reaches chepherd via DNS (host.containers.internal on
// Podman, the chepherd Service on K8s). We still mount:
//   - CHEPHERD_MCP_CONFIG  → the session dir containing .mcp.json (ro)
//   - the chepherd binary  → looked up via os.Executable (ro), so the
//     agent's stdio→WS bridge subprocess can launch
//
// Paths that don't exist are silently skipped.
func mcpMounts(env []string) []string {
	envMap := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			envMap[k] = v
		}
	}

	var mounts []string

	// Session dir containing the .mcp.json file.
	if cfgPath := envMap["CHEPHERD_MCP_CONFIG"]; cfgPath != "" {
		sessDir := filepath.Dir(cfgPath)
		if _, err := os.Stat(sessDir); err == nil {
			mounts = append(mounts, sessDir+":"+sessDir+":ro")
		}
	}

	// Chepherd binary — claude-code spawns it as an MCP subprocess.
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(exe); err == nil {
			mounts = append(mounts, exe+":"+exe+":ro")
		}
	}

	return mounts
}

// HostAddrForAgent returns an IP address agent containers can dial to
// reach chepherd's MCP listener on :9090. Two modes:
//
//  1. Chepherd is running INSIDE a pod (its own inner podman manages
//     agents): the inner-bridge gateway (typically 10.88.0.1) IS the
//     chepherd container's IP from the agent's perspective. Use it.
//
//  2. Chepherd is running on the host directly: the rootless-podman
//     bridge gateway is a phantom (not routed to a real host interface),
//     so we fall back to the host's primary outbound IP via the
//     UDP-socket-LocalAddr trick.
//
// Returns "" if neither mode resolves; callers default CHEPHERD_MCP_URL.
func HostAddrForAgent() string {
	// Mode 1: are we inside a chepherd pod? Detected via the agent-storage
	// bind mount that scripts/start.sh sets up. If yes, the inner-podman
	// bridge gateway is the right answer.
	if _, err := os.Stat(agentStorageRoot); err == nil {
		if gw := podmanInnerBridgeGateway(); gw != "" {
			return gw
		}
	}
	// Mode 2: outbound IP.
	c, err := net.Dial("udp4", "1.1.1.1:53")
	if err != nil {
		return ""
	}
	defer c.Close()
	if addr, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return ""
}

// podmanInnerBridgeGateway returns the gateway IP of the inner-podman
// default bridge network. Empty string if podman isn't available or
// the inspect fails.
func podmanInnerBridgeGateway() string {
	args := []string{"--root", agentStorageRoot, "--runroot", agentRunRoot,
		"network", "inspect", "podman"}
	out, err := exec.Command("podman", args...).Output()
	if err != nil {
		return ""
	}
	s := string(out)
	idx := strings.Index(s, `"gateway":`)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(`"gateway":`):]
	q1 := strings.Index(rest, `"`)
	if q1 < 0 {
		return ""
	}
	rest = rest[q1+1:]
	q2 := strings.Index(rest, `"`)
	if q2 < 0 {
		return ""
	}
	return rest[:q2]
}

// hostClaudeCredentialsPath returns the path to the host's Claude credentials
// file if it exists, or "" if not found. Used to pre-authenticate containers.
func hostClaudeCredentialsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".claude", ".credentials.json")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func imageExists(image string) bool {
	// Check the mounted agent storage first (inside container or explicit root).
	err := exec.Command("podman",
		"--root", agentStorageRoot,
		"--runroot", agentRunRoot,
		"image", "exists", image).Run()
	if err == nil {
		return true
	}
	// Fallback: default podman storage (dev mode, running outside container).
	err = exec.Command("podman", "image", "exists", image).Run()
	if err == nil {
		return true
	}
	err = exec.Command("docker", "image", "inspect", image).Run()
	return err == nil
}
