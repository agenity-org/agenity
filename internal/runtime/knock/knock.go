// Package knock — #472 Wave K1.
//
// Per V0.9.2-ARCHITECTURE.md §10 Pattern 1 step 12: when runner-A
// delivers an A2A task to runner-B, A writes ONE marker line to B's
// PTY so claude-code's pattern-detector sees it + calls
// chepherd.get_task on its own. NO submit sequence — no Enter, no
// Ctrl-J, no extra newlines. Just the marker + a single trailing
// LF.
//
// Wire format (operator-locked, do NOT rename fields without an
// ADR — K2 #473 + future federation knock interop depend on these
// exact bytes):
//
//	[chepherd-knock taskID=<uuid> from=<name>]\n
//
// The marker may arrive mid-stream with arbitrary preceding bytes
// — ANSI escape sequences from the agent's TUI, log-line
// interleaving from chepherd itself, etc. Parse MUST therefore be
// substring-anchored, NOT prefix-anchored.
//
// Refs #472 #473 V0.9.2-ARCHITECTURE.md §10 Pattern 1.
package knock

import (
	"fmt"
	"regexp"
	"strings"
)

// Marker is the §10 step-12 wire format. fmt verbs are %s/%s; pass
// the taskID string + the from-name string in that order.
const Marker = "[chepherd-knock taskID=%s from=%s]\n"

// MarkerPrefix is the verbatim substring every knock line begins
// with (after any leading ANSI noise). Used by detectors that just
// need a fast "is this line a knock?" check before paying for the
// regex.
const MarkerPrefix = "[chepherd-knock "

// FormatKnock returns the marker string for the given taskID + from
// name, ready to write to the recipient's PTY.
func FormatKnock(taskID, from string) string {
	return fmt.Sprintf(Marker, taskID, from)
}

// knockRE matches a knock marker anywhere in a byte stream. The
// taskID + from groups capture identifier-safe characters; this
// rejects markers whose values have been corrupted by truncation.
//
// Pattern: literal `[chepherd-knock taskID=` + group1 + ` from=` +
// group2 + `]`. taskID accepts UUID chars (hex + dash); from accepts
// the same identifier chars chepherd's @-handles use (alnum + dash +
// underscore).
var knockRE = regexp.MustCompile(
	`\[chepherd-knock taskID=([0-9a-fA-F-]+) from=([A-Za-z0-9_-]+)\]`,
)

// ParseKnock extracts (taskID, from) from a byte slice containing a
// knock marker. Robust to leading ANSI escape sequences + log-line
// interleaving — the regex is unanchored. Returns ok=false when no
// well-formed marker is present.
//
// If the input contains MULTIPLE knock markers, ParseKnock returns
// the FIRST one. Callers needing all markers should scan with
// ParseAll (filed as follow-up; AU1 single-knock-per-task contract
// today).
func ParseKnock(line []byte) (taskID, from string, ok bool) {
	m := knockRE.FindSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return string(m[1]), string(m[2]), true
}

// ContainsKnock is the fast-path substring check — returns true if
// the input could contain a knock marker. Useful for pump loops that
// want to skip the regex on lines that obviously aren't knocks.
func ContainsKnock(line []byte) bool {
	return strings.Contains(string(line), MarkerPrefix)
}
