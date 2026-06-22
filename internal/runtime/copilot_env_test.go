// internal/runtime/copilot_env_test.go — pins the two copilot startup-gate
// env vars that the daemon MUST inject into every agent container spawn.
//
// Both were operator-observed 2026-06-20 as the cause of qa/copilot connecting
// its MCP client but never calling get_task (the [chepherd-knock] marker was
// swallowed before it reached the model):
//
//   COPILOT_AUTO_UPDATE=false — without it, @github/copilot self-updates its
//   linux-x64 binary on launch, hits EACCES on the root-owned
//   /usr/lib/node_modules, and derails the agent.
//
//   COPILOT_ALLOW_ALL=true — without it, the CLI renders a blocking
//   "Confirm folder trust" modal on first launch in a not-yet-trusted folder.
//   That modal only listens for ↑/↓/Enter/Esc/1/2/3, so the injected knock
//   marker + CR is consumed by the modal and the knock never becomes a prompt.
//   The bundle's trust gate is `process.env.COPILOT_ALLOW_ALL === "true" ||
//   <folder in trustedFolders>`; the env var skips the modal (A/B-verified
//   against the real /usr/local/bin/copilot binary).
//
// These are unconditional (set for every flavor): the vars are copilot-specific
// and inert for the other CLIs, and gating them on slug would add a code path
// that the spawn hot loop does not otherwise need.
package runtime

import (
	"strings"
	"testing"
)

// hasEnvKV reports whether argv contains the consecutive pair ["-e", "KEY=VAL"]
// (the podman/docker form) anywhere in the slice.
func hasEnvKV(argv []string, kv string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-e" && argv[i+1] == kv {
			return true
		}
	}
	return false
}

func TestCopilotStartupGateEnv_Podman(t *testing.T) {
	r := &PodmanRuntime{}
	argv, _ := r.SpawnArgs("agent-copilot", "/tmp/home", "/tmp/secrets", "/tmp/cwd",
		[]string{"copilot"}, nil)
	for _, kv := range []string{"COPILOT_AUTO_UPDATE=false", "COPILOT_ALLOW_ALL=true"} {
		if !hasEnvKV(argv, kv) {
			t.Errorf("podman spawn argv missing -e %s\nargv: %s", kv, strings.Join(argv, " "))
		}
	}
}

func TestCopilotStartupGateEnv_Docker(t *testing.T) {
	r := &DockerRuntime{}
	argv, _ := r.SpawnArgs("agent-copilot", "/tmp/home", "/tmp/secrets", "/tmp/cwd",
		[]string{"copilot"}, nil)
	for _, kv := range []string{"COPILOT_AUTO_UPDATE=false", "COPILOT_ALLOW_ALL=true"} {
		if !hasEnvKV(argv, kv) {
			t.Errorf("docker spawn argv missing -e %s\nargv: %s", kv, strings.Join(argv, " "))
		}
	}
}
