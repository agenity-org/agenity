// internal/a2a/p0_483_extended_card_test.go pins the v0.9.4 §7 +
// A2A v1.0 agent/getAuthenticatedExtendedCard method body (#483
// Wave A4).
//
// Asserts:
//   - Unauthenticated (AuthSubject empty) → JSON-RPC -32001 error
//     envelope, no card content leaked.
//   - Authenticated + no grants → returns the public AgentCard
//     embedded inside ExtendedAgentCard + x-chepherd-auth carrying
//     subject + audit_endpoint, with empty grants slice.
//   - Authenticated + grants present → x-chepherd-auth.grants
//     enumerates the caller's active grants with scope details +
//     RateUsage summarizes per-minute / per-day limits.
//   - The wire shape is JSON-serializable and the spec field names
//     (`x-chepherd-auth`, `audit_endpoint`, `rate_usage`,
//     `calls_per_minute_limit`, etc.) survive a round-trip.
//
// Refs #483 V0.9.2-ARCHITECTURE.md §7 §15.3.
package a2a

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

func newExtendedCardTestRouter(t *testing.T) (*Router, *MethodBodies) {
	t.Helper()
	r, mb := newTestRouter(t)
	mb.AgentCardFn = func() AgentCard {
		return AgentCard{
			ProtocolVersion: "1.0",
			Name:            "test-runner",
			URL:             "http://daemon.example.com/jsonrpc",
		}
	}
	return r, mb
}

func callExtended(t *testing.T, r *Router, subject string) JSONRPCResponse {
	t.Helper()
	body, _ := json.Marshal(map[string]any{})
	req := JSONRPCRequest{
		JSONRPC:     "2.0",
		ID:          json.RawMessage(`"1"`),
		Method:      "agent/getAuthenticatedExtendedCard",
		Params:      body,
		AuthSubject: subject,
	}
	return r.handlers[canonicalizeMethod("agent/getAuthenticatedExtendedCard")](req)
}

func TestWaveA4_ExtendedCard_UnauthenticatedReturns32001(t *testing.T) {
	t.Parallel()
	r, _ := newExtendedCardTestRouter(t)
	resp := callExtended(t, r, "" /* no subject */)
	if resp.Error == nil {
		t.Fatalf("expected error envelope, got %+v", resp)
	}
	if resp.Error.Code != -32001 {
		t.Errorf("error code = %d, want -32001", resp.Error.Code)
	}
	if resp.Result != nil {
		t.Errorf("result should be nil on auth failure, got %v", resp.Result)
	}
}

func TestWaveA4_ExtendedCard_AuthenticatedNoGrants_EmitsBaseExtension(t *testing.T) {
	t.Parallel()
	r, _ := newExtendedCardTestRouter(t)
	resp := callExtended(t, r, "operator")
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result, ok := resp.Result.(getExtendedAgentCardResult)
	if !ok {
		t.Fatalf("Result type = %T, want getExtendedAgentCardResult", resp.Result)
	}
	if result.Card.Name != "test-runner" {
		t.Errorf("embedded AgentCard.Name = %q, want test-runner", result.Card.Name)
	}
	ext := result.Card.XChepherdAuth
	if ext == nil {
		t.Fatal("x-chepherd-auth extension missing")
	}
	if ext.Subject != "operator" {
		t.Errorf("Subject = %q, want operator", ext.Subject)
	}
	if ext.AuditEndpoint != "http://daemon.example.com/api/v1/audit" {
		t.Errorf("AuditEndpoint = %q, want http://daemon.example.com/api/v1/audit", ext.AuditEndpoint)
	}
	if len(ext.Grants) != 0 {
		t.Errorf("Grants = %v, want empty (no grants seeded)", ext.Grants)
	}
	if ext.RateUsage != nil {
		t.Errorf("RateUsage should be nil with no grants, got %+v", ext.RateUsage)
	}
}

func TestWaveA4_ExtendedCard_WithGrants_EnumeratesAndSummarizes(t *testing.T) {
	t.Parallel()
	r, mb := newExtendedCardTestRouter(t)

	// Seed a grant naming the test caller as grantee.
	grant := &persistence.Grant{
		ID:         "grant-A",
		GranterOrg: "org-X",
		GranteeOrg: "caller-org",
		Scope:      persistence.GrantScope{Type: "agent", AgentSID: "sid-T"},
		Permissions: []string{"call_agent", "subscribe_streaming"},
		RateLimit:   &persistence.GrantRateLimit{CallsPerMinute: 60, CallsPerDay: 5000},
		Accepted:   true,
		CreatedBy:  "operator",
		CreatedAt:  time.Now().UTC(),
	}
	if err := mb.Store.Grants().Save(context.Background(), grant); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	// And a SECOND grant with a higher per-minute limit so the
	// summary picks the higher cap.
	grant2 := &persistence.Grant{
		ID:         "grant-B",
		GranterOrg: "org-Y",
		GranteeOrg: "caller-org",
		Scope:      persistence.GrantScope{Type: "team", TeamID: "engineering"},
		Permissions: []string{"call_agent"},
		RateLimit:   &persistence.GrantRateLimit{CallsPerMinute: 120, CallsPerDay: 3000},
		Accepted:   true,
		CreatedBy:  "operator",
		CreatedAt:  time.Now().UTC(),
	}
	if err := mb.Store.Grants().Save(context.Background(), grant2); err != nil {
		t.Fatalf("seed grant 2: %v", err)
	}

	resp := callExtended(t, r, "caller-org")
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result := resp.Result.(getExtendedAgentCardResult)
	ext := result.Card.XChepherdAuth
	if ext == nil {
		t.Fatal("x-chepherd-auth extension missing")
	}
	if len(ext.Grants) != 2 {
		t.Fatalf("Grants len = %d, want 2", len(ext.Grants))
	}
	// Grants by ID for stable assertion regardless of List order.
	byID := map[string]GrantSummary{}
	for _, g := range ext.Grants {
		byID[g.GrantID] = g
	}
	if g := byID["grant-A"]; g.Scope.Type != "agent" || g.Scope.AgentSID != "sid-T" {
		t.Errorf("grant-A scope wrong: %+v", g.Scope)
	}
	if g := byID["grant-B"]; g.Scope.Type != "team" || g.Scope.TeamID != "engineering" {
		t.Errorf("grant-B scope wrong: %+v", g.Scope)
	}
	if ext.RateUsage == nil {
		t.Fatal("RateUsage should summarize seeded grants")
	}
	if ext.RateUsage.CallsPerMinuteLimit != 120 {
		t.Errorf("CallsPerMinuteLimit = %d, want 120 (higher cap)", ext.RateUsage.CallsPerMinuteLimit)
	}
	if ext.RateUsage.CallsPerDayLimit != 5000 {
		t.Errorf("CallsPerDayLimit = %d, want 5000 (higher cap)", ext.RateUsage.CallsPerDayLimit)
	}
	// CallsRemaining* are nil pointers in v0.9.4 (rate-counter not
	// yet shipped). Their omit-empty behavior keeps the wire compact.
	if ext.RateUsage.CallsRemainingMinute != nil {
		t.Errorf("CallsRemainingMinute should be nil pre-rate-counter-Wave, got %v", ext.RateUsage.CallsRemainingMinute)
	}
}

func TestWaveA4_ExtendedCard_WireShape_RoundTripsThroughJSON(t *testing.T) {
	t.Parallel()
	r, mb := newExtendedCardTestRouter(t)
	if err := mb.Store.Grants().Save(context.Background(), &persistence.Grant{
		ID: "g1", GranterOrg: "x", GranteeOrg: "y",
		Scope:    persistence.GrantScope{Type: "agent", AgentSID: "sid-z"},
		Accepted: true, CreatedBy: "op", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	resp := callExtended(t, r, "y")
	result := resp.Result.(getExtendedAgentCardResult)

	body, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Decode back via a generic map and confirm the spec field
	// names are exactly what's on the wire.
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	card, _ := raw["card"].(map[string]any)
	auth, _ := card["x-chepherd-auth"].(map[string]any)
	if auth == nil {
		t.Fatalf("x-chepherd-auth missing from wire: %s", body)
	}
	if auth["subject"] != "y" {
		t.Errorf("wire subject = %v, want y", auth["subject"])
	}
	if auth["audit_endpoint"] == "" {
		t.Errorf("wire audit_endpoint missing: %v", auth)
	}
	grants, _ := auth["grants"].([]any)
	if len(grants) != 1 {
		t.Fatalf("wire grants count = %d, want 1", len(grants))
	}
	g0, _ := grants[0].(map[string]any)
	if g0["grant_id"] != "g1" {
		t.Errorf("wire grant_id = %v, want g1", g0["grant_id"])
	}
	scope, _ := g0["scope"].(map[string]any)
	if scope["type"] != "agent" || scope["agent_sid"] != "sid-z" {
		t.Errorf("wire scope = %v", scope)
	}
}
