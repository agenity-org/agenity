// internal/webrtcrtc/ice_config.go — #493 Wave F3.
//
// Per V0.9.2-ARCHITECTURE.md §20: WebRTC peers need ICE candidate
// gathering to establish DataChannel across NAT. F1 #491 shipped
// host candidates only (LAN works). F3 adds:
//
//   - STUN srflx (server-reflexive) candidates from operator-
//     configured STUN servers — surfaces this peer's public NAT-
//     mapped address so the remote peer can dial it
//   - TURN allocate (relay) candidates from operator-configured TURN
//     servers — fallback path when both peers are behind symmetric
//     NATs that block direct srflx connections
//
// Flag shapes (parsed by ParseSTUNFlag / ParseTURNFlag):
//
//	--stun-server addr:port              (multi-value; repeat)
//	--turn-server addr:port:user:pass    (multi-value; repeat)
//
// Production deployments populate these via the F5 #495 chepherd.
// org-relay (which auto-templates the daemon's known STUN/TURN
// endpoints into runner spawn args). For local dev + LAN tests,
// leave the flags empty — DefaultICEServers() public-Google STUN
// kicks in via F1's fallback.
//
// Refs #493 #491 #495 V0.9.2-ARCHITECTURE.md §20.
package webrtcrtc

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pion/webrtc/v4"
)

// ParseSTUNFlag converts a `--stun-server addr:port` flag value into
// a webrtc.ICEServer with the canonical `stun:<addr>:<port>` URL.
// Repeat flags are parsed individually; the caller concatenates the
// resulting slices.
//
// Accepts both bare host:port AND fully-qualified stun:host:port
// (so operators don't need to remember the scheme prefix).
func ParseSTUNFlag(raw string) (webrtc.ICEServer, error) {
	if raw == "" {
		return webrtc.ICEServer{}, errors.New("ParseSTUNFlag: empty value")
	}
	url := raw
	if !strings.HasPrefix(url, "stun:") && !strings.HasPrefix(url, "stuns:") {
		url = "stun:" + url
	}
	// Sanity: must have a colon between host + port.
	rest := strings.TrimPrefix(strings.TrimPrefix(url, "stuns:"), "stun:")
	if !strings.Contains(rest, ":") {
		return webrtc.ICEServer{}, fmt.Errorf("ParseSTUNFlag: %q missing :port", raw)
	}
	return webrtc.ICEServer{URLs: []string{url}}, nil
}

// ParseTURNFlag converts a `--turn-server addr:port:user:pass` flag
// value into a webrtc.ICEServer. Accepts both bare addr:port:user:
// pass AND fully-qualified turn:addr:port:user:pass.
//
// Username + Credential are required for TURN allocate; this parser
// rejects values that don't supply them (vs leaving them blank — a
// missing TURN credential would silently downgrade to STUN and
// surprise the operator).
func ParseTURNFlag(raw string) (webrtc.ICEServer, error) {
	if raw == "" {
		return webrtc.ICEServer{}, errors.New("ParseTURNFlag: empty value")
	}
	// Strip optional scheme.
	stripped := strings.TrimPrefix(strings.TrimPrefix(raw, "turns:"), "turn:")
	scheme := "turn"
	if strings.HasPrefix(raw, "turns:") {
		scheme = "turns"
	}
	parts := strings.SplitN(stripped, ":", 4)
	if len(parts) != 4 {
		return webrtc.ICEServer{}, fmt.Errorf("ParseTURNFlag: %q must be addr:port:user:pass (4 colon-separated fields, got %d)", raw, len(parts))
	}
	addr, port, user, pass := parts[0], parts[1], parts[2], parts[3]
	if addr == "" || port == "" {
		return webrtc.ICEServer{}, fmt.Errorf("ParseTURNFlag: %q empty addr or port", raw)
	}
	if user == "" || pass == "" {
		return webrtc.ICEServer{}, fmt.Errorf("ParseTURNFlag: %q empty user or pass (TURN requires credentials; bare TURN without auth is a security hole — use --stun-server instead)", raw)
	}
	return webrtc.ICEServer{
		URLs:       []string{fmt.Sprintf("%s:%s:%s", scheme, addr, port)},
		Username:   user,
		Credential: pass,
	}, nil
}

// ParseICEServers parses both flag slices into a combined
// []webrtc.ICEServer suitable for Config.ICEServers. nil/empty
// slices produce an empty result (caller falls through to
// DefaultICEServers via NewPeerConnection's existing path).
//
// First-fail semantics: any parse error short-circuits + returns
// the offending flag so the operator sees the exact value to fix.
func ParseICEServers(stunFlags, turnFlags []string) ([]webrtc.ICEServer, error) {
	out := make([]webrtc.ICEServer, 0, len(stunFlags)+len(turnFlags))
	for _, raw := range stunFlags {
		s, err := ParseSTUNFlag(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	for _, raw := range turnFlags {
		s, err := ParseTURNFlag(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}
