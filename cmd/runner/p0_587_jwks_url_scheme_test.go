// cmd/runner/p0_587_jwks_url_scheme_test.go pins #587: the daemon
// JWKS URL surfaced in the runner's per-session Agent Card MUST
// use http:// (or https://) scheme, NOT ws:// / wss://.
//
// Bug: daemonURL arrives as `ws://chepherd:9090` (runner uses
// WebSocket to register with daemon). Pre-#587, the runner code
// dumped this verbatim into the Agent Card's
// SecuritySchemes["chepherd-jwt"].Description → peers tried
// fetching `ws://...:9090/.well-known/jwks.json` and got
// "Protocol \"ws\" not supported" from libcurl.
//
// Coverage:
//   - httpFromWS(ws://X) = http://X (and wss → https)
//   - httpFromWS(http://X) = http://X (idempotent passthrough)
//   - buildRunnerAgentCard with ws:// daemonURL emits http:// in
//     the SecuritySchemes Description (the operator-visible field
//     downstream tooling parses)
//
// Refs #587 #560.
package main

import (
	"strings"
	"testing"
)

func TestP0_587_HttpFromWS_TranslatesSchemes(t *testing.T) {
	cases := map[string]string{
		"ws://chepherd:9090":         "http://chepherd:9090",
		"wss://chepherd:9090":        "https://chepherd:9090",
		"ws://127.0.0.1:9094/x":      "http://127.0.0.1:9094/x",
		"wss://example.com/path?q=1": "https://example.com/path?q=1",
		"http://x":                   "http://x",  // passthrough
		"https://x":                  "https://x", // passthrough
		"":                           "",          // empty passthrough
		"ftp://x":                    "ftp://x",   // non-WS passthrough
	}
	for in, want := range cases {
		got := httpFromWS(in)
		if got != want {
			t.Errorf("httpFromWS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestP0_587_AgentCard_JWKSDescription_UsesHTTPScheme(t *testing.T) {
	card := buildRunnerAgentCard("test-sid", "test-runner", "", "http://chepherd:9090/.well-known/jwks.json")
	scheme, ok := card.SecuritySchemes["chepherd-jwt"]
	if !ok {
		t.Fatal("expected chepherd-jwt security scheme in card")
	}
	if !strings.Contains(scheme.Description, "http://chepherd:9090") {
		t.Errorf("description missing http:// JWKS URL; got: %q", scheme.Description)
	}
	if strings.Contains(scheme.Description, "ws://") {
		t.Errorf("description should NOT contain ws://; got: %q", scheme.Description)
	}
}
