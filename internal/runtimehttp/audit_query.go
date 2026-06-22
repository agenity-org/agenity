// internal/runtimehttp/audit_query.go — #489 Wave AU2.
//
// GET /api/v1/audit/events query endpoint over the daemon's
// AuditEventStore. Per-org partitioned (caller's org is the
// receiver-daemon's DaemonOrgID — cross-org events from federation
// peers stay scoped to the receiver, privacy by default).
//
// Query parameters (all optional):
//   - caller=<sub claim>      filter by caller
//   - callee=<runner sid>     filter by callee
//   - method=<a2a method>     filter by JSON-RPC method
//   - since=<RFC3339>         filter timestamp >= since
//   - until=<RFC3339>         filter timestamp <= until
//   - limit=<n>               max rows, default 100, max 1000
//
// Auth: gated by the standard authMiddleware (Bearer JWT) when Auth
// is wired. Dev / unit-test mode (Auth nil) passes through. RBAC
// scope: org-internal queries only — the per-org partition guard at
// the repository layer enforces this even if a request crosses orgs
// (the repository returns nothing when OrgID doesn't match).
//
// Refs #489 #488 V0.9.2-ARCH §10 §13.
package runtimehttp

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

func (s *Server) handleAuditEventsQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.AuditEventStore == nil {
		http.Error(w, "audit event store not wired", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	opts := persistence.AuditEventListOpts{
		OrgID:  s.effectiveDaemonOrgID(),
		Caller: q.Get("caller"),
		Callee: q.Get("callee"),
		Method: q.Get("method"),
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "invalid 'since' (want RFC3339): "+err.Error(), http.StatusBadRequest)
			return
		}
		opts.Since = &t
	}
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "invalid 'until' (want RFC3339): "+err.Error(), http.StatusBadRequest)
			return
		}
		opts.Until = &t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "invalid 'limit' (want positive integer)", http.StatusBadRequest)
			return
		}
		opts.Limit = n
	}
	recs, err := s.AuditEventStore.List(r.Context(), opts)
	if err != nil {
		http.Error(w, "audit_events query: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Project repository records into the wire shape. JSON field
	// names match runtime.AuditEvent (the inbound wire from runner).
	out := make([]map[string]any, 0, len(recs))
	for _, ev := range recs {
		row := map[string]any{
			"id":         ev.ID,
			"event_type": ev.EventType,
			"timestamp":  ev.Timestamp.UTC().Format(time.RFC3339Nano),
			"caller":     ev.Caller,
			"callee":     ev.Callee,
			"method":     ev.Method,
			"latency_ms": ev.LatencyMS,
			"status":     ev.Status,
		}
		if ev.JTI != "" {
			row["jti"] = ev.JTI
		}
		if ev.Error != "" {
			row["error"] = ev.Error
		}
		if ev.TaskID != "" {
			row["task_id"] = ev.TaskID
		}
		out = append(out, row)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"events": out,
		"org_id": opts.OrgID,
	})
}

// effectiveDaemonOrgID returns the DaemonOrgID config field or the
// "default" sentinel when unset. Matches the same default the
// audit-event ingest uses so saved + queried org_id stay aligned in
// dev / unit-test mode.
func (s *Server) effectiveDaemonOrgID() string {
	if s.DaemonOrgID == "" {
		return "default"
	}
	return s.DaemonOrgID
}
