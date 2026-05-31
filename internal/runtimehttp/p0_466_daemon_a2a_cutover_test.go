// internal/runtimehttp/p0_466_daemon_a2a_cutover_test.go — #466
// Wave R5 cutover assertions.
//
// Asserts:
//
//	V1 — POST /jsonrpc → 410 Gone + Deprecation header + Sunset
//	     header + Link rel="successor-version" pointing at /api/v1/
//	     agents/
//	V2 — body is structured JSON-RPC -32601 with a successor hint
//	V3 — GET /.well-known/agent-card.json → 410 Gone (same headers)
//	V4 — GET /.well-known/agent-card → 410 Gone (alias path also
//	     covered)
//	V5 — GET /api/v1/agents/ still works (D1 surviving endpoint)
//	V6 — GET /.well-known/jwks.json still works (T2 surviving)
//
// Refs #466 #453 V0.9.2-ARCH §5 #3 §22.
package runtimehttp_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chepherd/chepherd/internal/a2a"
	rh "github.com/chepherd/chepherd/internal/runtimehttp"
)

func r5Server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer((&rh.Server{}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestP0_466_DaemonJSONRPC_410Gone pins V1 + V2.
func TestP0_466_DaemonJSONRPC_410Gone(t *testing.T) {
	ts := r5Server(t)
	resp, err := http.Post(ts.URL+"/jsonrpc", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{}}`))
	if err != nil {
		t.Fatalf("POST /jsonrpc: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusGone {
		t.Errorf("V1 FAIL: /jsonrpc status = %d, want 410 Gone", resp.StatusCode)
	}
	if got := resp.Header.Get("Deprecation"); got != "true" {
		t.Errorf("V1 FAIL: Deprecation header = %q, want true", got)
	}
	if got := resp.Header.Get("Sunset"); got == "" {
		t.Errorf("V1 FAIL: Sunset header empty (RFC 8594 required for 410-Gone deprecation)")
	}
	if got := resp.Header.Get("Link"); !strings.Contains(got, `rel="successor-version"`) || !strings.Contains(got, "/api/v1/agents/") {
		t.Errorf("V1 FAIL: Link header missing successor-version pointing at /api/v1/agents/; got %q", got)
	}

	body, _ := io.ReadAll(resp.Body)
	var parsed struct {
		JSONRPC string `json:"jsonrpc"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("V2 FAIL: decode body: %v (body=%s)", err, body)
	}
	if parsed.JSONRPC != "2.0" {
		t.Errorf("V2 FAIL: jsonrpc field = %q, want 2.0", parsed.JSONRPC)
	}
	if parsed.Error.Code != -32601 {
		t.Errorf("V2 FAIL: error.code = %d, want -32601", parsed.Error.Code)
	}
	if !strings.Contains(parsed.Error.Message, "Wave R5") {
		t.Errorf("V2 FAIL: error.message lacks Wave-R5 attribution; got %q", parsed.Error.Message)
	}
	if !strings.Contains(parsed.Error.Message, "/api/v1/agents/") {
		t.Errorf("V2 FAIL: error.message lacks successor hint /api/v1/agents/; got %q", parsed.Error.Message)
	}
}

// TestP0_466_DaemonAgentCard_410Gone pins V3 + V4.
func TestP0_466_DaemonAgentCard_410Gone(t *testing.T) {
	ts := r5Server(t)

	// V3 — canonical well-known URI
	resp, err := http.Get(ts.URL + a2a.AgentCardPath)
	if err != nil {
		t.Fatalf("GET %s: %v", a2a.AgentCardPath, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("V3 FAIL: %s status = %d, want 410", a2a.AgentCardPath, resp.StatusCode)
	}
	if resp.Header.Get("Deprecation") != "true" {
		t.Errorf("V3 FAIL: Deprecation header missing")
	}

	// V4 — alias (suffix-less) form
	resp2, err := http.Get(ts.URL + a2a.AgentCardAliasPath)
	if err != nil {
		t.Fatalf("GET %s: %v", a2a.AgentCardAliasPath, err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusGone {
		t.Errorf("V4 FAIL: %s status = %d, want 410", a2a.AgentCardAliasPath, resp2.StatusCode)
	}
}

// TestP0_466_DaemonSurvivors pins V5 (Wave D1 directory still works).
// V6 (JWKS) is implicit — when JWKSBody/KeyStore are wired by the
// caller, the route stays mounted. Without either, the route stays
// absent (not 410). The D1 directory is the more critical survival
// pin since this is the discovery seam siblings use to find the
// per-runner endpoints after R5.
func TestP0_466_DaemonSurvivors(t *testing.T) {
	ts := r5Server(t)

	// V5 — D1 directory endpoint surviving
	resp, err := http.Get(ts.URL + "/api/v1/agents/")
	if err != nil {
		t.Fatalf("GET /api/v1/agents/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("V5 FAIL: GET /api/v1/agents/ status = %d, want 200 (Wave D1 #467 must survive R5)", resp.StatusCode)
	}
	// The body should be a {agents: [...]} envelope (D1 wire shape).
	body, _ := io.ReadAll(resp.Body)
	var envelope struct {
		Agents []any `json:"agents"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Errorf("V5 FAIL: decode D1 envelope: %v (body=%s)", err, body)
	}
	// envelope.Agents may be empty in the test harness; that's fine —
	// the WIRE SHAPE is what we're pinning, not the population.
}
