package runtimehttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chepherd/chepherd/internal/canon"
	"github.com/chepherd/chepherd/internal/roles"
)

// newTestServerWithCanon builds a minimal Server with just canon +
// roles wired (no runtime, no vault) so the HTTP routes can be
// exercised in isolation without a full chepherd boot.
func newTestServerWithCanon(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	c, err := canon.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := roles.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{canon: c, roles: r}
}

func TestCanonRoot_GetReturnsBlankBeforeFirstPut(t *testing.T) {
	s := newTestServerWithCanon(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/canon", nil)
	rr := httptest.NewRecorder()
	s.canonRoot(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got canon.Canon
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != 0 {
		t.Errorf("version = %d, want 0 before any Put", got.Version)
	}
}

func TestCanonRoot_PutBumpsVersion(t *testing.T) {
	s := newTestServerWithCanon(t)
	body := `{"body":"first canon","title":"Operator Canon","updated_by":"alice"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/canon", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	s.canonRoot(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got canon.Canon
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Version != 1 || got.Body != "first canon" || got.UpdatedBy != "alice" {
		t.Fatalf("unexpected canon: %+v", got)
	}
}

func TestCanonHistoryAndRollback(t *testing.T) {
	s := newTestServerWithCanon(t)
	// Put three versions
	for i, body := range []string{"v1", "v2", "v3"} {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/canon",
			bytes.NewBufferString(`{"body":"`+body+`"}`))
		rr := httptest.NewRecorder()
		s.canonRoot(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("Put %d: status %d", i+1, rr.Code)
		}
	}
	// History should have v1 + v2 (current is v3, not in history)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/canon/history", nil)
	rr := httptest.NewRecorder()
	s.canonHistory(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("history: %d", rr.Code)
	}
	var hist []canon.Canon
	_ = json.Unmarshal(rr.Body.Bytes(), &hist)
	if len(hist) < 2 {
		t.Fatalf("expected ≥2 history entries, got %d", len(hist))
	}
	// Rollback to v1
	req = httptest.NewRequest(http.MethodPost, "/api/v1/canon/rollback",
		bytes.NewBufferString(`{"to_version":1,"actor":"bob"}`))
	rr = httptest.NewRecorder()
	s.canonRollback(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("rollback: %d body=%s", rr.Code, rr.Body.String())
	}
	var rolled canon.Canon
	_ = json.Unmarshal(rr.Body.Bytes(), &rolled)
	if rolled.Body != "v1" || rolled.Version != 4 {
		t.Fatalf("rollback wrong: %+v", rolled)
	}
}

func TestCanonRollback_NotFound(t *testing.T) {
	s := newTestServerWithCanon(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/canon",
		bytes.NewBufferString(`{"body":"v1"}`))
	rr := httptest.NewRecorder()
	s.canonRoot(rr, req)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/canon/rollback",
		bytes.NewBufferString(`{"to_version":99}`))
	rr = httptest.NewRecorder()
	s.canonRollback(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("rollback non-existent version: status %d, want 404", rr.Code)
	}
}

func TestCanonRoot_RejectsBadJSON(t *testing.T) {
	s := newTestServerWithCanon(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/canon",
		bytes.NewBufferString(`{not json`))
	rr := httptest.NewRecorder()
	s.canonRoot(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad json: status %d, want 400", rr.Code)
	}
}

func TestRolesRoot_ListReturns12Builtins(t *testing.T) {
	s := newTestServerWithCanon(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/roles", nil)
	rr := httptest.NewRecorder()
	s.rolesRoot(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var got []roles.Role
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if len(got) != 12 {
		t.Fatalf("expected 12 builtin roles, got %d", len(got))
	}
}

func TestRoleByID_BuiltinUpdateRejected(t *testing.T) {
	s := newTestServerWithCanon(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/roles/architect",
		bytes.NewBufferString(`{"name":"hacked"}`))
	rr := httptest.NewRecorder()
	s.roleByID(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT on builtin: status %d, want 405", rr.Code)
	}
}

func TestRoleByID_GetBuiltin(t *testing.T) {
	s := newTestServerWithCanon(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/roles/code-reviewer", nil)
	rr := httptest.NewRecorder()
	s.roleByID(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET builtin: status %d", rr.Code)
	}
	var got roles.Role
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.ID != "code-reviewer" {
		t.Fatalf("id = %q, want code-reviewer", got.ID)
	}
	// Pair-conditional clause must be present in the response body
	if !strings.Contains(got.PrimaryPrompt, "Pair") {
		t.Error("code-reviewer GET missing Pair-conditional clause")
	}
}

func TestRolesRoot_CreateUserRole(t *testing.T) {
	s := newTestServerWithCanon(t)
	body := `{"name":"Cost Analyst","primary_prompt":"You watch unit economics."}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/roles",
		bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	s.rolesRoot(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST user role: status %d body=%s", rr.Code, rr.Body.String())
	}
	var got roles.Role
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if !strings.HasPrefix(got.ID, "user-") {
		t.Fatalf("user role ID should start with user-: %q", got.ID)
	}
}
