// internal/runtime/p0_413_mcp_not_bash_disambiguation_test.go —
// pins #413 P0: the per-agent CLAUDE.md briefing + the team-
// orientation/peer-message skills must explicitly disambiguate
// that chepherd.* MCP tools are claude-code native tool calls,
// NOT bash commands. Without this, operator-quoted regression:
//
//   ● Bash(chepherd.list_sessions 2>&1 | head -20)
//     ⎿  /bin/bash: line 1: chepherd.list_sessions: command not found
//
// Agent treated the MCP tool names as shell binaries. The briefing
// listed them in operator-friendly format but ambiguous about
// invocation — agent confabulated.
//
// Also pins the "/mcp not connected" escalation pattern so the
// agent has a correct fallback instead of pretending the tools work.
//
// Refs #413 P0 #395 #396 #404 #225.
package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestP0_413_ClaudeMD_DisambiguatesMCPVsBash locks the load-bearing
// strings: bash anti-pattern callout + native-tool-call framing +
// /mcp escalation pattern. Without these, the agent regresses to
// trying `Bash(chepherd.list_sessions)`.
func TestP0_413_ClaudeMD_DisambiguatesMCPVsBash(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	spec := SpawnSpec{Name: "test-413", Role: "worker", Team: "dev", AgentSlug: "claude-code"}
	materializeAgentBriefing(spec, tmp, nil)
	body, err := os.ReadFile(filepath.Join(tmp, ".claude", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	s := string(body)
	required := []string{
		// Native-tool-call framing
		"AUTOMATICALLY",
		"native tool calls",
		// Bash anti-pattern callout
		"Do NOT try to run them as bash commands",
		"they are NOT shell binaries",
		// /mcp escalation pattern
		"/mcp",
		"chepherd transport bug",
		"[chepherd] /mcp not connected",
		// Confabulation guard
		"Don't confabulate",
	}
	for _, sub := range required {
		if !strings.Contains(s, sub) {
			t.Errorf("CLAUDE.md missing %q\n---BODY---\n%s", sub, s)
		}
	}
}

// TestP0_413_TeamOrientationSkill_DisambiguatesMCP — same anti-bash
// callout in the team-orientation skill. Agents reading the skill
// directly via /skills must get the same disambiguation as agents
// reading CLAUDE.md.
func TestP0_413_TeamOrientationSkill_DisambiguatesMCP(t *testing.T) {
	t.Parallel()
	spec := SpawnSpec{Name: "x", Role: "worker", Team: "dev", AgentSlug: "claude-code"}
	skills := renderSkillSet(spec)
	// The map key may be either "team-orientation.md" (pre-#396) or
	// "team-orientation" (post-#396 subdir format). Look up by either.
	body := skills["team-orientation"]
	if body == "" {
		body = skills["team-orientation.md"]
	}
	if body == "" {
		t.Fatal("team-orientation skill not in renderSkillSet output")
	}
	required := []string{
		"native tool calls",
		"NOT bash commands",
		"/mcp",
		"[chepherd] /mcp not connected",
	}
	for _, sub := range required {
		if !strings.Contains(body, sub) {
			t.Errorf("team-orientation skill missing %q", sub)
		}
	}
}

// TestP0_413_PeerMessageSkill_DisambiguatesMCP — same coverage for
// peer-message skill. Operator's failure mode was specifically on
// peer messaging:
//
//	● Bash(chepherd.send_to_session code-reviewer << 'EOF' ...)
//	  ⎿  Error: Exit code 127 ... command not found
//
// so the peer-message skill must lead with the bash disambiguation.
func TestP0_413_PeerMessageSkill_DisambiguatesMCP(t *testing.T) {
	t.Parallel()
	spec := SpawnSpec{Name: "x", Role: "worker", Team: "dev", AgentSlug: "claude-code"}
	skills := renderSkillSet(spec)
	body := skills["peer-message"]
	if body == "" {
		body = skills["peer-message.md"]
	}
	if body == "" {
		t.Fatal("peer-message skill not in renderSkillSet output")
	}
	required := []string{
		"NATIVE MCP TOOL CALL",
		"not a shell binary",
		"Bash(chepherd.send_to_session ...)",
		"command not found",
		"[chepherd] /mcp not connected",
	}
	for _, sub := range required {
		if !strings.Contains(body, sub) {
			t.Errorf("peer-message skill missing %q", sub)
		}
	}
}
