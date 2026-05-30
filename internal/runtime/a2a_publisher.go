package runtime

import (
	"bytes"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
)

// pumpSendMark coordinates the byte-offset send-mark between Deliver
// and pumpPTYToBroker (#387 P0).
//
// Lifecycle:
//
//  1. Deliver creates a pumpSendMark
//  2. Deliver spawns pumpPTYToBroker with the mark
//  3. Pump calls sess.Subscribe — closes Subscribed when done
//  4. Deliver waits on <-mark.Subscribed
//  5. Deliver calls sess.Write(message)
//  6. Deliver calls mark.MarkSendNow() — signals SendNow chan
//  7. Pump receives on SendNow → records current responseBuf.Len()
//     as sendOffset; all subsequent silence-finalize slices use that
//     offset to exclude pre-send banner chrome from the captured
//     response
//
// #385 P1's cursor-gate alone wasn't enough: real claude-code banners
// contain ❯ inside the input-box TUI rendering (architect found 5+
// occurrences via grep on podman logs). The cursor gate accepted
// banner chunks as "response complete" because the banner DID
// contain a cursor. The byte-offset boundary is structural:
// everything received before MarkSendNow is banner; everything after
// is response, regardless of how many cursors are in either.
//
// Subscribed nil ⇒ pump doesn't signal subscribe (back-compat for
// tests that spawn pump without a Deliver-driven mark).
// SendNow nil ⇒ pump never receives a mark (back-compat: full buf
// used for the silence gate, matching pre-#387 behavior).
type pumpSendMark struct {
	Subscribed chan struct{}
	SendNow    chan struct{}
	once       sync.Once
}

func newPumpSendMark() *pumpSendMark {
	return &pumpSendMark{
		Subscribed: make(chan struct{}),
		SendNow:    make(chan struct{}),
	}
}

// MarkSendNow signals the pump to record the current responseBuf
// offset as the send boundary. Idempotent.
func (m *pumpSendMark) MarkSendNow() {
	if m == nil {
		return
	}
	m.once.Do(func() { close(m.SendNow) })
}

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

// promptCursorUTF8 is the UTF-8 byte sequence for claude-code's
// prompt cursor `❯` (U+276F HEAVY RIGHT-POINTING ANGLE QUOTATION
// MARK ORNAMENT). #385 P1 uses its presence in the response buffer
// as the gate for silence-finalize — its appearance marks the
// boundary between claude-code's startup chrome (banner +
// permission warning) and steady-state response.
var promptCursorUTF8 = []byte{0xe2, 0x9d, 0xaf}

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
// Refs #306 (A3) #324 (CI fix) #379 P0 (receive-loop completion)
// #385 P1 (cursor gate) #387 P0 (byte-offset send-mark).
//
// completer (#379) is invoked exactly ONCE with the accumulated agent
// response text when the silence window elapses, the channel closes,
// or sub.Done fires. nil disables the receive-loop persistence path
// (back-compat for tests without taskStore wiring).
//
// mark (#387) coordinates the byte-offset send boundary with the
// caller (Deliver). nil ⇒ no marking; pump uses full responseBuf for
// silence-gate + completer (matches pre-#387 behavior for back-compat
// with #379/#385 tests).
func pumpPTYToBroker(broker brokerPublisher, sess subscriberSource, task *a2a.Task, completer func(taskID, response string), mark *pumpSendMark) {
	if sess == nil || task == nil {
		return
	}
	sub, _, err := sess.Subscribe(64)
	if err != nil {
		return
	}
	defer sess.Unsubscribe(sub)

	// #387 P0 — tell the caller we've subscribed. Subsequent
	// sess.Write calls will land on the live channel (not just the
	// pre-subscribe ring snapshot). If mark is nil, this is a no-op.
	if mark != nil {
		close(mark.Subscribed)
	}

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
	//
	// #387 P0 — sendOffset (set when mark.SendNow fires) splits the
	// buffer into "banner" (buf[:sendOffset]) and "response"
	// (buf[sendOffset:]). The silence gate + completer use only the
	// response slice. sendOffset = -1 ⇒ mark never fired; use full
	// buf (back-compat for tests / when caller isn't using marking).
	var responseBuf bytes.Buffer
	sendOffset := -1
	responseSlice := func() []byte {
		if sendOffset < 0 || sendOffset > responseBuf.Len() {
			return responseBuf.Bytes()
		}
		return responseBuf.Bytes()[sendOffset:]
	}
	finalize := func() {
		slice := responseSlice()
		if completer != nil && len(slice) > 0 {
			completer(task.ID, string(slice))
		}
		if broker != nil {
			broker.Publish(task.ID, doneEvent(task))
		}
	}

	// #387 P0 — channel-or-nil pattern: when mark is wired, sendNowCh
	// is the real chan and a receive arms sendOffset; when mark is
	// nil, sendNowCh stays nil so the select branch never fires
	// (selecting from nil chan blocks forever, which is what we want).
	var sendNowCh <-chan struct{}
	if mark != nil {
		sendNowCh = mark.SendNow
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
		case <-sendNowCh:
			// #387 P0 — Deliver wrote the message. Record the boundary
			// so subsequent silence-finalize only considers post-send
			// bytes. Disable the case after firing (nil chan blocks
			// forever in select).
			sendOffset = responseBuf.Len()
			sendNowCh = nil
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
			//
			// #385 P1 — gate silence-finalize on having observed
			// claude-code's prompt cursor (❯, UTF-8 e2 9d af).
			//
			// #387 P0 — apply the gate to the POST-SEND slice only
			// (buf[sendOffset:]). Pre-#387 the gate ran against the
			// full buffer; real claude-code banners contain ❯ inside
			// the TUI input-box rendering (architect found 5+
			// occurrences via grep on podman logs), so the gate
			// passed during banner-paint silence and the first-
			// message-after-spawn completer captured banner chrome
			// instead of the reply. Slicing at sendOffset is
			// structural: everything before the mark is banner;
			// everything after is response.
			//
			// sub.Done + channel-close paths intentionally bypass
			// this gate so fast-exiting agents finalize cleanly.
			if !bytes.Contains(responseSlice(), promptCursorUTF8) {
				silenceTimer.Reset(silence)
				continue
			}
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
