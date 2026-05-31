// internal/a2a/p0_488_p2p_extension_test.go pins the v0.9.4 §10 +
// §20 chepherd-p2p AgentCard extension surface (#488 Wave F1).
//
// Refs #488 V0.9.2-ARCHITECTURE.md §10 §20 #208.
package a2a

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWaveF1_DefaultExtension_SupportedTrueByDefault(t *testing.T) {
	t.Parallel()
	ext := DefaultExtension()
	if !ext.Supported {
		t.Error("DefaultExtension.Supported = false, want true (runners ship with P2P wired)")
	}
	if ext.Version == "" {
		t.Error("Version empty")
	}
	if len(ext.SupportedDataChannels) == 0 || ext.SupportedDataChannels[0] != "a2a" {
		t.Errorf("SupportedDataChannels = %v, want canonical [a2a]", ext.SupportedDataChannels)
	}
	if len(ext.IceServers) != 0 {
		t.Errorf("PUBLIC DefaultExtension should have no ICE servers (auth-gated via A4); got %v", ext.IceServers)
	}
}

func TestWaveF1_PopulateSignalingEndpoint_AppendsWebRTCPath(t *testing.T) {
	t.Parallel()
	ext := DefaultExtension()
	ext.PopulateSignalingEndpoint("http://runner.example:9090")
	if ext.SignalingEndpoint != "http://runner.example:9090/webrtc/offer" {
		t.Errorf("SignalingEndpoint = %q, want trailing /webrtc/offer", ext.SignalingEndpoint)
	}
}

func TestWaveF1_PopulateSignalingEndpoint_StripsTrailingSlash(t *testing.T) {
	t.Parallel()
	ext := DefaultExtension()
	ext.PopulateSignalingEndpoint("http://runner.example:9090/")
	if ext.SignalingEndpoint != "http://runner.example:9090/webrtc/offer" {
		t.Errorf("SignalingEndpoint = %q, want trailing slash stripped", ext.SignalingEndpoint)
	}
}

func TestWaveF1_PopulateSignalingEndpoint_EmptyBaseURLNoChange(t *testing.T) {
	t.Parallel()
	ext := DefaultExtension()
	ext.PopulateSignalingEndpoint("")
	if ext.SignalingEndpoint != "" {
		t.Errorf("empty baseURL should leave SignalingEndpoint empty, got %q", ext.SignalingEndpoint)
	}
}

func TestWaveF1_PopulateSignalingEndpoint_NilExtensionSafe(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil-receiver PopulateSignalingEndpoint panicked: %v", r)
		}
	}()
	var ext *ChepherdP2PExtension
	ext.PopulateSignalingEndpoint("http://x")
}

func TestWaveF1_WithICEServers_ReturnsCopyNotMutate(t *testing.T) {
	t.Parallel()
	pub := DefaultExtension()
	pub.PopulateSignalingEndpoint("http://r:9090")
	authed := pub.WithICEServers([]IceServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
		{URLs: []string{"turn:turn.example:3478"}, Username: "u", Credential: "p"},
	})
	if pub == authed {
		t.Fatal("WithICEServers should return a new struct, not mutate")
	}
	if len(pub.IceServers) != 0 {
		t.Errorf("public ext IceServers should stay empty after WithICEServers, got %v", pub.IceServers)
	}
	if len(authed.IceServers) != 2 {
		t.Errorf("authenticated ext IceServers = %d, want 2", len(authed.IceServers))
	}
	if authed.IceServers[1].Username != "u" {
		t.Errorf("TURN credential not propagated: %+v", authed.IceServers[1])
	}
	// Other fields carry over.
	if authed.SignalingEndpoint != pub.SignalingEndpoint {
		t.Error("SignalingEndpoint not carried over to ICE-server copy")
	}
}

func TestWaveF1_ExtensionWireShape_Stable(t *testing.T) {
	t.Parallel()
	ext := DefaultExtension()
	ext.PopulateSignalingEndpoint("http://r:9090")
	body, err := json.Marshal(ext)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The JSON keys are spec-frozen. If a future change renames a
	// field this test holds the line.
	wantFragments := []string{
		`"version":"`,
		`"supported":true`,
		`"signalingEndpoint":"http://r:9090/webrtc/offer"`,
		`"supportedDataChannels":["a2a"]`,
	}
	s := string(body)
	for _, w := range wantFragments {
		if !strings.Contains(s, w) {
			t.Errorf("wire body missing %q\nbody=%s", w, s)
		}
	}
}
