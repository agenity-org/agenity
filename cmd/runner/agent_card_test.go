// cmd/runner/agent_card_test.go — unit tests for the #464 Wave R3
// per-session Agent Card builder.
//
// Asserts the spec-required fields are populated + chepherd-extension
// blocks present + baseURL/daemonURL templating works correctly.
//
// Refs #464 V0.9.2-ARCHITECTURE §5 #9 §7 §12.1.
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chepherd/chepherd/internal/a2a"
)

// TestR3_BuildRunnerAgentCard_RequiredFields pins the A2A v1.0
// spec-required fields: protocolVersion, name, url, version,
// capabilities, defaultInputModes, defaultOutputModes, skills.
func TestR3_BuildRunnerAgentCard_RequiredFields(t *testing.T) {
	card := buildRunnerAgentCard("sid-1", "runner-name", "http://runner:9091", "http://daemon:9090/.well-known/jwks.json")

	if card.ProtocolVersion != "1.0" {
		t.Errorf("protocolVersion = %q, want 1.0", card.ProtocolVersion)
	}
	if card.Name != "chepherd-runner-sid-1" {
		t.Errorf("name = %q, want chepherd-runner-sid-1", card.Name)
	}
	if card.URL != "http://runner:9091/a2a/sid-1/jsonrpc" {
		t.Errorf("url = %q, want http://runner:9091/a2a/sid-1/jsonrpc", card.URL)
	}
	if card.Version == "" {
		t.Errorf("version is empty — must report runnerSelfVersion")
	}
	if len(card.DefaultInputModes) == 0 {
		t.Errorf("defaultInputModes empty — required by A2A v1.0 spec")
	}
	if len(card.DefaultOutputModes) == 0 {
		t.Errorf("defaultOutputModes empty — required by A2A v1.0 spec")
	}
	if card.Skills == nil {
		t.Errorf("skills field is nil — must be empty array, not absent")
	}
}

// TestR3_BuildRunnerAgentCard_Capabilities pins the streaming-on
// declaration (Wave A1 #511 shipped inline SSE binding), push-
// notifications-off (Wave A3 lights it), extendedCard-off (Wave A5).
func TestR3_BuildRunnerAgentCard_Capabilities(t *testing.T) {
	card := buildRunnerAgentCard("sid-1", "", "", "")
	if !card.Capabilities.Streaming {
		t.Errorf("capabilities.streaming = false; want true (Wave A1 #511 shipped SSE)")
	}
	if card.Capabilities.PushNotifications {
		t.Errorf("capabilities.pushNotifications = true; want false until Wave A3 lights it")
	}
	if card.Capabilities.ExtendedCard {
		t.Errorf("capabilities.extendedCard = true; want false until Wave A5 ships state-transition history")
	}
}

// TestR3_BuildRunnerAgentCard_SecurityScheme pins the JWT auth-
// scheme advertisement. Peers read this to know how to authenticate
// SendMessage calls.
func TestR3_BuildRunnerAgentCard_SecurityScheme(t *testing.T) {
	card := buildRunnerAgentCard("sid-1", "", "", "http://daemon:9090/.well-known/jwks.json")
	if len(card.Security) == 0 {
		t.Fatalf("security array empty")
	}
	if _, ok := card.Security[0]["chepherd-jwt"]; !ok {
		t.Errorf("security[0] lacks chepherd-jwt key; got %+v", card.Security[0])
	}
	scheme, ok := card.SecuritySchemes["chepherd-jwt"]
	if !ok {
		t.Fatalf("securitySchemes lacks chepherd-jwt entry")
	}
	if scheme.Type != "http" {
		t.Errorf("scheme.type = %q, want http", scheme.Type)
	}
	if scheme.Scheme != "bearer" {
		t.Errorf("scheme.scheme = %q, want bearer", scheme.Scheme)
	}
	if scheme.BearerFormat != "JWT" {
		t.Errorf("scheme.bearerFormat = %q, want JWT", scheme.BearerFormat)
	}
	if !strings.Contains(scheme.Description, "http://daemon:9090/.well-known/jwks.json") {
		t.Errorf("scheme.description should reference daemon JWKS URL; got %q", scheme.Description)
	}
}

// TestR3_BuildRunnerAgentCard_JWKSDefaultsToRelative pins that an
// empty daemonJWKSURL falls back to the §12.1 well-known relative
// path — peers resolve against the daemon they discovered the card
// through.
func TestR3_BuildRunnerAgentCard_JWKSDefaultsToRelative(t *testing.T) {
	card := buildRunnerAgentCard("sid-1", "", "", "")
	scheme := card.SecuritySchemes["chepherd-jwt"]
	if !strings.Contains(scheme.Description, "/.well-known/jwks.json") {
		t.Errorf("scheme.description should reference /.well-known/jwks.json fallback; got %q", scheme.Description)
	}
	if strings.Contains(scheme.Description, "http://") || strings.Contains(scheme.Description, "https://") {
		t.Errorf("scheme.description should NOT include scheme when daemonJWKSURL empty; got %q", scheme.Description)
	}
}

// TestR3_BuildRunnerAgentCard_ChepherdP2PExtension pins the x-chepherd-p2p
// placeholder per the architect's R3 contract (F2/F3/F4 populate the
// actual WebRTC capabilities; R3 ensures the block is PRESENT so
// chepherd-aware peers see the extension marker).
func TestR3_BuildRunnerAgentCard_ChepherdP2PExtension(t *testing.T) {
	card := buildRunnerAgentCard("sid-1", "", "", "")
	if card.XChepherdP2P == nil {
		t.Errorf("x-chepherd-p2p block absent; should be present (placeholder per architect R3 contract; F-Waves populate)")
	}
}

// TestR3_BuildRunnerAgentCard_DescriptionIncludesRunnerName pins the
// operator-handle surface — when --name is set, description mentions
// the @-handle so dashboard / operator audit sees which runner is
// which.
func TestR3_BuildRunnerAgentCard_DescriptionIncludesRunnerName(t *testing.T) {
	card := buildRunnerAgentCard("sid-1", "iogrid-1", "", "")
	if !strings.Contains(card.Description, "@iogrid-1") {
		t.Errorf("description should include operator handle when --name set; got %q", card.Description)
	}
}

// TestR3_BuildRunnerAgentCard_URLRelativeWhenBaseEmpty pins the
// relative-URL fallback for the spec-required `url` field — agents
// resolve against the host they fetched the card from.
func TestR3_BuildRunnerAgentCard_URLRelativeWhenBaseEmpty(t *testing.T) {
	card := buildRunnerAgentCard("sid-1", "", "", "")
	if card.URL != "/a2a/sid-1/jsonrpc" {
		t.Errorf("url = %q, want /a2a/sid-1/jsonrpc when baseURL empty", card.URL)
	}
}

// TestR3_BuildRunnerAgentCard_StripsTrailingSlash pins the off-by-one
// defense — operators may pass baseURL with a trailing slash; we
// must not emit a double-slash in the spec-required url field.
func TestR3_BuildRunnerAgentCard_StripsTrailingSlash(t *testing.T) {
	card := buildRunnerAgentCard("sid-1", "", "http://runner:9091/", "")
	if card.URL != "http://runner:9091/a2a/sid-1/jsonrpc" {
		t.Errorf("url = %q, want no double slash", card.URL)
	}
}

// TestR3_AgentCard_JSON_RoundTrips marshals the card → JSON →
// re-unmarshals and asserts the field values round-trip. Catches a
// missing struct tag (silently dropped on marshal) before any
// downstream A2A peer hits a parse error.
func TestR3_AgentCard_JSON_RoundTrips(t *testing.T) {
	card := buildRunnerAgentCard("sid-1", "n1", "http://runner:9091", "http://daemon:9090/.well-known/jwks.json")
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Spot-check spec-frozen field name presence in the JSON bytes
	// (catches a Go-side rename that doesn't preserve the JSON tag).
	for _, want := range []string{
		`"protocolVersion":"1.0"`,
		`"name":"chepherd-runner-sid-1"`,
		`"url":"http://runner:9091/a2a/sid-1/jsonrpc"`,
		`"capabilities":`,
		`"streaming":true`,
		`"defaultInputModes":`,
		`"securitySchemes":`,
		`"bearerFormat":"JWT"`,
		`"x-chepherd-p2p":`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("JSON marshal missing %q. Full JSON: %s", want, raw)
		}
	}

	// Round-trip back into a2a.AgentCard.
	var decoded a2a.AgentCard
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ProtocolVersion != "1.0" || decoded.URL != "http://runner:9091/a2a/sid-1/jsonrpc" {
		t.Errorf("round-trip mismatch: %+v", decoded)
	}
}
