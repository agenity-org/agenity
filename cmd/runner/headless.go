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
	"github.com/chepherd/chepherd/internal/runtime/agentpatterns"
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

	// credentialsFile (#501 Wave H3) is the path to a 0600 JSON file
	// containing an array of credential records. Read once at
	// runHeadless start, applied to the CHILD agent process's env,
	// then the file is deleted. The key value NEVER appears on
	// the runner's command line (process args are world-readable
	// via /proc/<pid>/cmdline) — credentials flow via file path
	// only.
	credentialsFile string
}

// credential is one BYO-key record per provider.
type credential struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
}

// providerToEnvVar maps a credential provider to the env var the
// agent binary reads. FLAVOR-aware so a wrong provider name fails
// closed (the env var isn't set, the agent reports "missing API
// key" — better than silently propagating the wrong key under a
// wrong name).
var providerToEnvVar = map[string]string{
	"anthropic": "ANTHROPIC_API_KEY",
	"openai":    "OPENAI_API_KEY",
	"google":    "GEMINI_API_KEY",
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
	// #501 Wave H3 — inject BYO credentials into the CHILD's env
	// ONLY. Never exported to the runner's own env so other
	// (future / sibling) agents in the same runner process can't
	// see them, and `/proc/<runner-pid>/environ` doesn't leak the
	// values. The credentials file is deleted immediately after
	// read so a second observer (next sweep) sees nothing.
	childEnv, credSummary, credErr := buildChildEnvWithCredentials(hc.credentialsFile)
	if credErr != nil {
		envelope := failedTaskEnvelope(taskID, msg, "credentials: "+credErr.Error())
		writeResult(hc, envelope)
		return 2, nil
	}
	if len(childEnv) > 0 {
		cmd.Env = childEnv
		// Log the SUMMARY (provider names + scope only) — never
		// the key values. Audit hygiene at construction time per
		// the H3 dispatch's "redaction at the audit-event-
		// construction site, not at the log-line site" defense-
		// in-depth requirement.
		fmt.Fprintf(os.Stderr, "[chepherd-runner] credentials injected: %s\n", credSummary)
	}
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

	// #503 Wave H5 — detect AUTH_REQUIRED from agent output. The
	// detector runs on the full stdout (which includes the JSON
	// envelope's `result` prose) so both stream-json structured
	// markers AND headless prose patterns are covered.
	slug := hc.agentSlug
	if slug == "" {
		slug = "claude-code"
	}
	flavor := agentpatterns.ByAgentSlug(slug)
	if flavor.IsAuthRequired(stdout).Match {
		envelope := authRequiredTaskEnvelope(taskID, msg, agentResult.Result,
			flavor.ExtractAuthChallenge(stdout))
		writeResult(hc, envelope)
		return 4, nil
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

// authRequiredTaskEnvelope wraps the agent's auth-required response
// into a v0.9.4 §16 A2A Task in TaskStateAuthRequired. Status.Details
// carries the per-flavor AuthChallenge so iogrid consumers can
// surface the operator prompt without re-parsing agent bytes.
// (#503 Wave H5 / §15.3)
func authRequiredTaskEnvelope(taskID string, in a2a.Message, output string,
	ch *agentpatterns.AuthChallenge) a2a.Task {
	in.Kind = "message"
	if in.Role == "" {
		in.Role = "user"
	}
	out := a2a.Message{
		Role:  "agent",
		Kind:  "message",
		Parts: []a2a.Part{{Kind: "text", Text: output}},
	}
	status := a2a.TaskStatus{State: a2a.TaskStateAuthRequired}
	if ch != nil {
		status.Details = &a2a.TaskStatusDetails{
			AuthProvider: ch.Provider,
			AuthMessage:  ch.Message,
			AuthURL:      ch.URL,
		}
	}
	return a2a.Task{
		ID:        taskID,
		ContextID: in.ContextID,
		Kind:      "task",
		Status:    status,
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

// buildChildEnvWithCredentials reads the credentials file, deletes
// it, and returns the env slice the child agent process should run
// with — host env minus existing provider keys (so a host-level
// ANTHROPIC_API_KEY doesn't shadow the customer's BYO key) plus
// the injected per-task keys. Returns a human-readable summary
// (provider list, NEVER values) for audit logging.
//
// When credentialsFile is empty, returns (nil, "", nil) — the
// child inherits the runner's env as before (back-compat).
func buildChildEnvWithCredentials(credentialsFile string) ([]string, string, error) {
	if credentialsFile == "" {
		return nil, "", nil
	}
	raw, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, "", fmt.Errorf("read %q: %w", credentialsFile, err)
	}
	// Delete the file immediately so any later observer (sibling
	// process, ps-style scan of open fds) sees nothing.
	_ = os.Remove(credentialsFile)
	if len(raw) == 0 {
		return nil, "", nil
	}
	var creds []credential
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, "", fmt.Errorf("parse credentials JSON: %w", err)
	}
	if len(creds) == 0 {
		return nil, "", nil
	}
	// Start with the host env stripped of any pre-set provider
	// keys so the BYO values aren't shadowed.
	envSet := map[string]string{}
	for _, e := range os.Environ() {
		k, v, found := strings.Cut(e, "=")
		if !found {
			continue
		}
		envSet[k] = v
	}
	var providers []string
	for _, c := range creds {
		envVar, ok := providerToEnvVar[c.Provider]
		if !ok {
			return nil, "", fmt.Errorf("unsupported provider %q", c.Provider)
		}
		if c.Key == "" {
			return nil, "", fmt.Errorf("provider %q: empty key", c.Provider)
		}
		envSet[envVar] = c.Key
		providers = append(providers, c.Provider)
	}
	// Re-flatten the map into an env slice.
	out := make([]string, 0, len(envSet))
	for k, v := range envSet {
		out = append(out, k+"="+v)
	}
	return out, "providers=[" + strings.Join(providers, ",") + "]", nil
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
