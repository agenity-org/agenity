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
	// StopContainer terminates + removes the named sibling container.
	// #258 — Runtime.Stop only closed the PTY which left containers
	// leaking on operator's `podman ps` (19 zombies counted).
	// Implementations must be best-effort: a container that's already
	// gone is not an error. `name` is the agent label (without the
	// `chepherd-agent-<uuid>-` prefix); implementations prepend the
	// prefix using the instance UUID set via SetInstanceUUID.
	StopContainer(name string) error
	// ListAgentContainers returns all live OR exited containers whose
	// name starts with `chepherd-agent-<this-instance-uuid>-`. #270 —
	// pre-#270 the filter was `chepherd-agent-` (matched ALL chepherd
	// instances' agents) which made `ReapOrphanContainers` cross-kill
	// agents owned by a second chepherd binary on the same host. The
	// scoped filter ensures only THIS instance's agents are surfaced.
	ListAgentContainers() ([]string, error)
	// SetInstanceUUID configures the 8-char chepherd-instance UUID that
	// the runtime prefixes onto container names + filters by. #270 —
	// each chepherd binary derives a stable UUID from the absolute path
	// of its state-dir (see runtime.instanceUUID), so two binaries with
	// distinct state-dirs spawn distinct container-name pools and never
	// cross-kill each other. Implementations that don't manage
	// containers (BareExec) accept and ignore.
	SetInstanceUUID(uuid string)
	// ProbeContainerRunning checks whether the named agent container is
	// actually running after a spawn. Returns (true, "", nil) when the
	// container's State.Status is "running". Returns (false, ociErr, nil)
	// when the container exists but is NOT running (configured/created/
	// exited) — ociErr is the OCI runtime error message (e.g. "create
	// keyring: Disk quota exceeded"). Returns (false, "", err) when the
	// probe itself fails (e.g. podman not available, container never
	// recorded). BareExec implementations always return (true, "", nil).
	//
	// name is the agent label (without the chepherd-agent-<uuid>- prefix);
	// implementations prepend the prefix. Callers must wait a short
	// grace period (≥1s) before calling so the OCI runtime has had time
	// to attempt container start.
	//
	// #592 post-spawn container health check.
	ProbeContainerRunning(name string) (running bool, ociErr string, err error)
}

// DetectRuntime returns the best available ContainerRuntime.
// Order: Podman > Docker > BareExec. Caller must call SetInstanceUUID
// on the result before any SpawnArgs / StopContainer / ListAgentContainers
// invocation so the #270 instance-scoping holds.
//
// #383 P0 diagnostic: silently falling through to BareExec when
// PodmanRuntime+DockerRuntime are both unavailable was the root
// cause of operator-perceived "spawn pipeline broken" — agents got
// fork/exec'd as `/usr/bin/claude` on the chepherd container (which
// doesn't have it), with no surfacing of the fallback. The healthz
// also reported `spawner:podman-sidecar` (hardcoded in
// LocalRuntimeSpawner.Mode regardless of cr) which misled the
// operator into bisecting unrelated PRs. We now emit a loud stderr
// line on each fallback so the actual blocker (most often: missing
// chepherd-agent:latest image — `make agent-image` needed) is
// visible in chepherd's boot log instead of buried in a misleading
// "claude not found" downstream error.
func DetectRuntime() ContainerRuntime {
	// #522 — CHEPHERD_FORCE_BAREEXEC=1 forces BareExec immediately,
	// skipping the Podman/Docker probes. Used by the e2e harness so
	// tests run deterministically on hosts where rootless podman
	// rejects new containers (kernel keyring quota, persistent
	// keyring storage full, missing CAP_SYS_ADMIN, etc.) without
	// pulling those environment failures into test assertions about
	// unrelated CONTRACTS (team-routing, MCP RPC envelopes, etc.).
	// Tests that specifically exercise the container path opt back
	// in via NOT setting this var.
	if os.Getenv("CHEPHERD_FORCE_BAREEXEC") == "1" {
		fmt.Fprintf(os.Stderr, "[chepherd-runtime-detect] CHEPHERD_FORCE_BAREEXEC=1 — forcing BareExecRuntime (e2e test mode)\n")
		return &BareExecRuntime{}
	}
	p := &PodmanRuntime{}
	if err := p.Available(); err == nil {
		return p
	} else {
		fmt.Fprintf(os.Stderr, "[chepherd-runtime-detect] PodmanRuntime unavailable: %v\n", err)
	}
	d := &DockerRuntime{}
	if err := d.Available(); err == nil {
		return d
	} else {
		fmt.Fprintf(os.Stderr, "[chepherd-runtime-detect] DockerRuntime unavailable: %v\n", err)
	}
	fmt.Fprintf(os.Stderr,
		"[chepherd-runtime-detect] ⚠ FALLBACK to BareExecRuntime — agents will fork/exec on the chepherd host directly.\n"+
			"[chepherd-runtime-detect]   This is almost certainly NOT what you want. Run `make agent-image` to build chepherd-agent:latest, then bounce chepherd. (#383 P0)\n")
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
type PodmanRuntime struct {
	// instanceUUID is the 8-char chepherd-instance fingerprint set by
	// SetInstanceUUID (#270). Empty until configured — defensive: if
	// never set, container names fall back to the pre-#270 unscoped
	// shape so a forgotten configure doesn't silently break spawn.
	instanceUUID string
}

// containerNamePrefix returns the per-instance prefix used for all
// chepherd-agent-* container names. With UUID set (#270 canonical
// path): "chepherd-agent-<uuid>-". Without UUID (defensive fallback
// or BareExec): "chepherd-agent-".
func containerNamePrefix(uuid string) string {
	if uuid == "" {
		return "chepherd-agent-"
	}
	return "chepherd-agent-" + uuid + "-"
}

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

func (r *PodmanRuntime) SetInstanceUUID(uuid string) { r.instanceUUID = uuid }

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

	// --replace: if a previous container with this name still exists
	// (running OR exited but not cleaned up), stop + remove it before
	// the new spawn. Without this, every re-spawn of the same agent
	// name fails with exit 125 ("name is already in use") — operator
	// hit this directly during 2026-05-28 walk after my own test runs
	// left stale containers behind. --rm covers cleanup-on-exit;
	// --replace covers reuse-after-prior-leak. Both are needed.
	podArgs := append(podmanArgs(),
		"run", "--replace", "--interactive", "--tty", // #363: --rm removed; corpses persist for podman inspect; --replace handles name reuse
		"--publish-all=false", // #648: suppress auto-publishing of EXPOSE ports from the agent image — those ports (8080/9090) belong to the daemon only
		// #270 — instance-scoped container name. The prefix carries
		// this chepherd binary's UUID so a parallel chepherd binary
		// on the same host can't clobber or reap these containers.
		"--name", containerNamePrefix(r.instanceUUID)+agentName,
		// #365 — network mode. Default is slirp4netns
		// (rootless-friendly; no CNI plugin required) which matches
		// the CHEPHERD_MCP_URL=ws://10.0.2.100:... env contract. Pre-
		// #365 default was "bridge" which fails on rootless podman
		// without CNI ("failed to mount netns directory for rootless
		// cni: no such file or directory", exit 127). Operators on
		// rootful podman with CNI installed (or who explicitly want
		// per-pod network isolation) override via the CHEPHERD_CONTAINER_NETWORK
		// env var.
		"--network", agentNetworkMode(),
		// #372 P0 — DROP explicit --add-host. Podman 4.x+ provides
		// host.containers.internal DNS resolution automatically under
		// slirp4netns + bridge. My #370 added an explicit
		// "--add-host host.containers.internal:host-gateway" that
		// Podman REJECTS with "Error: invalid IP address in add-host:
		// host-gateway" → exit 125 → container dies before claude
		// starts. host-gateway is a Docker convention; Podman's
		// equivalent is the auto-injected entry under
		// slirp4netns/bridge modes.
		// Operators on Podman versions older than 4.x OR Docker who
		// need an explicit shim can set CHEPHERD_MCP_URL to a
		// direct IP override (e.g. CHEPHERD_MCP_URL=ws://10.0.2.2:9090/mcp/ws
		// for slirp4netns gateway).
		// Per-agent persistent home (claude session files, config).
		"-v", hostHome+":/home/agent:rw,U",
		// Working repo — read/write. ,U flag is conditional: SAFE on
		// chepherd-managed workspace clones (under state-dir), FATAL
		// on operator-owned host bind mounts (recursive chown fails
		// on foreign-owned files like iogrid/.git/index → container
		// stuck in Created). Heuristic: ,U only when path includes
		// the managed workspaces/ path. Operator-hit 2026-06-02.
		"-v", hostCwd+":"+cwd+workspaceMountFlags(hostCwd),
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
		// #273 — log the actual bind-mount so the operator's bastion log
		// shows the host path being mapped into /run/secrets and can
		// confirm via `podman inspect ... HostConfig.Binds` that the
		// chosen path matches an existing claude-credentials file.
		fmt.Fprintf(os.Stderr, "[chepherd-spawn-mount] %s: -v %s:/run/secrets:ro,U\n", agentName, hostSecrets)
	} else {
		fmt.Fprintf(os.Stderr, "[chepherd-spawn-mount] %s: agentSecretsDir empty — /run/secrets NOT mounted (claude-code will fall into OAuth login)\n", agentName)
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
	// Same trap for the GitHub Copilot CLI: @github/copilot self-updates its
	// linux-x64 binary on start and hits EACCES on the root-owned
	// /usr/lib/node_modules — which DERAILS the agent (operator-observed
	// 2026-06-20: qa/copilot connected MCP but never called get_task, stuck on
	// the failed update). COPILOT_AUTO_UPDATE=false disables that self-update.
	podArgs = append(podArgs, "-e", "COPILOT_AUTO_UPDATE=false")
	// Second copilot startup-gate: on first launch in a not-yet-trusted folder
	// the CLI renders a blocking "Confirm folder trust" modal that ONLY listens
	// for ↑/↓/Enter/Esc/1/2/3. The injected [chepherd-knock] marker + CR is
	// swallowed by that modal (the CR just dismisses it) so the knock never
	// reaches copilot as a prompt — agent shows "Session: 0 AIC used", never
	// calls get_task (operator-observed 2026-06-20). The bundle's trust gate is
	// `process.env.COPILOT_ALLOW_ALL === "true" || <folder in trustedFolders>`,
	// so COPILOT_ALLOW_ALL=true skips the modal outright (A/B-verified against
	// the real binary). This is copilot's analogue of claude-code's
	// permission-bypass; it also enables all tool/path/url permissions, the
	// intended full-autonomy posture for a sandboxed mesh agent (pairs with the
	// --allow-all-tools DefaultArg).
	podArgs = append(podArgs, "-e", "COPILOT_ALLOW_ALL=true")

	podArgs = append(podArgs, "chepherd-agent:latest")
	podArgs = append(podArgs, argv...)

	// Podman manages its own env via -e flags; return empty env slice so
	// ptyhost doesn't double-inject.
	return podArgs, nil
}

// StopContainer terminates + removes the sibling agent container.
// Best-effort: a container that's already gone is not an error. We
// run `podman stop --time 5` followed by `podman rm -f` so an
// already-stopped-but-not-removed container still gets cleaned up.
//
// #258 — Before this PR, Runtime.Stop only closed the PTY; the
// `podman run --rm` cleanup didn't fire reliably (operator counted
// 19 zombies). Explicit stop+rm here is the source of truth.
func (r *PodmanRuntime) StopContainer(name string) error {
	full := containerNamePrefix(r.instanceUUID) + name
	args := podmanArgs()
	// #258 reopen + #272 — operator reports stuck-stops on the bastion.
	// PR #260 added the call-through; PR #268 added stderr logging; #272
	// normalises the prefix to `[chepherd-stop]` so walker can grep the
	// whole Stop chain (Runtime.Stop entry → StopContainer call → podman
	// stop/rm shell-outs) with a single token.
	fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: PodmanRuntime.StopContainer enter (full=%s)\n", name, full)
	stopArgs := append(append([]string{}, args[1:]...), "stop", "--time", "5", full)
	stopOut, stopErr := exec.Command(args[0], stopArgs...).CombinedOutput()
	if stopErr != nil {
		s := strings.ToLower(string(stopOut))
		// "no such container" is the only benign case — anything else
		// is the operator's stuck-stop bug surfacing. Don't fail the
		// chain (rm -f below will retry), but DO log.
		if !strings.Contains(s, "no such container") && !strings.Contains(s, "not found") {
			fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: podman stop FAILED: %v (%s)\n", name, stopErr, strings.TrimSpace(string(stopOut)))
		} else {
			fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: podman stop: already gone\n", name)
		}
	} else {
		fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: podman stop ok (%s)\n", name, strings.TrimSpace(string(stopOut)))
	}
	rmArgs := append(append([]string{}, args[1:]...), "rm", "-f", full)
	rm := exec.Command(args[0], rmArgs...)
	out, err := rm.CombinedOutput()
	if err != nil {
		s := strings.ToLower(string(out))
		// "no such container" / "not found" => already gone, success.
		if strings.Contains(s, "no such container") || strings.Contains(s, "not found") {
			fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: podman rm -f: already gone (clean)\n", name)
			return nil
		}
		fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: podman rm -f FAILED: %v (%s)\n", name, err, strings.TrimSpace(string(out)))
		return fmt.Errorf("podman rm %s: %w (%s)", full, err, strings.TrimSpace(string(out)))
	}
	fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: podman rm -f ok (%s)\n", name, strings.TrimSpace(string(out)))
	return nil
}

// ListAgentContainers returns the names of all containers whose name
// starts with `chepherd-agent-`. Includes BOTH running + exited so
// the orphan cleanup helper can reap exited shells too. Empty list
// when no agents exist; error only on podman-call failure.
func (r *PodmanRuntime) ListAgentContainers() ([]string, error) {
	args := podmanArgs()
	// #270 — filter on the instance-scoped prefix so a second
	// chepherd binary on the same host can't see/reap our agents,
	// and we don't see/reap theirs. Pre-#270 containers with the
	// unscoped `chepherd-agent-<slug>` shape are intentionally NOT
	// matched here — they age out via natural operator churn and
	// the migration cost is operator-side `podman rm -f` of the
	// pre-fix containers (documented in the #270 PR body).
	prefix := containerNamePrefix(r.instanceUUID)
	psArgs := append(append([]string{}, args[1:]...),
		"ps", "-a", "--filter", "name="+prefix, "--format", "{{.Names}}")
	out, err := exec.Command(args[0], psArgs...).Output()
	if err != nil {
		return nil, fmt.Errorf("podman ps: %w", err)
	}
	names := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		n := strings.TrimSpace(line)
		if n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

// ProbeContainerRunning checks whether the named agent container is
// actually running after a spawn. See ContainerRuntime.ProbeContainerRunning.
func (r *PodmanRuntime) ProbeContainerRunning(name string) (bool, string, error) {
	full := containerNamePrefix(r.instanceUUID) + name
	args := podmanArgs()
	out, err := exec.Command(args[0], append(args[1:], "inspect", full,
		"--format", "{{.State.Status}}\t{{.State.Error}}")...).Output()
	if err != nil {
		// Container not found in podman's database.
		return false, "", fmt.Errorf("podman inspect %s: %w", full, err)
	}
	line := strings.TrimSpace(string(out))
	parts := strings.SplitN(line, "\t", 2)
	status := strings.TrimSpace(parts[0])
	ociErr := ""
	if len(parts) == 2 {
		ociErr = strings.TrimSpace(parts[1])
	}
	return status == "running", ociErr, nil
}

// ─── Docker ──────────────────────────────────────────────────────────────────

type DockerRuntime struct {
	instanceUUID string // see PodmanRuntime.instanceUUID
}

func (r *DockerRuntime) Name() string                { return "docker" }
func (r *DockerRuntime) SetInstanceUUID(uuid string) { r.instanceUUID = uuid }
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
func (r *DockerRuntime) StopContainer(name string) error {
	full := containerNamePrefix(r.instanceUUID) + name
	// #258 reopen + #272 — normalised `[chepherd-stop]` prefix.
	fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: DockerRuntime.StopContainer enter (full=%s)\n", name, full)
	if stopOut, stopErr := exec.Command("docker", "stop", "--time", "5", full).CombinedOutput(); stopErr != nil {
		s := strings.ToLower(string(stopOut))
		if !strings.Contains(s, "no such container") && !strings.Contains(s, "not found") {
			fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: docker stop FAILED: %v (%s)\n", name, stopErr, strings.TrimSpace(string(stopOut)))
		} else {
			fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: docker stop: already gone\n", name)
		}
	} else {
		fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: docker stop ok\n", name)
	}
	rm := exec.Command("docker", "rm", "-f", full)
	out, err := rm.CombinedOutput()
	if err != nil {
		s := strings.ToLower(string(out))
		if strings.Contains(s, "no such container") || strings.Contains(s, "not found") {
			fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: docker rm -f: already gone (clean)\n", name)
			return nil
		}
		fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: docker rm -f FAILED: %v (%s)\n", name, err, strings.TrimSpace(string(out)))
		return fmt.Errorf("docker rm %s: %w (%s)", full, err, strings.TrimSpace(string(out)))
	}
	fmt.Fprintf(os.Stderr, "[chepherd-stop] %s: docker rm -f ok (%s)\n", name, strings.TrimSpace(string(out)))
	return nil
}
func (r *DockerRuntime) ListAgentContainers() ([]string, error) {
	// #270 — instance-scoped prefix, same rationale as PodmanRuntime.
	prefix := containerNamePrefix(r.instanceUUID)
	out, err := exec.Command("docker", "ps", "-a", "--filter", "name="+prefix, "--format", "{{.Names}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	names := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		n := strings.TrimSpace(line)
		if n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}
func (r *DockerRuntime) SpawnArgs(agentName, agentHomeDir, agentSecretsDir, cwd string, argv []string, env []string) ([]string, []string) {
	// Docker variant — same flags as Podman except no :U mount option
	// (Docker handles UID remapping differently).
	dockerArgs := []string{
		"docker", "run", "--interactive", "--tty", // #363: corpse persists
		"--name", containerNamePrefix(r.instanceUUID) + agentName,
		"--network", agentNetworkMode(),
		"-v", agentHomeDir + ":/home/agent:rw",
		"-v", cwd + ":" + cwd + ":rw",
		"--workdir", cwd,
		"-e", "HOME=/home/agent",
		"-e", "TERM=xterm-256color",
		"-e", "COLORTERM=truecolor",
		"-e", "DISABLE_AUTOUPDATER=1",
		"-e", "COPILOT_AUTO_UPDATE=false", // #copilot self-update EACCES-derails the agent; disable it
		"-e", "COPILOT_ALLOW_ALL=true",    // #copilot folder-trust modal swallows the knock; this skips it (A/B-verified)
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

// ProbeContainerRunning checks whether the named agent container is
// actually running after a spawn. See ContainerRuntime.ProbeContainerRunning.
func (r *DockerRuntime) ProbeContainerRunning(name string) (bool, string, error) {
	full := containerNamePrefix(r.instanceUUID) + name
	out, err := exec.Command("docker", "inspect", full,
		"--format", "{{.State.Status}}\t{{.State.Error}}").Output()
	if err != nil {
		return false, "", fmt.Errorf("docker inspect %s: %w", full, err)
	}
	line := strings.TrimSpace(string(out))
	parts := strings.SplitN(line, "\t", 2)
	status := strings.TrimSpace(parts[0])
	ociErr := ""
	if len(parts) == 2 {
		ociErr = strings.TrimSpace(parts[1])
	}
	return status == "running", ociErr, nil
}

// ─── BareExec ────────────────────────────────────────────────────────────────

// BareExecRuntime runs agents directly on the host — no isolation.
// Used as a fallback when neither Podman nor Docker is available.
// Warns in the UI via the session's "container_runtime" field.
type BareExecRuntime struct{}

func (r *BareExecRuntime) Name() string     { return "bare" }
func (r *BareExecRuntime) Available() error { return nil }
func (r *BareExecRuntime) AgentHomeDir(agentName, stateDir string) (string, error) {
	// On bare exec, use the real host home — no isolation.
	return os.UserHomeDir()
}
func (r *BareExecRuntime) SpawnArgs(agentName, agentHomeDir, agentSecretsDir, cwd string, argv []string, env []string) ([]string, []string) {
	return argv, env
}

// BareExec has no container — Runtime.Stop's PTY close is sufficient.
func (r *BareExecRuntime) StopContainer(name string) error                    { return nil }
func (r *BareExecRuntime) ListAgentContainers() ([]string, error)             { return nil, nil }
func (r *BareExecRuntime) ProbeContainerRunning(string) (bool, string, error) { return true, "", nil }

// #270 — BareExec doesn't manage containers; the UUID is accepted and
// silently ignored to satisfy the interface.
func (r *BareExecRuntime) SetInstanceUUID(string) {}

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
// workspaceMountFlags returns the podman mount flags for the cwd bind.
// Returns ":rw,U" when the source path is under chepherd's managed
// workspaces/ tree (safe to recursive-chown), ":rw" otherwise. The latter
// protects operator-owned host directories where ,U would fail on
// foreign-owned files (e.g. another user's git checkout in the same
// repos parent) and leave the container in Created status forever.
func workspaceMountFlags(hostCwd string) string {
	if strings.Contains(hostCwd, "/.local/state/chepherd/workspaces/") ||
		strings.Contains(hostCwd, "/state/chepherd/workspaces/") {
		return ":rw,U"
	}
	return ":rw"
}

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
//
// Honors the CHEPHERD_CLAUDE_CREDS_PATH env override (#440 CI follow-up) so
// the e2e harness can point at a synthetic credentials file without
// clobbering the operator's real ~/.claude/.credentials.json or hijacking
// HOME (which would break podman's user-mode storage lookup).
func hostClaudeCredentialsPath() string {
	if p := os.Getenv("CHEPHERD_CLAUDE_CREDS_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}
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

// hostOAuthCredsPath resolves an agentOAuthCredsSpec's host fallback path
// to an absolute path on the chepherd host, returning "" if no file exists
// there. claude-code is delegated to hostClaudeCredentialsPath() so its
// CHEPHERD_CLAUDE_CREDS_PATH override + exact semantics are preserved
// byte-for-byte (#440). Other flavors resolve a ~-prefixed path under the
// operator's $HOME and Stat it.
func hostOAuthCredsPath(spec agentOAuthCredsSpec) string {
	if spec.flavor == "claude-code" {
		return hostClaudeCredentialsPath()
	}
	rel := spec.hostFallbackPath
	if rel == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if strings.HasPrefix(rel, "~/") {
		rel = rel[2:]
	} else if rel == "~" {
		rel = ""
	}
	p := filepath.Join(home, rel)
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

// agentNetworkMode returns the podman/docker --network argument used
// when spawning agent containers. Override via CHEPHERD_CONTAINER_NETWORK
// env var for operators who explicitly need per-pod bridge isolation.
//
// Common values:
//   - "container:chepherd" (default; SHARED NETNS — agent reaches MCP at 127.0.0.1:9090)
//   - "chepherd-net"       (user-defined podman network; requires netavark OR CNI plugins)
//   - "slirp4netns:port_handler=slirp4netns" (rootless host-loopback; broken on Podman 3.x)
//   - "host"               (no isolation; simplest)
//   - "bridge"             (per-pod isolation; requires CNI plugin)
//   - "none"               (no network at all)
//
// #414 P0 — shared-netns is the architectural answer that works on ALL
// hosts. Pre-#414 default was "chepherd-net" which requires either
// netavark (Podman 4.x+) OR working CNI plugins (#403/#406/#442/#443
// detection chain). On Podman 3.x hosts where rootless-CNI plumbing
// (rootless-cni-infra helper image, slirp4netns 1.2+, firewall plugin
// config version 1.0.0) is broken or missing, chepherd-net silently
// fell back to slirp4netns which kernel-isolates host loopback →
// agent can't reach chepherd MCP at all → operator-visible -32000.
//
// Shared netns (container:chepherd) makes the agent see chepherd's
// network namespace directly: 127.0.0.1:9090 IS the chepherd MCP
// listener via loopback. No CNI, no netavark, no slirp4netns gateway
// hops, no port forwards. Same primitive used by Kubernetes pod
// sidecars. Works on every podman + docker version with shared-netns
// support (all of them).
//
// Architect verified live 2026-05-31 via doctor --mcp:
//
//	--network=chepherd-net   → broken (rootless CNI plumbing dead)
//	--network=slirp4netns... → TCP unreachable (kernel isolation)
//	--network=container:chepherd → ALL CHECKS PASS
//
// Refs #365 P0 #398 #406 #414.
func agentNetworkMode() string {
	if m := os.Getenv("CHEPHERD_CONTAINER_NETWORK"); m != "" {
		return m
	}
	return "container:chepherd"
}
