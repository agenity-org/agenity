// internal/runtime/a3_broker_publish_test.go — pins #306 (A3) state-
// machine logic via a fake subscriberSource. No OS PTY dependency —
// CI environments without a controlling TTY can't run the real-echo
// variant reliably (caught by #324 CI failure; see _integration_test.go
// for the gated real-PTY exercise).
//
// Refs #306 (A3) #324 (CI fix).
package runtime

import (
	"sync"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/a2a"
	"github.com/agenity-org/agenity/internal/ptyhost/session"
)

// fakePublisher records every Publish call.
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

// fakeSubscriberSource mimics session.Session's Subscribe/Unsubscribe
// signatures + returns a Subscriber the test can drive directly.
type fakeSubscriberSource struct {
	sub *session.Subscriber
}

func newFakeSubscriberSource(bufferDepth int) *fakeSubscriberSource {
	return &fakeSubscriberSource{
		sub: &session.Subscriber{
			Ch:   make(chan []byte, bufferDepth),
			Done: make(chan struct{}),
		},
	}
}

func (s *fakeSubscriberSource) Subscribe(buf int) (*session.Subscriber, []byte, error) {
	return s.sub, nil, nil
}
func (s *fakeSubscriberSource) Unsubscribe(_ *session.Subscriber) {}

func (s *fakeSubscriberSource) PushChunk(b []byte) { s.sub.Ch <- b }
func (s *fakeSubscriberSource) Close() {
	close(s.sub.Done)
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

func TestPumpPTYToBroker_StatusThenChunkThenDone(t *testing.T) {
	t.Parallel()
	src := newFakeSubscriberSource(16)
	task := &a2a.Task{
		ID:        "task-a3",
		ContextID: "ctx-a3",
		Kind:      "task",
		Status:    a2a.TaskStatus{State: a2a.TaskStateWorking},
	}
	pub := newFakePublisher()

	go pumpPTYToBroker(pub, src, task, nil, nil)

	// Give the goroutine a tick to subscribe + publish initial status.
	time.Sleep(20 * time.Millisecond)

	src.PushChunk([]byte("hello-from-pump"))
	time.Sleep(20 * time.Millisecond)
	src.Close()

	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not publish `done` within 2s (deterministic stub)")
	}

	events := pub.Events()
	if len(events) < 3 {
		t.Fatalf("events = %d, want at least 3 (status + artifact + done): %+v", len(events), events)
	}
	if events[0].ev.Type != "status" || events[0].ev.Task == nil ||
		events[0].ev.Task.Status.State != a2a.TaskStateWorking {
		t.Errorf("events[0] = %+v, want status working", events[0])
	}
	// Find the artifact event.
	var sawArtifact bool
	for _, e := range events {
		if e.ev.Type == "artifact" && e.ev.Artifact != nil &&
			len(e.ev.Artifact.Parts) > 0 && e.ev.Artifact.Parts[0].Text == "hello-from-pump" {
			sawArtifact = true
			break
		}
	}
	if !sawArtifact {
		t.Errorf("no artifact event with 'hello-from-pump' chunk: %+v", events)
	}
	// Last event is done completed.
	last := events[len(events)-1]
	if last.ev.Type != "done" || last.ev.Task == nil ||
		last.ev.Task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("events[last] = %+v, want done completed", last)
	}
	// Every event carries the right taskID.
	for i, e := range events {
		if e.taskID != "task-a3" {
			t.Errorf("events[%d].taskID = %q, want task-a3", i, e.taskID)
		}
	}
}

func TestPumpPTYToBroker_ChannelCloseAlsoTriggersDone(t *testing.T) {
	t.Parallel()
	src := newFakeSubscriberSource(8)
	task := &a2a.Task{ID: "t2", ContextID: "c2", Kind: "task"}
	pub := newFakePublisher()
	go pumpPTYToBroker(pub, src, task, nil, nil)
	time.Sleep(20 * time.Millisecond)
	close(src.sub.Ch)
	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not publish `done` when channel closed")
	}
}

func TestPumpPTYToBroker_NilArgsNoOp(t *testing.T) {
	t.Parallel()
	pumpPTYToBroker(nil, nil, nil, nil, nil)
	pub := newFakePublisher()
	pumpPTYToBroker(pub, nil, nil, nil, nil)
	if len(pub.Events()) != 0 {
		t.Errorf("nil-source: events = %d, want 0", len(pub.Events()))
	}
}
