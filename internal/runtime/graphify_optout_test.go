package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

// #725 — the Graphify opt-out (SessionInfo.GraphifyDisabled, copied from
// SpawnSpec.DisableGraphify in Spawn) MUST survive a JSON round-trip, because
// SessionInfo is persisted and restored on resume. If the json tag regresses
// or the field is dropped from the persisted shape, an operator who opted a
// session out of the code-graph plugin would silently get it back (default-on)
// after a daemon restart — a silent decision-vs-persisted divergence.
func TestSessionInfo_GraphifyDisabled_SurvivesJSONRoundTrip(t *testing.T) {
	in := SessionInfo{Name: "opted-out", GraphifyDisabled: true}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out SessionInfo
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.GraphifyDisabled {
		t.Errorf("GraphifyDisabled lost across JSON round-trip: got %v, want true (raw: %s)",
			out.GraphifyDisabled, b)
	}
}

// A default-on session (GraphifyDisabled=false) must NOT emit the field
// (omitempty), keeping persisted SessionInfo compact and back-compatible with
// records written before #725.
func TestSessionInfo_GraphifyDisabled_OmittedWhenDefault(t *testing.T) {
	b, err := json.Marshal(SessionInfo{Name: "default-on"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "graphify_disabled") {
		t.Errorf("graphify_disabled should be omitted when false (omitempty), got: %s", b)
	}
}
