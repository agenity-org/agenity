package runtime

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// --- KeyringPreflight unit tests ---

func TestKeyringPreflight_ParseKeyUsers_NearLimit(t *testing.T) {
	content := "    0:      4 2/200 36/20000\n 1000:    160 160/200 14432/20000\n"
	res := parseKeyUsers(content, 1000)
	if res == nil {
		t.Fatal("expected result, got nil")
	}
	if res.Used != 160 {
		t.Errorf("used: want 160 got %d", res.Used)
	}
	if res.MaxKeys != 200 {
		t.Errorf("maxKeys: want 200 got %d", res.MaxKeys)
	}
	if !res.Warning {
		t.Error("expected Warning=true at 80% threshold")
	}
	if res.Exceeded {
		t.Error("expected Exceeded=false at 160/200")
	}
}

func TestKeyringPreflight_ParseKeyUsers_Exceeded(t *testing.T) {
	content := "    0:      4 2/200 36/20000\n 1000:    200 200/200 18000/20000\n"
	res := parseKeyUsers(content, 1000)
	if res == nil {
		t.Fatal("expected result, got nil")
	}
	if !res.Exceeded {
		t.Error("expected Exceeded=true at 200/200")
	}
	if !res.Warning {
		t.Error("expected Warning=true when Exceeded")
	}
}

func TestKeyringPreflight_ParseKeyUsers_Healthy(t *testing.T) {
	content := "    0:      4 2/200 36/20000\n 1000:     10 10/200 880/20000\n"
	res := parseKeyUsers(content, 1000)
	if res == nil {
		t.Fatal("expected result, got nil")
	}
	if res.Warning {
		t.Error("expected Warning=false at 5%")
	}
	if res.Exceeded {
		t.Error("expected Exceeded=false at 10/200")
	}
}

func TestKeyringPreflight_ParseKeyUsers_UIDNotFound(t *testing.T) {
	content := "    0:      4 2/200 36/20000\n"
	res := parseKeyUsers(content, 9999)
	if res != nil {
		t.Errorf("expected nil for unknown UID, got %+v", res)
	}
}

func TestKeyringPreflight_ParseKeyUsers_ExactlyAtWarningThreshold(t *testing.T) {
	// 80/100 = exactly 80% — should be Warning=true
	content := "    0:      4 2/200 36/20000\n 2000:     80 80/100 7000/10000\n"
	res := parseKeyUsers(content, 2000)
	if res == nil {
		t.Fatal("expected result, got nil")
	}
	if !res.Warning {
		t.Error("expected Warning=true at exactly 80%")
	}
}

// --- postSpawnContainerCheck unit tests ---

// probeStubRuntime extends fakeContainerRuntime with ProbeContainerRunning.
// We can't embed fakeContainerRuntime directly since it's defined in another
// test file (container_stop_test.go) — use a fresh stub here.
type probeStubRuntime struct {
	running  bool
	ociErr   string
	probeErr error
	probed   chan string
}

func (s *probeStubRuntime) Name() string                  { return "stub" }
func (s *probeStubRuntime) Available() error              { return nil }
func (s *probeStubRuntime) SetInstanceUUID(_ string)      {}
func (s *probeStubRuntime) AgentHomeDir(_, _ string) (string, error) {
	return "/tmp/stub-home", nil
}
func (s *probeStubRuntime) SpawnArgs(_, _, _, _ string, argv []string, env []string) ([]string, []string) {
	return argv, env
}
func (s *probeStubRuntime) StopContainer(_ string) error           { return nil }
func (s *probeStubRuntime) ListAgentContainers() ([]string, error) { return nil, nil }
func (s *probeStubRuntime) ProbeContainerRunning(name string) (bool, string, error) {
	if s.probed != nil {
		s.probed <- name
	}
	return s.running, s.ociErr, s.probeErr
}

func TestPostSpawnContainerCheck_Running_NoInbox(t *testing.T) {
	probed := make(chan string, 1)
	stub := &probeStubRuntime{running: true, probed: probed}
	rt := newTestRuntime(t, stub)

	go rt.postSpawnContainerCheck("alpha", 0)
	select {
	case <-probed:
	case <-time.After(time.Second):
		t.Fatal("ProbeContainerRunning not called within 1s")
	}
	time.Sleep(10 * time.Millisecond)

	rt.mu.Lock()
	inbox := rt.humanInbox
	rt.mu.Unlock()
	if len(inbox) != 0 {
		t.Errorf("expected empty inbox for healthy container, got %d entries", len(inbox))
	}
}

func TestPostSpawnContainerCheck_NotRunning_InboxEntry(t *testing.T) {
	probed := make(chan string, 1)
	stub := &probeStubRuntime{
		running: false,
		ociErr:  "EDQUOT: kernel keyring quota exceeded",
		probed:  probed,
	}
	rt := newTestRuntime(t, stub)

	go rt.postSpawnContainerCheck("beta", 0)
	select {
	case <-probed:
	case <-time.After(time.Second):
		t.Fatal("ProbeContainerRunning not called within 1s")
	}
	time.Sleep(10 * time.Millisecond)

	rt.mu.Lock()
	inbox := rt.humanInbox
	rt.mu.Unlock()
	if len(inbox) != 1 {
		t.Fatalf("expected 1 inbox entry for failed container, got %d", len(inbox))
	}
	if !strings.Contains(inbox[0].Body, "beta") {
		t.Errorf("inbox body should mention agent name 'beta': %q", inbox[0].Body)
	}
	if !strings.Contains(inbox[0].Body, "EDQUOT") {
		t.Errorf("inbox body should include OCI error: %q", inbox[0].Body)
	}
}

func TestPostSpawnContainerCheck_InspectError_NoInbox(t *testing.T) {
	// inspect error = transient; should not fire inbox failure
	probed := make(chan string, 1)
	stub := &probeStubRuntime{
		running:  false,
		probeErr: errors.New("container not found"),
		probed:   probed,
	}
	rt := newTestRuntime(t, stub)

	go rt.postSpawnContainerCheck("gamma", 0)
	select {
	case <-probed:
	case <-time.After(time.Second):
		t.Fatal("ProbeContainerRunning not called within 1s")
	}
	time.Sleep(10 * time.Millisecond)

	rt.mu.Lock()
	inbox := rt.humanInbox
	rt.mu.Unlock()
	if len(inbox) != 0 {
		t.Errorf("expected no inbox entry for inspect error (transient), got %d", len(inbox))
	}
}
