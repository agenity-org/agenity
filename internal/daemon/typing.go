package daemon

import (
	"os/exec"
	"strings"
	"time"
)

// TypingDetectWindow is how long we wait between the two capture-pane
// snapshots used to detect active keystrokes. 800ms is long enough to
// catch a steady typer (most humans average 200-400ms between chars)
// without adding noticeable latency to the daemon tick loop.
const TypingDetectWindow = 800 * time.Millisecond

// TypingDetectLines is how many lines from the bottom of the pane we
// compare. Claude Code's input area is the last 2-4 lines (prompt +
// possibly multi-line entry); 4 covers wrapped input + the prompt frame.
const TypingDetectLines = 4

// IsUserTyping returns true when the bottom of the tmux pane's visible
// region changes between two captures taken TypingDetectWindow apart.
// Used by the daemon to defer injection while the operator is actively
// typing — interrupting a half-written prompt with a SUPERVISOR message
// is jarring and clobbers the user's input.
//
// Returns false on any tmux failure (fail-open: if we can't tell,
// proceed with normal injection rather than block all coaching).
func IsUserTyping(tmuxName string) bool {
	first := captureBottom(tmuxName, TypingDetectLines)
	if first == "" {
		return false
	}
	time.Sleep(TypingDetectWindow)
	second := captureBottom(tmuxName, TypingDetectLines)
	if second == "" {
		return false
	}
	return first != second
}

// captureBottom returns the last N lines of the named pane, joined.
// Empty string on error.
func captureBottom(tmuxName string, lines int) string {
	out, err := exec.Command("tmux", "capture-pane", "-t", tmuxName, "-p").Output()
	if err != nil {
		return ""
	}
	body := string(out)
	// Split into lines and keep the trailing N non-empty ones. Pane
	// captures often have empty trailing rows; trim those first.
	all := strings.Split(strings.TrimRight(body, "\n"), "\n")
	// Drop empty trailing lines so the tail comparison focuses on the
	// actual input area, not the blank rows below it.
	for len(all) > 0 && strings.TrimSpace(all[len(all)-1]) == "" {
		all = all[:len(all)-1]
	}
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}
