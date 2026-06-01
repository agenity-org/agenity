// internal/runtime/k4_knock_briefing_test.go — #475 Wave K4 unit
// assertions for the agent briefing's knock-handling section. Pins
// the operator-locked sentences that teach the agent the §10
// Pattern 1 knock contract.
//
// Named assertions K4.B1-B6:
//
//	B1 — Section header "Inbound peer messages — the knock pattern"
//	B2 — Marker format shown verbatim: [chepherd-knock taskID=<uuid>
//	     from=<name>]
//	B3 — Action #1 mentions chepherd.get_task by exact tool name
//	B4 — Recipient-scoping warning present (mentions -32004 forbidden)
//	B5 — Reply instruction: "chepherd.send_to_session(from_name, reply_body)"
//	     for real-time sender notification (B5b checks wrong anti-pattern absent)
//	B6 — Briefing knock section is positioned BEFORE the operator
//	     section (so chronological reading hits knock-handling before
//	     escalation guidance)
//
// Refs #475 #472 #473.
package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestK4_KnockSection_AllLandmarksPresent(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	spec := SpawnSpec{
		Name:      "k4-worker",
		Role:      "worker",
		Team:      "k4-team",
		AgentSlug: "claude-code",
	}
	materializeAgentBriefing(spec, tmp, nil)
	body, err := os.ReadFile(filepath.Join(tmp, ".claude", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	s := string(body)

	// B1
	if !strings.Contains(s, "Inbound peer messages — the knock pattern") {
		t.Errorf("B1 FAIL: section header missing")
	}
	// B2 — exact marker format
	if !strings.Contains(s, "[chepherd-knock taskID=<uuid> from=<name>]") {
		t.Errorf("B2 FAIL: knock marker format missing or wrong")
	}
	// B3 — get_task tool by exact name
	if !strings.Contains(s, "chepherd.get_task(taskID)") {
		t.Errorf("B3 FAIL: chepherd.get_task(taskID) reference missing")
	}
	// B4 — recipient-scoping
	if !strings.Contains(s, "-32004 forbidden") {
		t.Errorf("B4 FAIL: -32004 forbidden warning missing (recipient-scoping)")
	}
	// B5 — reply via send_to_session for real-time delivery (correct pattern)
	if !strings.Contains(s, "chepherd.send_to_session(from_name, reply_body)") {
		t.Errorf("B5 FAIL: send_to_session reply instruction missing")
	}
	// B5b — wrong anti-pattern must NOT be present
	if strings.Contains(s, "Don't reply by calling `chepherd.send_to_session` back") {
		t.Errorf("B5b FAIL: anti-pattern 'Don't reply via send_to_session' still present — must be removed")
	}
	// B6 — knock section before operator section
	knockIdx := strings.Index(s, "Inbound peer messages — the knock pattern")
	opIdx := strings.Index(s, "## How to talk to the operator")
	if knockIdx < 0 || opIdx < 0 {
		t.Errorf("B6 FAIL: one of the section headers missing entirely")
	} else if knockIdx > opIdx {
		t.Errorf("B6 FAIL: knock section appears AFTER operator section (knockIdx=%d, opIdx=%d) — agent should see knock-handling before escalation guidance", knockIdx, opIdx)
	}
}

// TestK4_KnockSection_PointsAt_K2_GetTask asserts the briefing
// describes the K2 #473 tool contract (returns {task, input}). If
// K2's wire shape changes, this test catches the briefing drift.
func TestK4_KnockSection_PointsAt_K2_GetTask(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	spec := SpawnSpec{
		Name:      "k4-w",
		Role:      "worker",
		Team:      "k4-team",
		AgentSlug: "claude-code",
	}
	materializeAgentBriefing(spec, tmp, nil)
	body, _ := os.ReadFile(filepath.Join(tmp, ".claude", "CLAUDE.md"))
	s := string(body)
	// K2 returns {task, input} per the dispatch case. If we drop
	// "input" from the return value docs, the agent won't know to
	// read parts[].text.
	if !strings.Contains(s, "{task, input}") {
		t.Errorf("K2 return shape doc missing — should show `{task, input}` so agent knows to read input.parts[].text")
	}
}
