// internal/webrtcrtc/ice_config_test.go — #493 Wave F3 unit tests
// for the STUN/TURN flag parsers + the combined ICE-server
// generator.
//
// Named assertions F3.I1-I8:
//
//	I1 — ParseSTUNFlag accepts bare addr:port (auto-prefixes stun:)
//	I2 — ParseSTUNFlag accepts already-prefixed stun:addr:port
//	I3 — ParseSTUNFlag rejects empty + missing-port forms
//	I4 — ParseTURNFlag round-trips addr:port:user:pass into
//	     ICEServer{URLs, Username, Credential}
//	I5 — ParseTURNFlag rejects bare TURN without auth (security
//	     hole guard)
//	I6 — ParseTURNFlag rejects malformed (wrong field count)
//	I7 — ParseICEServers combines slices; first-fail short-circuits
//	     + surfaces the offending flag value
//	I8 — Empty slices produce empty result (fall-through to F1
//	     DefaultICEServers happens at the consumer)
//
// Refs #493.
package webrtcrtc

import (
	"strings"
	"testing"
)

func TestF3_I1_ParseSTUN_BareForm(t *testing.T) {
	got, err := ParseSTUNFlag("stun.example.com:3478")
	if err != nil {
		t.Fatalf("I1 FAIL: %v", err)
	}
	if len(got.URLs) != 1 || got.URLs[0] != "stun:stun.example.com:3478" {
		t.Errorf("I1 FAIL: URLs = %+v, want [stun:stun.example.com:3478]", got.URLs)
	}
}

func TestF3_I2_ParseSTUN_AlreadyPrefixed(t *testing.T) {
	got, err := ParseSTUNFlag("stun:stun.example.com:3478")
	if err != nil {
		t.Fatalf("I2 FAIL: %v", err)
	}
	if got.URLs[0] != "stun:stun.example.com:3478" {
		t.Errorf("I2 FAIL: URLs[0] = %q", got.URLs[0])
	}
	// stuns: also accepted.
	got2, err := ParseSTUNFlag("stuns:tls.stun.example.com:5349")
	if err != nil {
		t.Fatalf("I2 FAIL stuns: %v", err)
	}
	if got2.URLs[0] != "stuns:tls.stun.example.com:5349" {
		t.Errorf("I2 FAIL: stuns URL = %q", got2.URLs[0])
	}
}

func TestF3_I3_ParseSTUN_RejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "stun.example.com" /* no port */} {
		if _, err := ParseSTUNFlag(bad); err == nil {
			t.Errorf("I3 FAIL: ParseSTUNFlag(%q) accepted; want error", bad)
		}
	}
}

func TestF3_I4_ParseTURN_RoundTrip(t *testing.T) {
	got, err := ParseTURNFlag("turn.example.com:3478:alice:s3cret")
	if err != nil {
		t.Fatalf("I4 FAIL: %v", err)
	}
	if len(got.URLs) != 1 || got.URLs[0] != "turn:turn.example.com:3478" {
		t.Errorf("I4 FAIL: URLs = %+v", got.URLs)
	}
	if got.Username != "alice" {
		t.Errorf("I4 FAIL: Username = %q, want alice", got.Username)
	}
	if cred, _ := got.Credential.(string); cred != "s3cret" {
		t.Errorf("I4 FAIL: Credential = %v, want s3cret", got.Credential)
	}
}

func TestF3_I5_ParseTURN_RejectsBareWithoutAuth(t *testing.T) {
	// 4 fields, but two empty (no user, no pass) → reject.
	for _, bad := range []string{
		"turn.example.com:3478::",      // empty user + empty pass
		"turn.example.com:3478:alice:", // empty pass
		"turn.example.com:3478::s3cret", // empty user
	} {
		_, err := ParseTURNFlag(bad)
		if err == nil {
			t.Errorf("I5 FAIL: ParseTURNFlag(%q) accepted bare-no-auth TURN", bad)
			continue
		}
		if !strings.Contains(err.Error(), "credentials") && !strings.Contains(err.Error(), "user") && !strings.Contains(err.Error(), "pass") {
			t.Errorf("I5 FAIL: error msg should explain missing credentials; got %q", err)
		}
	}
}

func TestF3_I6_ParseTURN_RejectsMalformed(t *testing.T) {
	for _, bad := range []string{
		"",
		"turn.example.com",                 // missing everything
		"turn.example.com:3478",            // missing user+pass
		"turn.example.com:3478:alice",      // missing pass
	} {
		if _, err := ParseTURNFlag(bad); err == nil {
			t.Errorf("I6 FAIL: ParseTURNFlag(%q) accepted; want error", bad)
		}
	}
}

func TestF3_I7_ParseICEServers_CombinesAndShortCircuits(t *testing.T) {
	// Happy path
	got, err := ParseICEServers(
		[]string{"stun.a.com:3478", "stun:stun.b.com:3478"},
		[]string{"turn.c.com:3478:u:p"},
	)
	if err != nil {
		t.Fatalf("I7 FAIL happy: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("I7 FAIL: len = %d, want 3", len(got))
	}
	// First-fail surfaces the bad value
	_, err = ParseICEServers(
		[]string{"good.com:3478", ""},
		[]string{"turn.c.com:3478:u:p"},
	)
	if err == nil {
		t.Errorf("I7 FAIL: empty STUN value should short-circuit")
	}
}

func TestF3_I8_ParseICEServers_EmptyProducesEmpty(t *testing.T) {
	got, err := ParseICEServers(nil, nil)
	if err != nil {
		t.Fatalf("I8 FAIL: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("I8 FAIL: empty input → len=%d, want 0", len(got))
	}
}
