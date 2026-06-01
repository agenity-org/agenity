// p0_593_login_submit_container_name_test.go pins #593: claudeLoginSubmit
// must derive the container name using the instance UUID so podman exec
// resolves the correct container when multiple chepherd instances share a host.
package runtimehttp

import "testing"

func TestAgentContainerName_WithUUID(t *testing.T) {
	got := agentContainerName("abc12345", "alice")
	want := "chepherd-agent-abc12345-alice"
	if got != want {
		t.Errorf("agentContainerName(uuid,name) = %q, want %q", got, want)
	}
}

func TestAgentContainerName_WithoutUUID(t *testing.T) {
	got := agentContainerName("", "alice")
	want := "chepherd-agent-alice"
	if got != want {
		t.Errorf("agentContainerName(\"\",name) = %q, want %q", got, want)
	}
}

func TestAgentContainerName_UUIDNotDoubled(t *testing.T) {
	// Guard against the pre-#593 bug: UUID must appear exactly once.
	got := agentContainerName("deadbeef", "bob")
	want := "chepherd-agent-deadbeef-bob"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Pre-bug form would have been "chepherd-agent-bob" (UUID dropped).
	old := "chepherd-agent-" + "bob"
	if got == old {
		t.Errorf("UUID was dropped — reverted to pre-#593 form %q", old)
	}
}
