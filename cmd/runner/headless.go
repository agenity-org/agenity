// cmd/runner/headless.go implements the v0.9.4 §4 + §5 #49 per-task
// ephemeral runner lifecycle (#499 Wave H1).
//
// chepherd-runner --headless is the iogrid / batch-consumer mode:
// spawn, run ONE task, return an A2A Task envelope, exit. No daemon-
// WS registration, no MCP socket, no per-session A2A endpoint — the
// runner is treated as a single-shot RPC the iogrid HTTP API (Wave
// H2) wraps with HTTP semantics on top.
//
// Task input: --task-json '{"messages":[...]}' or --task-file
// <path> or stdin. Task is a v0.9.4 §16 A2A SendMessage params
// shape (`Message`); the headless runner extracts the user prompt
// text + drives `claude --print --output-format json` against it
// (claude-code flavor today; --agent flag selects others when their
// non-interactive --print equivalents land).
//
// Task output: A2A v1.0 Task envelope written to --result-file or
// stdout. Status.State is COMPLETED on success / FAILED on error.
// history[] carries the input + output Messages.
//
// Exit codes:
//   0 — task completed (Task envelope written, COMPLETED state)
//   2 — task failed (Task envelope written with FAILED state +
//        Status.Message; agent stderr captured into the message)
//   3 — task input malformed (no Task envelope; bare error to
//        stderr — caller can't parse a Task they didn't author)
//
// Refs #499 V0.9.2-ARCHITECTURE.md §4 §5 #49 §16.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/chepherd/chepherd/internal/a2a"
)

// headlessConfig collects flags specific to --headless mode.
type headlessConfig struct {
	enabled      bool
	taskJSON     string
	taskFile     string
	resultFile   string
	agentSlug    string
	timeout      time.Duration
	agentBinPath string // optional override; tests use this to point
	// at a fixture binary instead of the system `claude`.
}

// runHeadless executes the per-task ephemeral lifecycle. Returns the
// shell exit code the caller should pass to os.Exit. Non-nil error
// means we couldn't even produce a Task envelope (bare malformed
// input); caller writes the err string to stderr + exits 3.
func runHeadless(ctx context.Context, hc *headlessConfig) (int, error) {
	msg, err := readHeadlessTask(hc)
	if err != nil {
		return 3, fmt.Errorf("read task input: %w", err)
	}
	if msg.ContextID == "" {
		// Allow callers to omit ContextID — auto-generate one for the
		// ephemeral session so downstream Task records stay traceable.
		msg.ContextID = "headless-" + uuid.NewString()
	}
	taskID := msg.TaskID
	if taskID == "" {
		taskID = uuid.NewString()
	}

	bin := hc.agentBinPath
	if bin == "" {
		bin = "claude"
	}
	prompt, err := a2a.ExtractText(msg)
	if err != nil || prompt == "" {
		envelope := failedTaskEnvelope(taskID, msg, "task message has no text parts")
		writeResult(hc, envelope)
		return 2, nil
	}

	// Run claude --print --output-format json with the prompt as
	// argv. Per the M2 live-walk probes claude returns a single JSON
	// object with {type:"result", stop_reason:"end_turn", result:
	// "<text>", ...}.
	runCtx := ctx
	if hc.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, hc.timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, bin, "--print", "--output-format", "json", prompt)
	stdout, runErr := cmd.Output()
	if runErr != nil {
		stderr := ""
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			stderr = strings.TrimSpace(string(exitErr.Stderr))
		}
		reason := runErr.Error()
		if stderr != "" {
			reason = reason + " — " + stderr
		}
		envelope := failedTaskEnvelope(taskID, msg, "agent process failed: "+reason)
		writeResult(hc, envelope)
		return 2, nil
	}

	// Parse the claude result envelope. We expect type=result and a
	// non-error response. Any other shape → FAILED.
	var agentResult struct {
		Type       string `json:"type"`
		IsError    bool   `json:"is_error"`
		Result     string `json:"result"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(stdout, &agentResult); err != nil {
		envelope := failedTaskEnvelope(taskID, msg, "decode agent result: "+err.Error())
		writeResult(hc, envelope)
		return 2, nil
	}
	if agentResult.Type != "result" || agentResult.IsError {
		envelope := failedTaskEnvelope(taskID, msg,
			fmt.Sprintf("agent returned error envelope: type=%q is_error=%v",
				agentResult.Type, agentResult.IsError))
		writeResult(hc, envelope)
		return 2, nil
	}

	envelope := completedTaskEnvelope(taskID, msg, agentResult.Result)
	writeResult(hc, envelope)
	return 0, nil
}

// readHeadlessTask resolves the inbound A2A Message from the
// configured input source. Precedence: --task-json > --task-file >
// stdin. Empty input → error.
func readHeadlessTask(hc *headlessConfig) (a2a.Message, error) {
	var raw []byte
	switch {
	case hc.taskJSON != "":
		raw = []byte(hc.taskJSON)
	case hc.taskFile != "":
		b, err := os.ReadFile(hc.taskFile)
		if err != nil {
			return a2a.Message{}, fmt.Errorf("read --task-file %q: %w", hc.taskFile, err)
		}
		raw = b
	default:
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return a2a.Message{}, fmt.Errorf("read stdin: %w", err)
		}
		raw = b
	}
	if len(raw) == 0 {
		return a2a.Message{}, errors.New("task input is empty (no --task-json, --task-file, or stdin)")
	}
	var msg a2a.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return a2a.Message{}, fmt.Errorf("parse task JSON: %w", err)
	}
	if len(msg.Parts) == 0 && msg.Role == "" {
		// Allow operators to pass a bare {"prompt":"..."} shape too —
		// translate into a Message with one text Part. Convenience
		// for CLI ad-hoc invocations.
		var compact struct {
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(raw, &compact); err == nil && compact.Prompt != "" {
			msg = a2a.Message{
				Role:  "user",
				Kind:  "message",
				Parts: []a2a.Part{{Kind: "text", Text: compact.Prompt}},
			}
		}
	}
	return msg, nil
}

// completedTaskEnvelope wraps the agent's response into a v0.9.4
// §16 A2A Task in TaskStateCompleted. history[] carries the input
// Message + the agent's output Message so iogrid consumers can
// read the full exchange.
func completedTaskEnvelope(taskID string, in a2a.Message, output string) a2a.Task {
	in.Kind = "message"
	if in.Role == "" {
		in.Role = "user"
	}
	out := a2a.Message{
		Role:  "agent",
		Kind:  "message",
		Parts: []a2a.Part{{Kind: "text", Text: output}},
	}
	return a2a.Task{
		ID:        taskID,
		ContextID: in.ContextID,
		Kind:      "task",
		Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted},
		History:   []a2a.Message{in, out},
	}
}

// failedTaskEnvelope wraps the failure cause into a v0.9.4 §16 A2A
// Task in TaskStateFailed. Status.Message carries the failure
// reason so iogrid consumers can surface it to operators.
func failedTaskEnvelope(taskID string, in a2a.Message, reason string) a2a.Task {
	in.Kind = "message"
	if in.Role == "" {
		in.Role = "user"
	}
	return a2a.Task{
		ID:        taskID,
		ContextID: in.ContextID,
		Kind:      "task",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateFailed,
			Message: &a2a.Message{
				Role:  "agent",
				Kind:  "message",
				Parts: []a2a.Part{{Kind: "text", Text: reason}},
			},
		},
		History: []a2a.Message{in},
	}
}

func writeResult(hc *headlessConfig, envelope a2a.Task) {
	body, _ := json.MarshalIndent(envelope, "", "  ")
	body = append(body, '\n')
	if hc.resultFile != "" {
		if err := os.WriteFile(hc.resultFile, body, 0o600); err == nil {
			return
		}
		fmt.Fprintf(os.Stderr, "warn: write --result-file %q failed, falling back to stdout\n", hc.resultFile)
	}
	_, _ = os.Stdout.Write(body)
}
