package runtime

import (
	"bytes"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
)

// ansiEscapeRE matches CSI sequences (ESC [ ... letter) + OSC sequences
// (ESC ] ... BEL/ST) + the bare ESC + simple two-byte sequences. Good
// enough for stripping claude-code's PTY ANSI chrome before persisting
// the agent's response to taskStore.
var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*(\x07|\x1b\\)|\x1b[()][AB012]|\x1b[<=>]|\x1b\([AB0-2]|\x1b[78]`)

// stripANSI removes ANSI escape sequences from s. Used to clean the
// agent's PTY response before persisting it as a Message text part.
//
// #379 P0 receive-loop fix.
func stripANSI(s string) string {
	return ansiEscapeRE.ReplaceAllString(s, "")
}

// silenceWindow is the period of no PTY output that triggers the
// "response complete" signal in pumpPTYToBroker. Configurable via
// CHEPHERD_A2A_SILENCE_WINDOW_MS env var (default 1500ms). Shorter
// values risk firing mid-response on natural pauses; longer values
// delay the task-completed transition.
//
// #379 P0 receive-loop fix.
func silenceWindow() time.Duration {
	if v := os.Getenv("CHEPHERD_A2A_SILENCE_WINDOW_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 1500 * time.Millisecond
}

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
// Refs #306 (A3) #324 (CI fix) #379 P0 (receive-loop completion).
//
// completer (#379) is invoked exactly ONCE with the accumulated agent
// response text when the silence window elapses, the channel closes,
// or sub.Done fires. nil disables the receive-loop persistence path
// (back-compat for tests without taskStore wiring).
func pumpPTYToBroker(broker brokerPublisher, sess subscriberSource, task *a2a.Task, completer func(taskID, response string)) {
	if sess == nil || task == nil {
		return
	}
	sub, _, err := sess.Subscribe(64)
	if err != nil {
		return
	}
	defer sess.Unsubscribe(sub)

	// Initial status — SSE subscribers see the working state immediately.
	if broker != nil {
		broker.Publish(task.ID, a2a.StreamEvent{
			Type: "status",
			Task: &a2a.Task{
				ID:        task.ID,
				ContextID: task.ContextID,
				Kind:      "task",
				Status:    a2a.TaskStatus{State: a2a.TaskStateWorking},
			},
		})
	}

	// #379 P0 — accumulate the agent's PTY output so we can persist
	// it as a Message{role:"agent"} into the Task's history once the
	// response is complete. silence-window heuristic: response is
	// "complete" after CHEPHERD_A2A_SILENCE_WINDOW_MS (default 1500ms)
	// of no new PTY output. Channel-close or sub.Done also finalises.
	var responseBuf bytes.Buffer
	finalize := func() {
		if completer != nil && responseBuf.Len() > 0 {
			completer(task.ID, responseBuf.String())
		}
		if broker != nil {
			broker.Publish(task.ID, doneEvent(task))
		}
	}

	silence := silenceWindow()
	silenceTimer := time.NewTimer(silence)
	defer silenceTimer.Stop()
	// Start with timer drained — we only arm it after first chunk so
	// a task that produces zero output doesn't auto-complete.
	if !silenceTimer.Stop() {
		<-silenceTimer.C
	}
	timerArmed := false

	for {
		select {
		case chunk, ok := <-sub.Ch:
			if !ok {
				finalize()
				return
			}
			responseBuf.Write(chunk)
			if broker != nil {
				broker.Publish(task.ID, a2a.StreamEvent{
					Type: "artifact",
					Artifact: &a2a.Artifact{
						ArtifactID: task.ID + "-stream",
						Parts: []a2a.Part{
							{Kind: "text", Text: string(chunk)},
						},
					},
				})
			}
			// Reset silence timer — keep waiting for more chunks.
			if timerArmed && !silenceTimer.Stop() {
				select {
				case <-silenceTimer.C:
				default:
				}
			}
			silenceTimer.Reset(silence)
			timerArmed = true
		case <-silenceTimer.C:
			// #379 P0 — silence window elapsed → response complete.
			// Fire the completer (persists agent response + flips
			// state) and the broker's done event, then exit.
			finalize()
			return
		case <-sub.Done:
			finalize()
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
