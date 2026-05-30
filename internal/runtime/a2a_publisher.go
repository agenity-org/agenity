package runtime

import (
	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
)

// brokerPublisher is the minimal seam between A2ADeliverer and a2a's
// StreamBroker — the runtime package can't import a2a's StreamBroker
// type directly without risking a cyclic dep, so we adapt via an
// interface that any *a2a.StreamBroker satisfies.
//
// Refs #306 (A3) #225.
type brokerPublisher interface {
	Publish(taskID string, ev a2a.StreamEvent) int
}

// subscriberSource abstracts session.Session for testing. Production
// callers pass *session.Session (which satisfies this interface
// naturally). Tests pass a fakeSubscriberSource that drives chunks
// + done deterministically without OS PTY semantics — necessary
// because CI environments without a controlling TTY break the
// real-echo path (#324 follow-up).
//
// Refs #306 (A3 CI fix) #324.
type subscriberSource interface {
	Subscribe(buf int) (*session.Subscriber, []byte, error)
	Unsubscribe(sub *session.Subscriber)
}

// SetBroker wires the streaming broker so Deliver fires PTY output
// events through it. nil disables publishing (back-compat).
func (d *A2ADeliverer) SetBroker(b brokerPublisher) {
	d.broker = b
}

// pumpPTYToBroker subscribes to the session's PTY output and
// publishes status / artifact / done StreamEvents to the broker.
// Runs as a goroutine spawned from A2ADeliverer.Deliver; bounded
// by the session's lifetime.
//
// The `sess` parameter is a subscriberSource — interface-typed so
// tests can stub it. *session.Session satisfies the interface
// without changes.
//
// Refs #306 (A3) #324 (CI fix).
func pumpPTYToBroker(broker brokerPublisher, sess subscriberSource, task *a2a.Task) {
	if broker == nil || sess == nil || task == nil {
		return
	}
	sub, _, err := sess.Subscribe(64)
	if err != nil {
		return
	}
	defer sess.Unsubscribe(sub)

	// Initial status — SSE subscribers see the working state immediately.
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

// doneEvent builds the terminal stream event. Uses state=completed.
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
