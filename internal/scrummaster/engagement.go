// Package daemon — engagement.go decides whether a human is actively
// driving a session. Used by the tick loop to defer injection while the
// operator is in conversation: pushing a SUPERVISOR message mid-dialogue
// is jarring and clobbers the user's input.
//
// Signal hierarchy (cheap → expensive):
//
//  1. JSONL last-user-message recency — read the session's JSONL, find
//     the most recent USER message (not tool_result), check if it's within
//     EngagementWindow. This is fast (one file read, small tail), zero
//     blocking sleep, and directly answers "was the human just here".
//
//  2. Active typing (typing.go) — only used as a narrow backstop when
//     JSONL is unavailable. Sleeps 800ms.
//
// Returns true = "human engaged, don't intervene now".
package scrummaster

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// EngagementWindow is how recent a user message has to be to count the
// human as actively engaged. 15 minutes is the conservative side of
// "still in this conversation" — short enough that a truly walked-away
// user isn't blocking coaching forever; long enough that the daemon
// doesn't slip an injection between user prompts during normal think-
// type-reply pacing.
const EngagementWindow = 15 * time.Minute

// InterruptionEvidenceWindow scans the recent JSONL for past-injection
// contamination — user messages that contain "[SUPERVISOR" substring,
// meaning the daemon previously injected into a pending-typing buffer
// and got concatenated into the eventual committed user prompt. When
// such evidence exists within this window, the daemon backs off
// HARDER (no injection for InterruptionDeferWindow).
const InterruptionEvidenceWindow = 60 * time.Minute

// InterruptionDeferWindow is how long we hold off injection after
// detecting evidence of a prior typing-interruption. Twice the normal
// engagement window — gives the user a real chance to finish their
// thread without further interference.
const InterruptionDeferWindow = 30 * time.Minute

// MinInjectInterval is the hard minimum between coach injections for a
// single session, regardless of judge cadence. Prevents back-to-back
// SUPERVISOR pile-ups even when JSONL signals don't gate.
const MinInjectInterval = 10 * time.Minute

// HasRecentInterruptionEvidence scans the recent JSONL for user messages
// containing "[SUPERVISOR" — a fingerprint of past injections that got
// concatenated into pending-typing input. If true, a previous inject
// interrupted the operator; back off for InterruptionDeferWindow.
func HasRecentInterruptionEvidence(jsonlPath string) bool {
	if jsonlPath == "" {
		return false
	}
	f, err := os.Open(jsonlPath)
	if err != nil {
		return false
	}
	defer f.Close()
	cutoff := time.Now().Add(-InterruptionEvidenceWindow)
	const tailBytes = 1024 * 1024
	st, err := f.Stat()
	if err != nil {
		return false
	}
	size := st.Size()
	startAt := int64(0)
	if size > tailBytes {
		startAt = size - tailBytes
	}
	if _, err := f.Seek(startAt, 0); err != nil {
		return false
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytesContains(line, []byte(`"type":"user"`)) {
			continue
		}
		if !bytesContains(line, []byte(`[SUPERVISOR`)) && !bytesContains(line, []byte(`SUPERVISOR — `)) {
			continue
		}
		var event struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Type != "user" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil {
			continue
		}
		if t.After(cutoff) {
			return true
		}
	}
	return false
}

// IsHumanEngaged reports whether the most recent USER-typed message in
// the session's JSONL transcript happened within EngagementWindow.
//
// Distinguishes user-typed messages from tool-results (which also have
// type=user in the claude-code JSONL): user-typed messages have
// message.content as a string OR an array with type=text. tool-results
// have message.content as an array with type=tool_result.
//
// Returns false on any I/O error (fail-open: don't gate injection on
// a broken transcript reader).
func IsHumanEngaged(jsonlPath string) bool {
	if jsonlPath == "" {
		return false
	}
	f, err := os.Open(jsonlPath)
	if err != nil {
		return false
	}
	defer f.Close()

	cutoff := time.Now().Add(-EngagementWindow)
	// Walk the file backward in chunks; cheaper than scanning forward
	// from start. For typical JSONL sizes (<50 MiB), a backward bufio
	// scanner over the last 256 KiB catches anything within
	// EngagementWindow in practice.
	const tailBytes = 256 * 1024
	st, err := f.Stat()
	if err != nil {
		return false
	}
	size := st.Size()
	startAt := int64(0)
	if size > tailBytes {
		startAt = size - tailBytes
	}
	if _, err := f.Seek(startAt, 0); err != nil {
		return false
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	var latestUserAt time.Time
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) < 8 {
			continue
		}
		// Cheap pre-filter: skip lines that don't contain "type":"user"
		// before doing the full JSON decode.
		if !bytesContains(line, []byte(`"type":"user"`)) {
			continue
		}
		var event struct {
			Type      string          `json:"type"`
			Timestamp string          `json:"timestamp"`
			Message   json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Type != "user" {
			continue
		}
		if !isUserTypedMessage(event.Message) {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil {
			continue
		}
		if t.After(latestUserAt) {
			latestUserAt = t
		}
	}
	if latestUserAt.IsZero() {
		return false
	}
	return latestUserAt.After(cutoff)
}

// isUserTypedMessage distinguishes a real user prompt from a tool_result
// envelope (both have type=user in the JSONL).
//
// Real user prompts:    message.content is a STRING, or array with type=text
// Tool results:         message.content is an array with type=tool_result
func isUserTypedMessage(rawMessage []byte) bool {
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(rawMessage, &msg); err != nil {
		return false
	}
	if msg.Role != "user" {
		return false
	}
	// String content = user typed it directly.
	if len(msg.Content) > 0 && msg.Content[0] == '"' {
		return true
	}
	// Array content: inspect each entry for a type=text part (real user
	// input) vs type=tool_result (machine-driven).
	var parts []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg.Content, &parts); err != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "text" {
			return true
		}
	}
	return false
}

// bytesContains is strings.Contains for []byte without importing the
// strings package twice.
func bytesContains(haystack, needle []byte) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return len(needle) == 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
