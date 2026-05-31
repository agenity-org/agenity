// internal/persistence/sqlite/audit_events_test.go — #489 Wave AU2
// unit assertions on the SQLite-backed AuditEventRepository.
//
// Named assertions T1-T7:
//
//	T1 — Save + List round-trip on a single event
//	T2 — List requires OrgID (privacy guard) — empty returns error
//	T3 — Caller filter
//	T4 — Callee filter
//	T5 — Method filter
//	T6 — Since/Until time-range filter
//	T7 — Limit + ordering (timestamp DESC)
//
// Refs #489.
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

func newAuditStore(t *testing.T) persistence.AuditEventRepository {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.NewStore(context.Background(), dir+"/audit.sqlite")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store.AuditEvents()
}

func mkEvent(id, org, caller, callee, method string, ts time.Time) *persistence.AuditEventRecord {
	return &persistence.AuditEventRecord{
		ID:        id,
		OrgID:     org,
		EventType: "audit.received",
		Timestamp: ts,
		Caller:    caller,
		Callee:    callee,
		Method:    method,
		LatencyMS: 42,
		JTI:       "j-" + id,
		Status:    "success",
	}
}

func TestAU2_T1_SaveAndList_RoundTrip(t *testing.T) {
	repo := newAuditStore(t)
	ctx := context.Background()
	ev := mkEvent("e1", "org-A", "caller-X", "callee-Y", "message/send", time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC))
	if err := repo.Save(ctx, ev); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := repo.List(ctx, persistence.AuditEventListOpts{OrgID: "org-A"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("T1 FAIL: len = %d, want 1", len(got))
	}
	r := got[0]
	if r.ID != "e1" || r.Caller != "caller-X" || r.Callee != "callee-Y" ||
		r.Method != "message/send" || r.LatencyMS != 42 || r.JTI != "j-e1" {
		t.Errorf("T1 FAIL: round-trip mismatch: %+v", r)
	}
}

func TestAU2_T2_ListRequiresOrgID(t *testing.T) {
	repo := newAuditStore(t)
	_, err := repo.List(context.Background(), persistence.AuditEventListOpts{})
	if err == nil {
		t.Errorf("T2 FAIL: empty OrgID returned no error — privacy guard broken")
	}
}

func TestAU2_T3_CallerFilter(t *testing.T) {
	repo := newAuditStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = repo.Save(ctx, mkEvent("e1", "org-A", "alpha", "x", "m", now))
	_ = repo.Save(ctx, mkEvent("e2", "org-A", "beta", "x", "m", now))
	got, err := repo.List(ctx, persistence.AuditEventListOpts{OrgID: "org-A", Caller: "alpha"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "e1" {
		t.Errorf("T3 FAIL: got %d rows, want 1 (e1); rows=%+v", len(got), got)
	}
}

func TestAU2_T4_CalleeFilter(t *testing.T) {
	repo := newAuditStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = repo.Save(ctx, mkEvent("e1", "org-A", "x", "alpha", "m", now))
	_ = repo.Save(ctx, mkEvent("e2", "org-A", "x", "beta", "m", now))
	got, _ := repo.List(ctx, persistence.AuditEventListOpts{OrgID: "org-A", Callee: "beta"})
	if len(got) != 1 || got[0].ID != "e2" {
		t.Errorf("T4 FAIL: got %d rows, want 1 (e2)", len(got))
	}
}

func TestAU2_T5_MethodFilter(t *testing.T) {
	repo := newAuditStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = repo.Save(ctx, mkEvent("e1", "org-A", "x", "y", "message/send", now))
	_ = repo.Save(ctx, mkEvent("e2", "org-A", "x", "y", "tasks/get", now))
	got, _ := repo.List(ctx, persistence.AuditEventListOpts{OrgID: "org-A", Method: "tasks/get"})
	if len(got) != 1 || got[0].ID != "e2" {
		t.Errorf("T5 FAIL: got %d rows, want 1 (e2)", len(got))
	}
}

func TestAU2_T6_TimeRangeFilter(t *testing.T) {
	repo := newAuditStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 31, 18, 0, 0, 0, time.UTC)
	_ = repo.Save(ctx, mkEvent("e1", "org-A", "x", "y", "m", t0))
	_ = repo.Save(ctx, mkEvent("e2", "org-A", "x", "y", "m", t1))
	_ = repo.Save(ctx, mkEvent("e3", "org-A", "x", "y", "m", t2))
	since := t1
	until := t2
	got, _ := repo.List(ctx, persistence.AuditEventListOpts{
		OrgID: "org-A",
		Since: &since,
		Until: &until,
	})
	if len(got) != 2 {
		t.Errorf("T6 FAIL: got %d rows, want 2 (e2+e3)", len(got))
	}
	for _, r := range got {
		if r.ID == "e1" {
			t.Errorf("T6 FAIL: e1 leaked through time-range filter")
		}
	}
}

func TestAU2_T7_LimitAndOrdering(t *testing.T) {
	repo := newAuditStore(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_ = repo.Save(ctx, mkEvent(
			"e"+string(rune('0'+i)),
			"org-A",
			"x", "y", "m",
			base.Add(time.Duration(i)*time.Minute),
		))
	}
	got, _ := repo.List(ctx, persistence.AuditEventListOpts{OrgID: "org-A", Limit: 3})
	if len(got) != 3 {
		t.Fatalf("T7 FAIL: got %d rows, want 3", len(got))
	}
	// Ordering: most recent first.
	if got[0].ID != "e4" || got[1].ID != "e3" || got[2].ID != "e2" {
		t.Errorf("T7 FAIL: ordering [%s, %s, %s], want [e4, e3, e2]", got[0].ID, got[1].ID, got[2].ID)
	}
}
