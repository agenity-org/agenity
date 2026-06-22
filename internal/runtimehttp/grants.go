// internal/runtimehttp/grants.go implements the CRUD surface for
// the v0.9.4 §13 RBAC grant store on the chepherd-daemon HTTP API
// (#469 Wave D3). The persistence layer (sqlite + postgres) +
// schema already exist (#7); D3 adds the operator-facing HTTP
// endpoints + the production wiring of Server.GrantCheck (#468
// Wave D2's RBAC seam) to a real persistence-backed check.
//
// Endpoints:
//
//	POST   /api/v1/grants          create — mints UUID if id is empty
//	GET    /api/v1/grants          list (filters: granter_org, grantee_org, only_active)
//	GET    /api/v1/grants/{id}     fetch one
//	DELETE /api/v1/grants/{id}     remove
//
// Wire shape mirrors §13 verbatim: id, granter_org, grantee_org,
// scope{type, workspace_id, team_id, agent_sid}, permissions[],
// rate_limit{calls_per_minute, calls_per_day}, expires_at,
// accepted, created_by, created_at.
//
// Auth: routed through the same #139 bearer-token middleware as
// every other /api/v1/* endpoint.
//
// Refs #469 V0.9.2-ARCHITECTURE.md §13.
package runtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/agenity-org/agenity/internal/persistence"
)

// grantCheckTimeout bounds each PersistenceGrantCheck.List call so a
// hung DB connection can't block JWT mints indefinitely. Two seconds
// is comfortably above the local-SQLite p99 (single-digit ms) and far
// below the operator's perceptible-latency floor.
const grantCheckTimeout = 2 * time.Second

func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), grantCheckTimeout)
}

// grantWire is the wire-shape mirror of persistence.Grant. It exists
// so the §13 JSON field names (snake_case) are decoupled from the
// internal Go struct's PascalCase, AND so json.Marshal omits the
// unset nested pointers cleanly. NEVER rename without an ADR.
type grantWire struct {
	ID          string             `json:"id"`
	GranterOrg  string             `json:"granter_org"`
	GranteeOrg  string             `json:"grantee_org"`
	Scope       grantScopeWire     `json:"scope"`
	Permissions []string           `json:"permissions"`
	RateLimit   *grantRateLimitWire `json:"rate_limit,omitempty"`
	ExpiresAt   *time.Time         `json:"expires_at,omitempty"`
	Accepted    bool               `json:"accepted"`
	CreatedBy   string             `json:"created_by"`
	CreatedAt   time.Time          `json:"created_at"`
}

type grantScopeWire struct {
	Type        string `json:"type"` // workspace | team | agent
	WorkspaceID string `json:"workspace_id,omitempty"`
	TeamID      string `json:"team_id,omitempty"`
	AgentSID    string `json:"agent_sid,omitempty"`
}

type grantRateLimitWire struct {
	CallsPerMinute int `json:"calls_per_minute"`
	CallsPerDay    int `json:"calls_per_day"`
}

func toWire(g *persistence.Grant) grantWire {
	w := grantWire{
		ID:         g.ID,
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
		Accepted:    g.Accepted,
		CreatedBy:   g.CreatedBy,
		CreatedAt:   g.CreatedAt,
	}
	if g.RateLimit != nil {
		w.RateLimit = &grantRateLimitWire{
			CallsPerMinute: g.RateLimit.CallsPerMinute,
			CallsPerDay:    g.RateLimit.CallsPerDay,
		}
	}
	return w
}

func fromWire(w *grantWire) *persistence.Grant {
	g := &persistence.Grant{
		ID:         w.ID,
		GranterOrg: w.GranterOrg,
		GranteeOrg: w.GranteeOrg,
		Scope: persistence.GrantScope{
			Type:        w.Scope.Type,
			WorkspaceID: w.Scope.WorkspaceID,
			TeamID:      w.Scope.TeamID,
			AgentSID:    w.Scope.AgentSID,
		},
		Permissions: w.Permissions,
		ExpiresAt:   w.ExpiresAt,
		Accepted:    w.Accepted,
		CreatedBy:   w.CreatedBy,
		CreatedAt:   w.CreatedAt,
	}
	if w.RateLimit != nil {
		g.RateLimit = &persistence.GrantRateLimit{
			CallsPerMinute: w.RateLimit.CallsPerMinute,
			CallsPerDay:    w.RateLimit.CallsPerDay,
		}
	}
	return g
}

// grantsRoot handles /api/v1/grants — GET list, POST create.
func (s *Server) grantsRoot(w http.ResponseWriter, r *http.Request) {
	if s.GrantStore == nil {
		http.Error(w, "grant store not initialised", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		opts := persistence.GrantListOpts{}
		q := r.URL.Query()
		opts.GranterOrg = q.Get("granter_org")
		opts.GranteeOrg = q.Get("grantee_org")
		opts.OnlyActive = q.Get("only_active") == "true"
		list, err := s.GrantStore.List(r.Context(), opts)
		if err != nil {
			http.Error(w, "list: "+err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]grantWire, 0, len(list))
		for _, g := range list {
			out = append(out, toWire(g))
		}
		writeJSON(w, http.StatusOK, map[string]any{"grants": out})
	case http.MethodPost:
		var body grantWire
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.GranterOrg == "" || body.GranteeOrg == "" {
			http.Error(w, "granter_org and grantee_org are required",
				http.StatusBadRequest)
			return
		}
		switch body.Scope.Type {
		case "workspace", "team", "agent":
		default:
			http.Error(w, "scope.type must be one of workspace|team|agent",
				http.StatusBadRequest)
			return
		}
		if body.ID == "" {
			body.ID = uuid.NewString()
		}
		if body.CreatedAt.IsZero() {
			body.CreatedAt = time.Now().UTC()
		}
		g := fromWire(&body)
		if err := s.GrantStore.Save(r.Context(), g); err != nil {
			http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, toWire(g))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// grantByID handles /api/v1/grants/{id} — GET fetch, DELETE remove.
func (s *Server) grantByID(w http.ResponseWriter, r *http.Request) {
	if s.GrantStore == nil {
		http.Error(w, "grant store not initialised", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/grants/")
	id = strings.SplitN(id, "/", 2)[0]
	if id == "" {
		http.Error(w, "grant id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		g, err := s.GrantStore.Get(r.Context(), id)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "get: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, toWire(g))
	case http.MethodDelete:
		if err := s.GrantStore.Delete(r.Context(), id); err != nil {
			http.Error(w, "delete: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// PersistenceGrantCheck is the production GrantCheckFn that consults
// the persistence-backed grant store. Wired into Server.GrantCheck
// by cmd/run.go when the daemon starts with persistence enabled.
//
// Match rules (v0.9.4 §13 single-org interim semantics):
//
//   - The grant must be Accepted and not Expired.
//   - scope.type=agent: scope.agent_sid == targetSID → match
//   - scope.type=team: targetSID's session.Team == scope.team_id → match
//     (requires runtime lookup; nil-runtime path skips team scope)
//   - scope.type=workspace: workspace identity is not yet attached to
//     sessions → workspace grants are conservatively NOT matched in
//     v0.9.4. Wave W (workspace persistence) wires this branch.
//
// Cross-org `granter_org`/`grantee_org` validation is intentionally
// deferred to Wave D8 (federation). For single-org chepherd the orgs
// collapse to the daemon's local identity, so the org match is
// trivially satisfied; the gate is the scope-match.
//
// Returns the first matching grant's ID + a minute-quantized rate
// window identifier. Returns allowed=false when no grant matches.
//
// Refs #469 #468.
func PersistenceGrantCheck(store persistence.RBACGrantRepository, sessionTeam func(sid string) (string, bool)) GrantCheckFn {
	return func(callerSID, targetSID string) (string, string, bool) {
		if store == nil {
			return "", "", false
		}
		ctx, cancel := contextWithTimeout()
		defer cancel()
		grants, err := store.List(ctx, persistence.GrantListOpts{OnlyActive: true})
		if err != nil {
			return "", "", false
		}
		var targetTeam string
		var haveTeam bool
		if sessionTeam != nil {
			targetTeam, haveTeam = sessionTeam(targetSID)
		}
		for _, g := range grants {
			if !grantCovers(g, targetSID, targetTeam, haveTeam) {
				continue
			}
			return g.ID, rateWindowNow(), true
		}
		return "", "", false
	}
}

func grantCovers(g *persistence.Grant, targetSID, targetTeam string, haveTeam bool) bool {
	switch g.Scope.Type {
	case "agent":
		return g.Scope.AgentSID != "" && g.Scope.AgentSID == targetSID
	case "team":
		return haveTeam && g.Scope.TeamID != "" && g.Scope.TeamID == targetTeam
	case "workspace":
		// Workspace identity not yet attached to sessions — see comment
		// on PersistenceGrantCheck. Conservative skip.
		return false
	}
	return false
}

// rateWindowNow returns the minute-quantized accounting bucket the
// §15.2 chepherd_rate_window claim points at. Format is ISO-8601
// at minute precision so it sorts lexicographically.
func rateWindowNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04")
}
