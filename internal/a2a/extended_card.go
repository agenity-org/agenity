// internal/a2a/extended_card.go implements the v0.9.4 §7 + A2A v1.0
// agent/getAuthenticatedExtendedCard method body (#483 Wave A4).
//
// The PUBLIC Agent Card is served at /.well-known/agent-card.json
// for unauthenticated discovery. The EXTENDED card carries the same
// AgentCard fields PLUS authenticated-only annotations the caller's
// grants entitle them to see:
//
//   - x-chepherd-auth.subject — the validated bearer subject so
//     callers can confirm they're seeing the right scope.
//   - x-chepherd-auth.audit_endpoint — daemon-side audit URL the
//     caller's grants authorize them to query (per §14 audit
//     sovereignty).
//   - x-chepherd-auth.grants — every active grant naming this
//     caller as grantee, with scope details visible inline.
//   - x-chepherd-auth.rate_usage — per-grant rate-limit configuration
//     (calls_per_minute / calls_per_day) so the caller knows the
//     budget they're operating within. "Remaining" counts will fill
//     in once a future Wave ships a rate-limit counter; A4 emits
//     the configured limits + leaves the remaining-counts unset.
//   - x-chepherd-p2p — placeholder for the chepherd P2P extension
//     (F-series WebRTC fields). A4 ships the field structure; F2/F4
//     populate the ICE candidates + signaling URL.
//
// Auth gate: caller MUST authenticate via Bearer JWT (D2 mint + T2
// JWKS verify path). Unauthenticated → JSON-RPC error -32001 per
// the §15.3 auth-failure shape. The PUBLIC card stays available at
// the well-known URL for unauthenticated discovery.
//
// Refs #483 V0.9.2-ARCHITECTURE.md §7 §14 §15.3.
package a2a

import (
	"context"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// ExtendedAgentCard is the wire shape of getAuthenticatedExtendedCard's
// response — the AgentCard fields plus the x-chepherd-auth annotation.
// JSON field names are spec-frozen; do NOT rename without an ADR.
type ExtendedAgentCard struct {
	AgentCard
	XChepherdAuth *AuthenticatedExtension `json:"x-chepherd-auth,omitempty"`
}

// AuthenticatedExtension annotates the Agent Card with fields only
// visible to authenticated callers per §7. Each field is omit-empty
// so the wire stays compact when the caller's grants don't unlock
// the corresponding piece.
type AuthenticatedExtension struct {
	Subject       string         `json:"subject"`
	AuditEndpoint string         `json:"audit_endpoint,omitempty"`
	Grants        []GrantSummary `json:"grants,omitempty"`
	RateUsage     *RateUsage     `json:"rate_usage,omitempty"`
}

// GrantSummary is the inline grant detail emitted with the extended
// card. Reuses the persistence.Grant fields but excludes the
// granter-internal CreatedBy / CreatedAt so consumers see scope +
// rate-limit only.
type GrantSummary struct {
	GrantID     string             `json:"grant_id"`
	GranterOrg  string             `json:"granter_org"`
	GranteeOrg  string             `json:"grantee_org"`
	Scope       grantScopeWire     `json:"scope"`
	Permissions []string           `json:"permissions"`
	RateLimit   *grantRateLimitW   `json:"rate_limit,omitempty"`
	ExpiresAt   *time.Time         `json:"expires_at,omitempty"`
}

type grantScopeWire struct {
	Type        string `json:"type"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	TeamID      string `json:"team_id,omitempty"`
	AgentSID    string `json:"agent_sid,omitempty"`
}

type grantRateLimitW struct {
	CallsPerMinute int `json:"calls_per_minute"`
	CallsPerDay    int `json:"calls_per_day"`
}

// RateUsage carries the rolling-window rate-limit configuration +
// remaining-budget snapshot. v0.9.4 ships the configured limits;
// the CallsRemaining* fields are nil pointers (omit-empty) until a
// future Wave wires a real rate counter. Caller code is encouraged
// to assume "limit only, no remaining" and refetch when limits
// matter; that contract stays valid once remaining counts land.
type RateUsage struct {
	CallsPerMinuteLimit  int  `json:"calls_per_minute_limit"`
	CallsPerDayLimit     int  `json:"calls_per_day_limit"`
	CallsRemainingMinute *int `json:"calls_remaining_in_minute,omitempty"`
	CallsRemainingDay    *int `json:"calls_remaining_in_day,omitempty"`
}

// handleGetAuthenticatedExtendedCard implements the spec method.
// Steps:
//
//  1. Require authenticated subject (JWT verified by AuthMiddleware).
//     Unauthenticated → -32001.
//  2. Load the public Agent Card via AgentCardFn.
//  3. Look up every active grant naming the caller as grantee.
//  4. Build the AuthenticatedExtension with subject, audit endpoint,
//     grants, and per-grant rate-limit configuration.
//  5. Return ExtendedAgentCard wrapping the public card.
//
// Refs #483.
func (m *MethodBodies) handleGetAuthenticatedExtendedCard(req JSONRPCRequest) JSONRPCResponse {
	if m.AgentCardFn == nil {
		return errorResp(req.ID, ErrCodeInternalError, "AgentCard provider not wired")
	}
	if req.AuthSubject == "" {
		// -32001 is the established auth-failure JSON-RPC code per the
		// §15.3 auth-required chain + routes.go AuthMiddleware's
		// writeAuthError shape. Returned with HTTP 200 in the JSON-RPC
		// envelope; AuthMiddleware never let an unauthenticated request
		// reach the body when fully wired, so this branch only fires
		// when the middleware is unwired (dev mode) AND the caller is
		// asking for the extended card anyway. Conservative: deny.
		return errorResp(req.ID, -32001, "authentication required: agent/getAuthenticatedExtendedCard needs a valid Bearer JWT")
	}
	card := m.AgentCardFn()
	ext := &AuthenticatedExtension{
		Subject:       req.AuthSubject,
		AuditEndpoint: m.auditEndpoint(),
	}
	if m.Store != nil {
		grants := m.lookupGranteeGrants(req.AuthSubject)
		ext.Grants = grants
		ext.RateUsage = summarizeRateUsage(grants)
	}
	// A2A v1.0 §7.11: result IS the AgentCard directly, not nested under a
	// "card" key. Return ExtendedAgentCard (embeds AgentCard) as the result.
	return JSONRPCResponse{
		JSONRPC: "2.0", ID: req.ID,
		Result: ExtendedAgentCard{
			AgentCard:     card,
			XChepherdAuth: ext,
		},
	}
}

// auditEndpoint returns the daemon-side audit URL the caller's grants
// authorize them to query. v0.9.4 emits a stable path placeholder;
// the actual audit-API ships in a future Wave. AgentCardFn returns
// the public URL; we splice "/api/v1/audit" onto its base.
func (m *MethodBodies) auditEndpoint() string {
	card := m.AgentCardFn()
	// AgentCard.URL ends in "/jsonrpc" per spec; strip that to get
	// the daemon base + append the audit path. Defensive: if URL
	// doesn't end in "/jsonrpc" (e.g. test card), return the base
	// as-is + the audit suffix.
	base := card.URL
	if len(base) > len("/jsonrpc") && base[len(base)-len("/jsonrpc"):] == "/jsonrpc" {
		base = base[:len(base)-len("/jsonrpc")]
	}
	return base + "/api/v1/audit"
}

// lookupGranteeGrants returns every active grant whose GranteeOrg
// matches the caller's subject. v0.9.4's auth subject IS the
// grantee identifier (single-org chepherd; cross-org claim mapping
// lands with Wave D8). The single-arg List call filters by
// granteeOrg + OnlyActive.
func (m *MethodBodies) lookupGranteeGrants(subject string) []GrantSummary {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := m.Store.Grants().List(ctx, persistence.GrantListOpts{
		GranteeOrg: subject,
		OnlyActive: true,
	})
	if err != nil {
		return nil
	}
	out := make([]GrantSummary, 0, len(rows))
	for _, g := range rows {
		out = append(out, toGrantSummary(g))
	}
	return out
}

func toGrantSummary(g *persistence.Grant) GrantSummary {
	s := GrantSummary{
		GrantID:    g.ID,
		GranterOrg: g.GranterOrg,
		GranteeOrg: g.GranteeOrg,
		Scope: grantScopeWire{
			Type:        g.Scope.Type,
			WorkspaceID: g.Scope.WorkspaceID,
			TeamID:      g.Scope.TeamID,
			AgentSID:    g.Scope.AgentSID,
		},
		Permissions: g.Permissions,
		ExpiresAt:   g.ExpiresAt,
	}
	if g.RateLimit != nil {
		s.RateLimit = &grantRateLimitW{
			CallsPerMinute: g.RateLimit.CallsPerMinute,
			CallsPerDay:    g.RateLimit.CallsPerDay,
		}
	}
	return s
}

// summarizeRateUsage picks the LOOSEST (most permissive) per-minute
// + per-day limit across the caller's grants — the caller's actual
// budget is whatever combination they qualify for, but for display
// purposes we surface the highest cap so the consumer sees the
// ceiling that matters. Returns nil when no grant carries a rate
// limit configuration.
func summarizeRateUsage(grants []GrantSummary) *RateUsage {
	var bestMin, bestDay int
	for _, g := range grants {
		if g.RateLimit == nil {
			continue
		}
		if g.RateLimit.CallsPerMinute > bestMin {
			bestMin = g.RateLimit.CallsPerMinute
		}
		if g.RateLimit.CallsPerDay > bestDay {
			bestDay = g.RateLimit.CallsPerDay
		}
	}
	if bestMin == 0 && bestDay == 0 {
		return nil
	}
	return &RateUsage{
		CallsPerMinuteLimit: bestMin,
		CallsPerDayLimit:    bestDay,
		// CallsRemaining* left nil — populated when the rate-limit
		// counter Wave ships.
	}
}
