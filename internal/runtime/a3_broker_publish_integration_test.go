//go:build integration

// internal/runtime/a3_broker_publish_integration_test.go — real-PTY
// exercise of pumpPTYToBroker. Gated behind `-tags integration` so
// CI environments without a controlling TTY don't run it. Catches
// regressions where the OS PTY semantics drift, which the unit test
// can't.
//
// Run locally: `go test -tags integration -race -run TestPumpPTYToBroker_RealEcho ./internal/runtime/...`
//
// Refs #306 (A3) #324 (CI fix).
package runtime

import (
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/a2a"
	"github.com/agenity-org/agenity/internal/ptyhost/session"
)

func TestPumpPTYToBroker_RealEcho(t *testing.T) {
	t.Parallel()
	sess, err := session.New("a3-integration", session.Spec{
		Command: []string{"echo", "hello-from-pty"},
	})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	task := &a2a.Task{
		ID: "task-a3-integration", ContextID: "ctx-a3", Kind: "task",
		Status: a2a.TaskStatus{State: a2a.TaskStateWorking},
	}
	pub := newFakePublisher()
	go pumpPTYToBroker(pub, sess, task, nil, nil)

	select {
	case <-pub.done:
	case <-time.After(10 * time.Second):
		t.Fatal("real-PTY pump did not publish `done` within 10s")
	}
	events := pub.Events()
	if len(events) < 2 {
		t.Fatalf("events = %d, want at least 2", len(events))
	}
	if events[0].ev.Type != "status" {
		t.Errorf("events[0].Type = %q, want status", events[0].ev.Type)
	}
	if events[len(events)-1].ev.Type != "done" {
		t.Errorf("events[last].Type = %q, want done", events[len(events)-1].ev.Type)
	}
}
