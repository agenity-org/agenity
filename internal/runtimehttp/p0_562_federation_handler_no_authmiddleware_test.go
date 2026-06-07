// p0_562_federation_handler_no_authmiddleware_test.go guards against
// regression of #562: the federation mTLS listener must use
// FederationHandler() (no authMiddleware) not Handler() (authMiddleware
// wraps all /api/v1/* → hub's forwarded request hits 401).
package runtimehttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestP0_562_FederationHandler_BypassesAuthMiddleware verifies that
// FederationHandler() routes /api/v1/federation/jwt without requiring
// a Bearer token — i.e., authMiddleware is NOT wrapping the handler.
// With a non-empty AuthToken on the Server, Handler() would return 401;
// FederationHandler() must return non-401 (200 or 503/400 for bad body,
// but NOT 401).
func TestP0_562_FederationHandler_BypassesAuthMiddleware(t *testing.T) {
	srv := &Server{
		OrgID:     "bob.example",
		AuthToken: "secret-bearer-token", // dashboard listener would block w/o this token
		// KeyStore intentionally nil → endpoint returns 503 (not configured)
		// which is fine — the key test is NO 401.
	}

	h := srv.FederationHandler()
	hs := httptest.NewServer(h)
	defer hs.Close()

	req, _ := http.NewRequest("POST", hs.URL+"/api/v1/federation/jwt",
		strings.NewReader(`{"scope":"a2a.send","audience":"runner-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Chepherd-Caller-Org", "alice.example")
	req.Header.Set("X-Chepherd-Hub-Attest", "true")
	// Deliberately NO Authorization header — simulates hub forwarding
	// without a daemon Bearer token.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("FederationHandler returned 401 — authMiddleware is incorrectly blocking cross-org traffic (#562 regression)")
	}
	// 503 = KeyStore not wired (expected here since KeyStore==nil), but NOT 401.
	t.Logf("FederationHandler status=%d (expected non-401; 503 OK when KeyStore not set)", resp.StatusCode)
}

// TestP0_562_Handler_RequiresBearerForFedPath verifies that the DASHBOARD
// handler (Handler()) DOES require auth on /api/v1/federation/jwt, so
// we don't accidentally remove authMiddleware from the dashboard path.
func TestP0_562_Handler_RequiresBearerForFedPath(t *testing.T) {
	srv := &Server{
		OrgID:     "bob.example",
		AuthToken: "secret-bearer-token",
		WebDir:    "",
	}

	h := srv.Handler()
	hs := httptest.NewServer(h)
	defer hs.Close()

	req, _ := http.NewRequest("POST", hs.URL+"/api/v1/federation/jwt",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Handler returned %d, want 401 — dashboard path should still require auth", resp.StatusCode)
	}
}
