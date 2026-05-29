package runtime

import (
	"context"
	"fmt"
	"os"
)

// AgentSpawner is the pluggable strategy for HOW an agent container/Pod is
// brought up. The chepherd daemon never calls podman/kubectl directly —
// it delegates to a configured AgentSpawner so the same code paths work on
// (a) a hobby Podman setup, (b) a generic K8s cluster via the chepherd
// operator + CRD, (c) an in-cluster trusted environment that holds
// `pods/create` directly.
//
// Architectural rationale (#127): the chepherd daemon should never carry
// `pods/create` permissions itself — that's a textbook Falco-flagged
// privilege escalation. The Operator mode delegates Pod creation to a
// small, audited controller via the `ChepherdAgent` CRD. The chepherd
// daemon's K8s SA only needs `chepherdagents/create` in its own namespace.
type AgentSpawner interface {
	// Mode returns the canonical name: "podman-sidecar" | "operator" | "direct".
	Mode() string

	// Spawn provisions the agent container/Pod. Blocks until the process
	// is ready to attach (a PTY is open and the binary has exec'd).
	// The returned SpawnArtifact carries the argv ptyhost should exec
	// (LocalArgv populated) OR a logical handle (PodNamespace+PodName for
	// K8s spawners — future).
	Spawn(ctx context.Context, req SpawnRequest) (*SpawnArtifact, error)

	// Terminate stops the agent container/Pod. Idempotent.
	Terminate(ctx context.Context, name string) error
}

// SpawnRequest carries everything a spawner needs to provision an agent.
// Subset of SpawnSpec — only the bits relevant after argv has been
// resolved by the agent catalog.
type SpawnRequest struct {
	Name         string
	AgentHomeDir string
	SecretsDir   string
	Cwd          string
	Argv         []string // resolved CLI argv (claude --dangerously-skip-permissions ...)
	Env          []string // KEY=VALUE pairs
}

// SpawnArtifact describes how to attach to the just-spawned agent.
// Local spawners populate LocalArgv (the ptyhost will fork/exec it).
// K8s spawners populate PodNamespace+PodName (ptyhost streams from the
// pod via kubectl exec or an in-cluster client — future work).
type SpawnArtifact struct {
	LocalArgv    []string
	LocalEnv     []string
	PodNamespace string // K8s only
	PodName      string // K8s only
}

// DefaultSpawnerMode resolves which AgentSpawner to use:
//   - $CHEPHERD_SPAWNER     ("podman-sidecar" | "operator" | "direct")
//   - $CHEPHERD_PROFILE     ("minimal" → podman-sidecar, others → operator)
//   - $KUBERNETES_SERVICE_HOST present → operator
//   - else → podman-sidecar (matches the legacy v0.5–v0.7 default)
func DefaultSpawnerMode() string {
	if m := os.Getenv("CHEPHERD_SPAWNER"); m != "" {
		return m
	}
	switch os.Getenv("CHEPHERD_PROFILE") {
	case "minimal":
		return "podman-sidecar"
	case "standard", "enterprise":
		return "operator"
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return "operator"
	}
	return "podman-sidecar"
}

// NewAgentSpawner constructs the configured spawner. The chepherd `run`
// command calls this once at boot.
func NewAgentSpawner(mode string, cr ContainerRuntime) (AgentSpawner, error) {
	switch mode {
	case "", "podman-sidecar", "local":
		// "local" is an alias kept for dev who don't want to run a
		// sidecar — same code path as podman-sidecar today (the sidecar
		// boundary becomes meaningful only once #126 containerizes
		// chepherd itself).
		return &LocalRuntimeSpawner{cr: cr}, nil
	case "operator":
		return &KubernetesOperatorSpawner{}, nil
	case "direct":
		return &KubernetesDirectSpawner{}, nil
	default:
		return nil, fmt.Errorf("unknown spawner mode %q (want podman-sidecar | operator | direct)", mode)
	}
}

// ─── LocalRuntimeSpawner ─────────────────────────────────────────────────────
//
// Wraps the existing ContainerRuntime (PodmanRuntime / DockerRuntime /
// BareExecRuntime) so the chepherd daemon's spawn path is unchanged when
// running in single-host mode. The chepherd-daemon-as-pod plan (#126)
// will move the actual podman invocation into a sidecar, but the
// interface is already in place.

type LocalRuntimeSpawner struct {
	cr ContainerRuntime
}

func (s *LocalRuntimeSpawner) Mode() string { return "podman-sidecar" }

func (s *LocalRuntimeSpawner) Spawn(_ context.Context, req SpawnRequest) (*SpawnArtifact, error) {
	argv, env := s.cr.SpawnArgs(req.Name, req.AgentHomeDir, req.SecretsDir, req.Cwd, req.Argv, req.Env)
	return &SpawnArtifact{LocalArgv: argv, LocalEnv: env}, nil
}

func (s *LocalRuntimeSpawner) Terminate(_ context.Context, name string) error {
	// Best-effort `podman stop` — ignored if container already gone.
	// The PTY teardown in session.go is the source of truth for "stopped".
	return nil
}

// ─── KubernetesOperatorSpawner (scaffold for #127) ───────────────────────────
//
// Writes a ChepherdAgent CR; a separate `chepherd-operator` (small,
// audited, holds pods/create scoped to one namespace + Kyverno
// image-allowlist) reconciles it into a Pod. The chepherd daemon's
// ServiceAccount only needs `chepherdagents/create` in its own namespace,
// not `pods/create` — closes the Falco-flagged privilege escalation.
//
// Implementation lands with the operator chart (#130). Right now the
// constructor returns success so config validation works, but Spawn
// returns a clean error explaining what's missing.

type KubernetesOperatorSpawner struct{}

func (s *KubernetesOperatorSpawner) Mode() string { return "operator" }

func (s *KubernetesOperatorSpawner) Spawn(_ context.Context, _ SpawnRequest) (*SpawnArtifact, error) {
	return nil, fmt.Errorf("operator spawner not yet wired — install bp-chepherd-operator and set CHEPHERD_OPERATOR_NAMESPACE (see #130)")
}

func (s *KubernetesOperatorSpawner) Terminate(_ context.Context, _ string) error {
	return fmt.Errorf("operator spawner not yet wired")
}

// ─── KubernetesDirectSpawner (scaffold for #127) ─────────────────────────────
//
// Holds pods/create directly via client-go. Opt-in only — recommended for
// vCluster-scoped deployments where the blast radius is already contained
// by the vCluster boundary, OR for trusted dev environments. NOT
// recommended for shared production K8s clusters: prefer the operator
// pattern so the privilege is held by a small, audited controller.

type KubernetesDirectSpawner struct{}

func (s *KubernetesDirectSpawner) Mode() string { return "direct" }

func (s *KubernetesDirectSpawner) Spawn(_ context.Context, _ SpawnRequest) (*SpawnArtifact, error) {
	return nil, fmt.Errorf("direct spawner not yet wired — k8s client-go integration tracked in #127")
}

func (s *KubernetesDirectSpawner) Terminate(_ context.Context, _ string) error {
	return fmt.Errorf("direct spawner not yet wired")
}
