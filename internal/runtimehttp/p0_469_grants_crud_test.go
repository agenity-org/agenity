// internal/runtimehttp/p0_469_grants_crud_test.go pins the v0.9.4
// §13 RBAC grant CRUD endpoints (#469 Wave D3) AND the production
// GrantCheck wiring against PR #508's mint endpoint. Asserts:
//
//   - POST /api/v1/grants creates a grant + assigns a UUID when id
//     is empty + returns 201 + the round-tripped record.
//   - GET /api/v1/grants lists all grants in §13 wire shape.
//   - GET /api/v1/grants/{id} fetches one + 404 on unknown id.
//   - DELETE /api/v1/grants/{id} removes + subsequent GET → 404.
//   - All routes return 503 when GrantStore is nil.
//   - The PersistenceGrantCheck function returns allowed=true for a
//     target SID covered by an agent-scoped grant.
//   - PersistenceGrantCheck returns allowed=false when no grant
//     covers the target — making the D2 stub deny path "live".
//   - When wired into Server.GrantCheck, POST /api/v1/jwt/mint:
//     * returns 403 for a (caller, target) pair with no grant
//     * returns 200 for a (caller, target) pair covered by a grant
//     * the minted JWT's chepherd_grant_id claim carries the grant's
//       persistence ID
//
// Refs #469 V0.9.2-ARCHITECTURE.md §13 #468.
package runtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/auth"
	"github.com/agenity-org/agenity/internal/persistence"
	"github.com/agenity-org/agenity/internal/persistence/sqlite"
)

func newGrantStoreForTest(t *testing.T) persistence.RBACGrantRepository {
	t.Helper()
	store, err := sqlite.NewStore(context.Background(),
		filepath.Join(t.TempDir(), "d3.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store.Grants()
}

func TestWaveD3_GrantsCRUD_CreateListGetDelete(t *testing.T) {
	t.Parallel()
	repo := newGrantStoreForTest(t)
	srv := httptest.NewServer((&Server{GrantStore: repo}).Handler())
	defer srv.Close()

	// POST — server assigns UUID when id omitted.
	create := `{
		"granter_org": "org-X",
		"grantee_org": "org-Y",
		"scope": {"type":"agent","agent_sid":"sid-target"},
		"permissions": ["call_agent"],
		"rate_limit": {"calls_per_minute":100,"calls_per_day":10000},
		"accepted": true,
		"created_by": "operator-X"
	}`
	resp, err := http.Post(srv.URL+"/api/v1/grants",
		"application/json", bytes.NewBufferString(create))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201", resp.StatusCode)
	}
	var created grantWire
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == "" {
		t.Fatal("server should assign id when client omits it")
	}
	if created.Scope.Type != "agent" || created.Scope.AgentSID != "sid-target" {
		t.Errorf("scope = %+v", created.Scope)
	}
	if created.RateLimit == nil || created.RateLimit.CallsPerMinute != 100 {
		t.Errorf("rate_limit = %+v", created.RateLimit)
	}

	// GET list — should contain the new grant.
	listResp, _ := http.Get(srv.URL + "/api/v1/grants")
	var listBody struct {
		Grants []grantWire `json:"grants"`
	}
	_ = json.NewDecoder(listResp.Body).Decode(&listBody)
	listResp.Body.Close()
	if len(listBody.Grants) != 1 || listBody.Grants[0].ID != created.ID {
		t.Errorf("list = %v, want 1 grant with id=%s", listBody.Grants, created.ID)
	}

	// GET single.
	getResp, _ := http.Get(srv.URL + "/api/v1/grants/" + created.ID)
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("GET status = %d, want 200", getResp.StatusCode)
	}
	getResp.Body.Close()

	// GET unknown.
	unkResp, _ := http.Get(srv.URL + "/api/v1/grants/does-not-exist")
	if unkResp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown GET status = %d, want 404", unkResp.StatusCode)
	}
	unkResp.Body.Close()

	// DELETE.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/grants/"+created.ID, nil)
	delResp, _ := http.DefaultClient.Do(req)
	if delResp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204", delResp.StatusCode)
	}
	delResp.Body.Close()

	// GET after delete → 404.
	postDel, _ := http.Get(srv.URL + "/api/v1/grants/" + created.ID)
	if postDel.StatusCode != http.StatusNotFound {
		t.Errorf("post-delete GET status = %d, want 404", postDel.StatusCode)
	}
	postDel.Body.Close()
}

func TestWaveD3_GrantsCRUD_RejectsInvalidScopeType(t *testing.T) {
	t.Parallel()
	repo := newGrantStoreForTest(t)
	srv := httptest.NewServer((&Server{GrantStore: repo}).Handler())
	defer srv.Close()

	body := `{"granter_org":"a","grantee_org":"b","scope":{"type":"bogus"}}`
	resp, _ := http.Post(srv.URL+"/api/v1/grants",
		"application/json", bytes.NewBufferString(body))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on bad scope.type", resp.StatusCode)
	}
}

func TestWaveD3_GrantsCRUD_NilStoreReturns503(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/api/v1/grants")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("nil-store GET status = %d, want 503", resp.StatusCode)
	}
}

// Save a grant directly into the repo (bypass HTTP) so the grant-check
// tests don't depend on the CRUD endpoint behaving.
func seedGrant(t *testing.T, repo persistence.RBACGrantRepository, g *persistence.Grant) {
	t.Helper()
	if err := repo.Save(context.Background(), g); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
}

func TestWaveD3_PersistenceGrantCheck_AgentScope(t *testing.T) {
	t.Parallel()
	repo := newGrantStoreForTest(t)
	seedGrant(t, repo, &persistence.Grant{
		ID:         "grant-1",
		GranterOrg: "org-X",
		GranteeOrg: "org-Y",
		Scope:      persistence.GrantScope{Type: "agent", AgentSID: "sid-target"},
		Permissions: []string{"call_agent"},
		Accepted:   true,
		CreatedBy:  "operator",
		CreatedAt:  time.Now().UTC(),
	})
	check := PersistenceGrantCheck(repo, nil)

	gid, win, allowed := check("sid-caller", "sid-target")
	if !allowed {
		t.Fatal("allowed = false, want true (agent-scoped grant matches target)")
	}
	if gid != "grant-1" {
		t.Errorf("gid = %q, want grant-1", gid)
	}
	if win == "" {
		t.Error("rate_window empty")
	}

	_, _, allowed2 := check("sid-caller", "sid-not-covered")
	if allowed2 {
		t.Error("allowed = true for uncovered target — grant scope leaked")
	}
}

func TestWaveD3_PersistenceGrantCheck_TeamScope(t *testing.T) {
	t.Parallel()
	repo := newGrantStoreForTest(t)
	seedGrant(t, repo, &persistence.Grant{
		ID:         "grant-team",
		GranterOrg: "org-X",
		GranteeOrg: "org-Y",
		Scope:      persistence.GrantScope{Type: "team", TeamID: "engineering"},
		Permissions: []string{"call_agent"},
		Accepted:   true,
		CreatedBy:  "operator",
		CreatedAt:  time.Now().UTC(),
	})
	sidToTeam := func(sid string) (string, bool) {
		switch sid {
		case "sid-on-team":
			return "engineering", true
		case "sid-off-team":
			return "marketing", true
		}
		return "", false
	}
	check := PersistenceGrantCheck(repo, sidToTeam)

	if _, _, ok := check("c", "sid-on-team"); !ok {
		t.Error("on-team target not allowed by team grant")
	}
	if _, _, ok := check("c", "sid-off-team"); ok {
		t.Error("off-team target should not be allowed by team grant")
	}
}

func TestWaveD3_PersistenceGrantCheck_DenyWhenExpired(t *testing.T) {
	t.Parallel()
	repo := newGrantStoreForTest(t)
	yesterday := time.Now().Add(-24 * time.Hour).UTC()
	seedGrant(t, repo, &persistence.Grant{
		ID:         "grant-old",
		GranterOrg: "org-X",
		GranteeOrg: "org-Y",
		Scope:      persistence.GrantScope{Type: "agent", AgentSID: "sid-target"},
		Accepted:   true,
		ExpiresAt:  &yesterday,
		CreatedBy:  "operator",
		CreatedAt:  yesterday.Add(-24 * time.Hour),
	})
	check := PersistenceGrantCheck(repo, nil)
	if _, _, ok := check("c", "sid-target"); ok {
		t.Error("expired grant should not authorize")
	}
}

// Integration: the D2 stub deny path becomes a live check. JWT mint
// against a Server with GrantStore wired + GrantCheck =
// PersistenceGrantCheck returns 403 without a grant, 200 with one.
func TestWaveD3_JWTMint_DeniesWithoutGrant_AllowsWithGrant(t *testing.T) {
	t.Parallel()
	repo := newGrantStoreForTest(t)
	priv := newES256ForTest(t)
	srv := httptest.NewServer((&Server{
		ES256Priv:  priv,
		GrantStore: repo,
		GrantCheck: PersistenceGrantCheck(repo, nil),
	}).Handler())
	defer srv.Close()

	// No grant yet — mint must return 403.
	deny, _ := http.Post(srv.URL+"/api/v1/jwt/mint", "application/json",
		bytes.NewBufferString(`{"sub":"sid-caller","aud":"sid-target"}`))
	if deny.StatusCode != http.StatusForbidden {
		t.Fatalf("pre-grant mint status = %d, want 403", deny.StatusCode)
	}
	deny.Body.Close()

	// Seed grant + mint succeeds.
	seedGrant(t, repo, &persistence.Grant{
		ID:         "grant-int",
		GranterOrg: "org-X",
		GranteeOrg: "org-Y",
		Scope:      persistence.GrantScope{Type: "agent", AgentSID: "sid-target"},
		Permissions: []string{"call_agent"},
		Accepted:   true,
		CreatedBy:  "operator",
		CreatedAt:  time.Now().UTC(),
	})
	allow, _ := http.Post(srv.URL+"/api/v1/jwt/mint", "application/json",
		bytes.NewBufferString(`{"sub":"sid-caller","aud":"sid-target"}`))
	if allow.StatusCode != http.StatusOK {
		t.Fatalf("post-grant mint status = %d, want 200", allow.StatusCode)
	}
	var body struct{ Token string }
	_ = json.NewDecoder(allow.Body).Decode(&body)
	allow.Body.Close()

	// The minted JWT must carry the grant ID in chepherd_grant_id.
	claims, err := auth.VerifyJWS(&priv.PublicKey, body.Token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims["chepherd_grant_id"] != "grant-int" {
		t.Errorf("chepherd_grant_id = %v, want grant-int", claims["chepherd_grant_id"])
	}
}
