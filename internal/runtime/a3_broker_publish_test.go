// internal/runtime/a3_broker_publish_test.go — pins #225 row A3:
// A2ADeliverer.SetBroker installs a publisher; pumpPTYToBroker
// subscribes to PTY output + publishes status/artifact/done events.
//
// Tests use a fakePublisher rather than the real *a2a.StreamBroker
// so we observe exactly what gets called without the SSE plumbing,
// and a real session spawning `echo` to produce one chunk + exit.
//
// Refs #225 row A3.
package runtime

import (
	"sync"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
)

// fakePublisher records every Publish call. Safe for concurrent use.
type fakePublisher struct {
	mu     sync.Mutex
	events []recordedEvent
	done   chan struct{}
}

type recordedEvent struct {
	taskID string
	ev     a2a.StreamEvent
}

func newFakePublisher() *fakePublisher {
	return &fakePublisher{done: make(chan struct{})}
}

func (p *fakePublisher) Publish(taskID string, ev a2a.StreamEvent) int {
	p.mu.Lock()
	p.events = append(p.events, recordedEvent{taskID, ev})
	if ev.Type == "done" {
		select {
		case <-p.done:
		default:
			close(p.done)
		}
	}
	p.mu.Unlock()
	return 1
}

func (p *fakePublisher) Events() []recordedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]recordedEvent, len(p.events))
	copy(out, p.events)
	return out
}

func TestA2ADeliverer_SetBroker_NilDisables(t *testing.T) {
	t.Parallel()
	d := NewA2ADeliverer(&Runtime{})
	d.SetBroker(nil)
	if d.broker != nil {
		t.Error("SetBroker(nil) left non-nil broker")
	}
}

func TestA2ADeliverer_SetBroker_AssignsBroker(t *testing.T) {
	t.Parallel()
	d := NewA2ADeliverer(&Runtime{})
	pub := newFakePublisher()
	d.SetBroker(pub)
	if d.broker == nil {
		t.Error("SetBroker(non-nil) didn't assign")
	}
}

func TestPumpPTYToBroker_PublishesStatusThenDone(t *testing.T) {
	t.Parallel()
	// Spawn `echo` — produces "hello-from-pty\n" on stdout then exits.
	// The pty readLoop sees the chunk, calls fanout to my subscriber,
	// then EOFs + closes the session → my Subscriber.Done closes →
	// pump publishes the terminal `done` event.
	sess, err := session.New("a3-test", session.Spec{
		Command: []string{"echo", "hello-from-pty"},
	})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	task := &a2a.Task{
		ID:        "task-a3-1",
		ContextID: "ctx-a3-1",
		Kind:      "task",
		Status:    a2a.TaskStatus{State: a2a.TaskStateWorking},
	}
	pub := newFakePublisher()

	go pumpPTYToBroker(pub, sess, task)

	select {
	case <-pub.done:
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not publish `done` within 5s")
	}

	events := pub.Events()
	if len(events) < 2 {
		t.Fatalf("pump produced %d events, want at least 2 (status + done)", len(events))
	}
	// First event MUST be a status with state=working.
	if events[0].ev.Type != "status" || events[0].ev.Task == nil ||
		events[0].ev.Task.Status.State != a2a.TaskStateWorking {
		t.Errorf("events[0] = %+v, want status working", events[0])
	}
	// Last event MUST be done with state=completed.
	last := events[len(events)-1]
	if last.ev.Type != "done" || last.ev.Task == nil ||
		last.ev.Task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("events[last] = %+v, want done completed", last)
	}
	// Every event MUST carry the right taskID.
	for i, e := range events {
		if e.taskID != "task-a3-1" {
			t.Errorf("events[%d].taskID = %q, want task-a3-1", i, e.taskID)
		}
	}
}

func TestPumpPTYToBroker_NilArgsNoOp(t *testing.T) {
	t.Parallel()
	pumpPTYToBroker(nil, nil, nil)
	pub := newFakePublisher()
	pumpPTYToBroker(pub, nil, nil)
	if len(pub.Events()) != 0 {
		t.Errorf("nil-session: events = %d, want 0", len(pub.Events()))
	}
}
