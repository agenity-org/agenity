// internal/runtimehttp/d4_infotomap_pid_test.go — pins #356 P0:
// infoToMap MUST surface pid + started + container_runtime + every
// other JSON-tagged field on SessionInfo. Earlier flattened-handcoded
// version stripped these → dashboard showed 'pid: —' even when the
// spawn succeeded.
//
// Refs #356 P0 #314 (D4).
package runtimehttp

import (
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/runtime"
)

func TestInfoToMap_PreservesPID(t *testing.T) {
	t.Parallel()
	info := &runtime.SessionInfo{
		ID:               "sess-1",
		Name:             "generalist",
		AgentSlug:        "claude-code",
		Team:             "solo",
		Role:             runtime.RoleWorker,
		Cwd:              "/work",
		CreatedAt:        time.Now().UTC(),
		PID:              12345,
		ContainerRuntime: "podman",
		AgentHomeDir:     "/home/agent",
	}
	out := infoToMap(info, true)
	if out["pid"] == nil {
		t.Errorf("infoToMap stripped pid; map = %+v", out)
	}
	if pid, ok := out["pid"].(float64); !ok || int(pid) != 12345 {
		t.Errorf("pid = %v (%T), want 12345", out["pid"], out["pid"])
	}
	if out["container_runtime"] != "podman" {
		t.Errorf("container_runtime = %v, want podman", out["container_runtime"])
	}
	if out["agent_home_dir"] != "/home/agent" {
		t.Errorf("agent_home_dir = %v, want /home/agent", out["agent_home_dir"])
	}
	if out["live"] != true {
		t.Errorf("live = %v, want true", out["live"])
	}
}

func TestInfoToMap_OmitsEmptyPID(t *testing.T) {
	t.Parallel()
	// SessionInfo.PID has json:",omitempty" — PID 0 should be omitted
	// from the marshalled output.
	info := &runtime.SessionInfo{
		ID: "sess-x", Name: "agent-x", AgentSlug: "claude-code",
		CreatedAt: time.Now().UTC(),
		// PID intentionally zero
	}
	out := infoToMap(info, false)
	if _, present := out["pid"]; present {
		t.Errorf("PID 0 not omitted; map has pid = %v", out["pid"])
	}
	if out["live"] != false {
		t.Errorf("live = %v, want false", out["live"])
	}
}
