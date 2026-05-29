// internal/runtime/getbycontextid_test.go — pins the #217 contract:
// Runtime.GetByContextID resolves a session against EITHER the byID
// index OR the byName index. The A2ADeliverer uses this so SendMessage
// callers can pass whichever shape they have (the long-form session ID
// returned by /api/v1/sessions, or the short @-name).
//
// Refs #208.
package runtime

import (
	"testing"
)

// TestRuntime_GetByContextID_AcceptsBothShapes seeds the runtime's
// in-memory indexes by hand (no real Spawn) and asserts GetByContextID
// returns the same SessionInfo whether queried by the full ID or the
// short name. Pin against a regression where the resolution path
// reverts to byName-only and SendMessage callers passing the full ID
// silently get -32603.
//
// Refs #208.
func TestRuntime_GetByContextID_AcceptsBothShapes(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const (
		sessID = "shepherd-1780057429428571338"
		name   = "shepherd"
	)
	info := &SessionInfo{ID: sessID, Name: name}

	// Manually seed the private maps. We are in package runtime so this
	// poke is legitimate; it lets us exercise the lookup contract
	// without needing a real PTY-backed session.New / container spawner.
	rt.mu.Lock()
	rt.info[sessID] = info
	rt.byName[name] = sessID
	rt.mu.Unlock()

	// Lookup by full session ID — the shape /api/v1/sessions returns.
	if _, gotInfo := rt.GetByContextID(sessID); gotInfo != info {
		t.Errorf("GetByContextID(%q) = %v, want %v (lookup-by-ID broken — #216 walk-failure mode)", sessID, gotInfo, info)
	}

	// Lookup by short @-name — the historical chepherd convention.
	if _, gotInfo := rt.GetByContextID(name); gotInfo != info {
		t.Errorf("GetByContextID(%q) = %v, want %v (lookup-by-name broken)", name, gotInfo, info)
	}

	// Lookup of a value matching neither index — must return nil/nil.
	if sess, gotInfo := rt.GetByContextID("nonexistent-shape"); sess != nil || gotInfo != nil {
		t.Errorf("GetByContextID(nonexistent) = (%v, %v), want (nil, nil)", sess, gotInfo)
	}
}

// TestRuntime_GetByContextID_IDPreferredOverName proves that when an
// ID-vs-name collision exists (e.g. someone spawns a session whose NAME
// equals another session's full ID, however unlikely), the byID match
// wins. Doc-pins the documented order (byID first, byName fallback) so
// a future reorder doesn't slip in unnoticed.
//
// Refs #208.
func TestRuntime_GetByContextID_IDPreferredOverName(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const conflict = "x-conflict"
	infoByID := &SessionInfo{ID: conflict, Name: "real-name"}
	infoByName := &SessionInfo{ID: "another-id", Name: conflict}

	rt.mu.Lock()
	rt.info[conflict] = infoByID
	rt.info["another-id"] = infoByName
	rt.byName[conflict] = "another-id"
	rt.byName["real-name"] = conflict
	rt.mu.Unlock()

	if _, gotInfo := rt.GetByContextID(conflict); gotInfo != infoByID {
		t.Errorf("GetByContextID(%q) returned name-match %v, want id-match %v (order wrong)", conflict, gotInfo, infoByID)
	}
}
