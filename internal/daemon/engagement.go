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
package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// EngagementWindow is how recent a user message has to be to count the
// human as actively engaged. 5 minutes covers most "thinking, typing
// next prompt" gaps; anything older and the user has clearly stepped
// away or yielded the floor to the agent.
const EngagementWindow = 5 * time.Minute

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
