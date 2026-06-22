// chepherd — a TUI supervisor for parallel AI coding agents.
//
// Watches every Claude Code session running in tmux on this host, scores each
// on goal/velocity/focus/end-state-proximity, and coaches them when they drift
// using the operator's own ~/.claude/CLAUDE.md as the rubric.
//
// See https://github.com/agenity-org/agenity for documentation.
package main

import (
	"fmt"
	"os"

	"github.com/agenity-org/agenity/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "chepherd:", err)
		os.Exit(1)
	}
}
