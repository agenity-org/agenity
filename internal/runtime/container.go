package runtime

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// extractEnv pulls one KEY out of a "KEY=VAL" slice. Returns "" if not
// present. Used for threading runtime-side values (e.g. the per-agent
// PVC handle minted by the Agent registry, #172) through the existing
// ContainerRuntime.SpawnArgs interface without changing the signature.
func extractEnv(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix)
		}
	}
	return ""
}

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

// PodmanRuntime spawns each agent as a SIBLING container on the same
// podman that runs chepherd itself. v0.8/v0.9 architecture: one
// chepherd container + N agent containers, all visible to `podman ps`
// on the host. When chepherd is itself containerised, it reaches the
// host podman via the bind-mounted socket using the --remote flag.
// scripts/start.sh sets up the socket bind-mount at
// /run/host-podman/podman.sock.
//
// The earlier nested-podman design (commit f958359) put agent
// containers inside chepherd's own filesystem at
// /var/lib/chepherd-agents/storage — that broke the host visibility
// contract and was a misread of issue #124 ("containerize chepherd
// daemon"). Removed entirely.
type PodmanRuntime struct{}

// hostPodmanSocketPath is the path inside the chepherd container at
// which the host's rootless podman socket is bind-mounted. Matches
// scripts/start.sh. Empty if the file doesn't exist (= we're not
// running inside the chepherd container; podman talks to its own
// storage locally).
const hostPodmanSocketPath = "/run/host-podman/podman.sock"

// podmanArgs returns the argv prefix for invoking the podman CLI from
// inside the chepherd container. When the bind-mounted host socket is
// present, prefix with "--remote --url unix://..." so every podman
// call lands on the host daemon. Otherwise return just ["podman"] so
// dev-mode (chepherd running on the host directly) uses local storage.
func podmanArgs() []string {
	if _, err := os.Stat(hostPodmanSocketPath); err == nil {
		return []string{"podman", "--remote", "--url", "unix://" + hostPodmanSocketPath}
	}
	return []string{"podman"}
}

// toHostPath translates a path that exists inside the chepherd
// container (e.g. /home/chepherd/repos/foo) to the equivalent host
// path (e.g. /home/openova/repos/foo) the host podman daemon will
// see when constructing bind-mounts. Returns the input unchanged when
// we're not running containerised (the host-state-dir env vars are
// only set by scripts/start.sh when chepherd is itself in a pod).
//
// Mappings come from env vars set by scripts/start.sh:
//
//	CHEPHERD_HOST_STATE_DIR  ← inside: /home/chepherd/.local/state/chepherd
//	CHEPHERD_HOST_REPOS_DIR  ← inside: /home/chepherd/repos
//	CHEPHERD_HOST_CLAUDE_DIR ← inside: /home/chepherd/.claude
func toHostPath(p string) string {
	type mapping struct{ in, env string }
	maps := []mapping{
		{"/home/chepherd/.local/state/chepherd", "CHEPHERD_HOST_STATE_DIR"},
		{"/home/chepherd/repos", "CHEPHERD_HOST_REPOS_DIR"},
		{"/home/chepherd/.claude", "CHEPHERD_HOST_CLAUDE_DIR"},
	}
	for _, m := range maps {
		host := os.Getenv(m.env)
		if host == "" {
			continue
		}
		if p == m.in {
			return host
		}
		if strings.HasPrefix(p, m.in+"/") {
			return host + p[len(m.in):]
		}
	}
	return p
}

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
	// v0.8/v0.9 architecture: ONE chepherd container + N SIBLING agent
	// containers on the host podman. Each agent appears in the operator's
	// `podman ps` like any other container.
	//
	// When chepherd runs containerised, it talks to the HOST podman via
	// the bind-mounted /run/host-podman/podman.sock (set by start.sh
	// via CONTAINER_HOST=unix:///run/host-podman/podman.sock). The
	// previous nested-podman design (introduced in f958359 for the
	// chepherd-as-pod plan) is gone — agent containers no longer live
	// inside chepherd's filesystem.
	// Translate every chepherd-container path to its host equivalent
	// so the host podman daemon can resolve the bind-mount sources.
	hostHome := toHostPath(agentHomeDir)
	hostSecrets := toHostPath(agentSecretsDir)
	hostCwd := toHostPath(cwd)

	podArgs := append(podmanArgs(),
		"run", "--rm", "--interactive", "--tty",
		"--name", "chepherd-agent-"+agentName,
		// Default bridge network — sibling to chepherd on host podman.
		"--network", "bridge",
		// Per-agent persistent home (claude session files, config).
		"-v", hostHome+":/home/agent:rw,U",
		// Working repo — read/write. Source is the host path; the
		// agent sees its workdir at the original cwd (chepherd-view)
		// since claude-code expects that string to match its prompts.
		"-v", hostCwd+":"+cwd+":rw",
		"--workdir", cwd,
	)

	// #172 — per-agent PVC. Lives on the HOST podman (sibling to the
	// agent container), visible via `podman volume ls` on the host.
	if pvcHandle := extractEnv(env, "CHEPHERD_PVC_HANDLE"); pvcHandle != "" {
		base := podmanArgs()
		if exec.Command(base[0], append(append([]string{}, base[1:]...), "volume", "exists", pvcHandle)...).Run() != nil {
			_ = exec.Command(base[0], append(append([]string{}, base[1:]...), "volume", "create", pvcHandle)...).Run()
		}
		podArgs = append(podArgs, "-v", pvcHandle+":/workspace:rw,U")
	}

	// Per-agent secrets — mounted at /run/secrets so the entrypoint script
	// in the agent image can link /run/secrets/claude-credentials into the
	// agent's home (R4). Once the token vault (#131) lands, all token
	// material is written here from the vault at spawn time.
	if agentSecretsDir != "" {
		podArgs = append(podArgs, "-v", hostSecrets+":/run/secrets:ro,U")
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
	// #172 — same per-agent PVC mount as PodmanRuntime above. Docker
	// has no equivalent of podman's --root scoping; create the named
	// volume on the default Docker engine.
	if pvcHandle := extractEnv(env, "CHEPHERD_PVC_HANDLE"); pvcHandle != "" {
		if exec.Command("docker", "volume", "inspect", pvcHandle).Run() != nil {
			_ = exec.Command("docker", "volume", "create", pvcHandle).Run()
		}
		dockerArgs = append(dockerArgs, "-v", pvcHandle+":/workspace:rw")
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

	// Session dir containing the .mcp.json file. Source path must be
	// host-visible (toHostPath translates /home/chepherd/.local/state/
	// → CHEPHERD_HOST_STATE_DIR for sibling-container spawn).
	if cfgPath := envMap["CHEPHERD_MCP_CONFIG"]; cfgPath != "" {
		sessDir := filepath.Dir(cfgPath)
		if _, err := os.Stat(sessDir); err == nil {
			mounts = append(mounts, toHostPath(sessDir)+":"+sessDir+":ro")
		}
	}

	// Chepherd binary — claude-code spawns it as an MCP subprocess.
	// Two modes:
	//   - Dev (chepherd on host): bind-mount the executable into the
	//     agent so the agent's `chepherd mcp` subprocess can launch.
	//   - Containerised (host socket present): the executable lives
	//     inside the chepherd container's filesystem — NOT visible to
	//     the host podman daemon. Skip the mount; the chepherd-agent
	//     image ships its own /usr/local/bin/chepherd binary (built
	//     by Dockerfile.agent) so the MCP bridge launches from the
	//     image's copy.
	if _, err := os.Stat(hostPodmanSocketPath); err != nil {
		// Dev mode — host has the executable.
		if exe, err := os.Executable(); err == nil {
			if _, err := os.Stat(exe); err == nil {
				mounts = append(mounts, exe+":"+exe+":ro")
			}
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
	// With sibling-container architecture, the chepherd container is
	// reachable by name on the same podman network, OR by the host's
	// outbound IP. Try outbound first.
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
	base := podmanArgs()
	if err := exec.Command(base[0], append(append([]string{}, base[1:]...), "image", "exists", image)...).Run(); err == nil {
		return true
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err == nil {
		return true
	}
	return false
}
