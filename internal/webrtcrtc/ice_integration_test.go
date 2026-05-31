// internal/webrtcrtc/ice_integration_test.go — #493 Wave F3
// integration: prove the ParseICEServers result flows correctly
// into Config + the resulting Config produces a PeerConnection that
// LISTS the configured ICE servers (we can't drive a full STUN/TURN
// candidate exchange without a real public STUN server, so this
// pins the wiring up to the boundary).
//
// Named assertions F3.J1-J3:
//
//	J1 — Config.ICEServers populated from ParseICEServers result
//	     flows into PeerConnection's internal pion configuration
//	J2 — Empty Config.ICEServers falls through to DefaultICEServers
//	     (F1 #491 behavior preserved)
//	J3 — TURN with credentials reaches pion as Username + Credential
//	     (not stripped during the parse → config conversion)
//
// Refs #493 #491.
package webrtcrtc

import (
	"strings"
	"testing"
)

func TestF3_J1_ConfigICEServersFlowIntoPeerConnection(t *testing.T) {
	servers, err := ParseICEServers(
		[]string{"stun.example.com:3478"},
		[]string{"turn.example.com:3478:alice:s3cret"},
	)
	if err != nil {
		t.Fatalf("ParseICEServers: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("J1 setup: len(servers) = %d, want 2", len(servers))
	}

	pc, err := NewPeerConnection(Config{ICEServers: servers})
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	defer pc.Close()

	cfg := pc.pc.GetConfiguration()
	if len(cfg.ICEServers) != 2 {
		t.Fatalf("J1 FAIL: pion config ICEServers len = %d, want 2", len(cfg.ICEServers))
	}
	// Verify our STUN + TURN URLs landed unchanged.
	flat := []string{}
	for _, s := range cfg.ICEServers {
		flat = append(flat, s.URLs...)
	}
	wantSTUN := "stun:stun.example.com:3478"
	wantTURN := "turn:turn.example.com:3478"
	hasSTUN := false
	hasTURN := false
	for _, u := range flat {
		if u == wantSTUN {
			hasSTUN = true
		}
		if u == wantTURN {
			hasTURN = true
		}
	}
	if !hasSTUN {
		t.Errorf("J1 FAIL: STUN URL %q missing from pion config (got %+v)", wantSTUN, flat)
	}
	if !hasTURN {
		t.Errorf("J1 FAIL: TURN URL %q missing from pion config (got %+v)", wantTURN, flat)
	}
}

func TestF3_J2_EmptyFallsThroughToDefault(t *testing.T) {
	pc, err := NewPeerConnection(Config{ICEServers: nil})
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	defer pc.Close()

	cfg := pc.pc.GetConfiguration()
	if len(cfg.ICEServers) == 0 {
		t.Fatalf("J2 FAIL: default ICEServers should be populated (F1 fallback)")
	}
	// One of them should be the public Google STUN that
	// DefaultICEServers returns.
	flat := []string{}
	for _, s := range cfg.ICEServers {
		flat = append(flat, s.URLs...)
	}
	found := false
	for _, u := range flat {
		if strings.Contains(u, "stun.l.google.com") {
			found = true
		}
	}
	if !found {
		t.Errorf("J2 FAIL: DefaultICEServers fallback should include stun.l.google.com; got %+v", flat)
	}
}

func TestF3_J3_TURNCredentialsReachPionUnstripped(t *testing.T) {
	servers, err := ParseICEServers(nil, []string{"turn.example.com:3478:alice:s3cret"})
	if err != nil {
		t.Fatalf("ParseICEServers: %v", err)
	}
	pc, err := NewPeerConnection(Config{ICEServers: servers})
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	defer pc.Close()

	cfg := pc.pc.GetConfiguration()
	if len(cfg.ICEServers) != 1 {
		t.Fatalf("J3 FAIL: cfg.ICEServers len = %d, want 1", len(cfg.ICEServers))
	}
	turn := cfg.ICEServers[0]
	if turn.Username != "alice" {
		t.Errorf("J3 FAIL: TURN Username = %q, want alice", turn.Username)
	}
	if cred, _ := turn.Credential.(string); cred != "s3cret" {
		t.Errorf("J3 FAIL: TURN Credential = %v, want s3cret (must reach pion unstripped)", turn.Credential)
	}
}
