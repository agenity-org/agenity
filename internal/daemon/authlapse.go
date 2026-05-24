package daemon

import (
	"os/exec"
	"strings"
)

// AuthLapsePatterns are substrings that, when present in a tmux pane's
// recent output, indicate the Claude session can't reach the Claude
// API — usually because the operator's auth token expired. Founder
// sees: "Please run /login · API Error: 403 The socket connection was
// closed unexpectedly." on the live pane.
//
// Conservative list — only patterns Claude Code itself emits, not
// generic 403s that might come from a user's own tool calls.
var AuthLapsePatterns = []string{
	"Please run /login",
	"API Error: 403",
	"socket connection was closed unexpectedly",
}

// CheckAuthLapsed runs `tmux capture-pane -t <tmuxName> -p` (last screen
// only — fast) and returns true if any AuthLapsePattern is present. False
// on any error (so a transient tmux glitch never falsely flags a healthy
// session).
func CheckAuthLapsed(tmuxName string) bool {
	if tmuxName == "" {
		return false
	}
	out, err := exec.Command("tmux", "capture-pane", "-t", tmuxName, "-p").Output()
	if err != nil {
		return false
	}
	body := string(out)
	for _, pat := range AuthLapsePatterns {
		if strings.Contains(body, pat) {
			return true
		}
	}
	return false
}
