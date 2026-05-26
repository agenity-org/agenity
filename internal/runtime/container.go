package runtime

import (
	"fmt"
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
	// argv, env, cwd, and home directory. For bare exec this is just
	// argv. For Podman it wraps argv in `podman run ...`.
	SpawnArgs(agentName, agentHomeDir, cwd string, argv []string, env []string) ([]string, []string)
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

const agentUID = 1000 // UID of the `agent` user inside the chepherd-agent image

func (r *PodmanRuntime) AgentHomeDir(agentName, stateDir string) (string, error) {
	dir := filepath.Join(stateDir, "agents", agentName, "home")
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(filepath.Join(claudeDir, "projects"), 0o755); err != nil {
		return "", err
	}
	// Chown the tree to agentUID so the non-root agent process (running inside
	// a nested container where root maps to a different host UID) can read/write
	// its own home directory.
	for _, p := range []string{dir, claudeDir, filepath.Join(claudeDir, "projects")} {
		_ = os.Chown(p, agentUID, agentUID)
	}
	// Copy host Claude credentials into the agent home so the agent process
	// (UID agentUID, non-root) can authenticate. Refreshed on every spawn so
	// OAuth token rotation is reflected without a full restart.
	if src := hostClaudeCredentialsPath(); src != "" {
		if data, err := os.ReadFile(src); err == nil {
			dst := filepath.Join(claudeDir, ".credentials.json")
			if werr := os.WriteFile(dst, data, 0o600); werr == nil {
				_ = os.Chown(dst, agentUID, agentUID)
			}
		}
	}
	return dir, nil
}

func (r *PodmanRuntime) SpawnArgs(agentName, agentHomeDir, cwd string, argv []string, env []string) ([]string, []string) {
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
		"-v", agentHomeDir+":/home/agent:rw",
		// Working repo — read/write.
		"-v", cwd+":"+cwd+":rw",
		"--workdir", cwd,
	)

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
func (r *DockerRuntime) SpawnArgs(agentName, agentHomeDir, cwd string, argv []string, env []string) ([]string, []string) {
	// Docker variant — same flags as Podman except no --userns keep-id
	// (Docker handles this differently).
	dockerArgs := []string{
		"docker", "run", "--rm", "--interactive", "--tty",
		"--name", "chepherd-agent-" + agentName,
		"--network", "bridge",
		"-v", agentHomeDir + ":/home/agent:rw",
		"-v", cwd + ":" + cwd + ":rw",
		"--workdir", cwd,
		"-e", "HOME=/home/agent",
	}
	if hostCreds := hostClaudeCredentialsPath(); hostCreds != "" {
		dockerArgs = append(dockerArgs, "-v", hostCreds+":/home/agent/.claude/.credentials.json:ro")
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
func (r *BareExecRuntime) SpawnArgs(agentName, agentHomeDir, cwd string, argv []string, env []string) ([]string, []string) {
	return argv, env
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// mcpMounts returns the `-v host:container:mode` strings needed to give a
// container access to chepherd's MCP infrastructure. It reads three things
// from the agent's env vars:
//   - CHEPHERD_MCP_CONFIG  → the session dir (parent) mounted ro
//   - CHEPHERD_MCP_SOCK    → the Unix socket file mounted rw
//   - the chepherd binary  → looked up via os.Executable, mounted ro
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

	// Unix socket used by the MCP server.
	if sockPath := envMap["CHEPHERD_MCP_SOCK"]; sockPath != "" {
		if _, err := os.Stat(sockPath); err == nil {
			mounts = append(mounts, sockPath+":"+sockPath+":rw")
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
