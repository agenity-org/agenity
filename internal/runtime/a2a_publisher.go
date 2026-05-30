package runtime

import (
	"sync"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
)

// brokerPublisher is the minimal seam between A2ADeliverer and a2a's
// StreamBroker — the runtime package can't import a2a's StreamBroker
// type directly without risking a cyclic dep, so we adapt via an
// interface that any *a2a.StreamBroker satisfies.
//
// Refs #225 row A3.
type brokerPublisher interface {
	Publish(taskID string, ev a2a.StreamEvent) int
}

// SetBroker wires the streaming broker so Deliver fires PTY output
// events through it. nil disables publishing (back-compat — A2A
// without broker still works; SSE just won't see events for tasks
// without an A2ADeliverer-wired publisher).
//
// Refs #225 row A3.
func (d *A2ADeliverer) SetBroker(b brokerPublisher) {
	d.broker = b
}

// pumpPTYToBroker subscribes to the session's PTY output and
// publishes each chunk to the broker as an `artifact` StreamEvent
// scoped to the task. Publishes an initial `status` event with
// state=working. On Subscriber.Done it publishes a final `done`
// event with state=completed so the broker GCs the subscription
// + the SSE handler can close the connection.
//
// Runs as a goroutine spawned from A2ADeliverer.Deliver. Bounded
// by the session's lifetime — when the session closes the
// Subscriber.Done channel closes and this loop returns.
//
// Refs #225 row A3.
func pumpPTYToBroker(broker brokerPublisher, sess *session.Session, task *a2a.Task) {
	if broker == nil || sess == nil || task == nil {
		return
	}
	sub, _, err := sess.Subscribe(64)
	if err != nil {
		return
	}
	defer sess.Unsubscribe(sub)

	// Initial status event so any SSE subscriber waiting on this
	// task sees the working state immediately, without needing to
	// wait for the first PTY chunk.
	broker.Publish(task.ID, a2a.StreamEvent{
		Type: "status",
		Task: &a2a.Task{
			ID:        task.ID,
			ContextID: task.ContextID,
			Kind:      "task",
			Status:    a2a.TaskStatus{State: a2a.TaskStateWorking},
		},
	})

	for {
		select {
		case chunk, ok := <-sub.Ch:
			if !ok {
				broker.Publish(task.ID, doneEvent(task))
				return
			}
			broker.Publish(task.ID, a2a.StreamEvent{
				Type: "artifact",
				Artifact: &a2a.Artifact{
					ArtifactID: task.ID + "-stream",
					Parts: []a2a.Part{
						{Kind: "text", Text: string(chunk)},
					},
				},
			})
		case <-sub.Done:
			broker.Publish(task.ID, doneEvent(task))
			return
		}
	}
}

// doneEvent builds the terminal stream event. Uses state=completed —
// any failure path before broker hookup already returned a `failed`
// Task to the JSON-RPC caller, so the broker-side stream is only
// alive on the success path.
func doneEvent(task *a2a.Task) a2a.StreamEvent {
	return a2a.StreamEvent{
		Type: "done",
		Task: &a2a.Task{
			ID:        task.ID,
			ContextID: task.ContextID,
			Kind:      "task",
			Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted},
		},
	}
}

// publisherSetGuard is reserved for future use when multiple
// concurrent SetBroker calls need to be safely serialized. The
// lock is held by SetBroker and read by Deliver. Today A2ADeliverer
// is constructed once at boot and SetBroker is called once before
// the first Deliver — so the field is effectively immutable post-
// boot — but the var leaves a seam for hot-rewire if needed.
var publisherSetGuard sync.Mutex
