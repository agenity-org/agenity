// internal/webrtcrtc/fingerprint.go — #494 Wave F4 DTLS fingerprint
// verification + out-of-band pinning layer.
//
// PREMISE-CHECK FINDING (#494 dispatch 2026-05-31):
// pion/webrtc/v4 ALREADY verifies DTLS cert fingerprint against the
// SDP a=fingerprint line by default. The control surface is
// SettingEngine.DisableCertificateFingerprintVerification(bool) —
// default false. So RFC 8829 spec compliance ships transparently with
// pion. F4's chepherd-specific value-add is OUT-OF-BAND fingerprint
// PINNING: the operator pre-shares a legit peer's expected cert
// fingerprint via a trusted side-channel, and chepherd rejects any
// SDP whose fingerprint doesn't match. Pinning defends against the
// SDP-signaling-compromise scenario where an attacker could swap
// both the cert AND the fingerprint in flight (both would match each
// other but be the attacker's, not the legit peer's).
//
// Wire:
//
//	a=fingerprint:sha-256 D7:53:B8:...:AB
//
// Per RFC 4572 §5 — algorithm name + lowercase hex bytes separated by
// colons. pion produces this canonical shape via Certificate.GetFingerprints.
//
// Refs #494 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 7.
package webrtcrtc

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/pion/webrtc/v4"
)

// fingerprintLineRE matches one SDP `a=fingerprint:<algo> <hex>`
// attribute. Captures algorithm + value separately so callers can
// build DTLSFingerprint instances. Algorithm is letter+digit+dash
// (sha-256, sha-1, sha-512); value is colon-separated 2-hex pairs.
var fingerprintLineRE = regexp.MustCompile(`(?im)^a=fingerprint:([A-Za-z0-9\-]+)\s+([0-9A-Fa-f:]+)\s*$`)

// ExtractFingerprints parses every `a=fingerprint:` attribute from
// the SDP body. Returns an empty slice if none (does NOT error —
// some SDPs omit the line if no media section needs DTLS).
//
// Per RFC 4572 §5 + RFC 8829 §5.1.6: an SDP may carry one fingerprint
// per media section + one session-level fingerprint; chepherd treats
// any match against the pinned set as success (consistent with pion's
// own multi-media-section handling).
func ExtractFingerprints(sdp webrtc.SessionDescription) []webrtc.DTLSFingerprint {
	matches := fingerprintLineRE.FindAllStringSubmatch(sdp.SDP, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]webrtc.DTLSFingerprint, 0, len(matches))
	seen := map[string]bool{}
	for _, m := range matches {
		algo := strings.ToLower(strings.TrimSpace(m[1]))
		val := strings.ToLower(strings.TrimSpace(m[2]))
		key := algo + ":" + val
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, webrtc.DTLSFingerprint{
			Algorithm: algo,
			Value:     val,
		})
	}
	return out
}

// NormalizeFingerprint lowercases the algorithm + value so case
// differences in operator-supplied pins don't false-mismatch a peer's
// SDP. Caller-side normalization keeps the comparison loop branch-free.
func NormalizeFingerprint(fp webrtc.DTLSFingerprint) webrtc.DTLSFingerprint {
	return webrtc.DTLSFingerprint{
		Algorithm: strings.ToLower(strings.TrimSpace(fp.Algorithm)),
		Value:     strings.ToLower(strings.TrimSpace(fp.Value)),
	}
}

// MatchesAny reports whether candidate matches at least one entry in
// pinned. Comparison is case-insensitive (NormalizeFingerprint applied
// to both sides). Empty pinned → false (caller must check
// len(pinned)==0 explicitly to mean "no pinning required").
func MatchesAny(candidate webrtc.DTLSFingerprint, pinned []webrtc.DTLSFingerprint) bool {
	c := NormalizeFingerprint(candidate)
	for _, p := range pinned {
		n := NormalizeFingerprint(p)
		if c.Algorithm == n.Algorithm && c.Value == n.Value {
			return true
		}
	}
	return false
}

// VerifyPinnedSDP returns nil iff at least one fingerprint extracted
// from sdp is present in pinned. Used by SetRemoteOffer / SetRemoteAnswer
// when Config.PinnedFingerprints is non-empty (#494 Wave F4).
//
// Returns ErrFingerprintMismatch when none match — caller MUST abort
// the negotiation + close the PeerConnection. Returns
// ErrFingerprintMissing when sdp carries no fingerprint at all (the
// SDP is structurally invalid for DTLS-SRTP; pion would fail later,
// but chepherd's pinning layer fails fast here for cleaner logs).
//
// Empty pinned is a programming error — callers gate this fn behind
// `len(cfg.PinnedFingerprints) > 0`.
func VerifyPinnedSDP(sdp webrtc.SessionDescription, pinned []webrtc.DTLSFingerprint) error {
	if len(pinned) == 0 {
		return errors.New("VerifyPinnedSDP: nil pinned set (programming error — gate this call behind len check)")
	}
	got := ExtractFingerprints(sdp)
	if len(got) == 0 {
		return ErrFingerprintMissing
	}
	for _, g := range got {
		if MatchesAny(g, pinned) {
			return nil
		}
	}
	return fmt.Errorf("%w: SDP advertised %d fingerprints; none match any of %d pinned",
		ErrFingerprintMismatch, len(got), len(pinned))
}

// ErrFingerprintMismatch is returned by VerifyPinnedSDP when none of
// the SDP's fingerprints match the pinned set. Callers detect this
// sentinel via errors.Is + close the PeerConnection.
var ErrFingerprintMismatch = errors.New("webrtc: peer SDP fingerprint not in pinned set")

// ErrFingerprintMissing is returned by VerifyPinnedSDP when the SDP
// has no `a=fingerprint:` attribute at all — invalid for DTLS-SRTP
// negotiation per RFC 8829 §5.1.6.
var ErrFingerprintMissing = errors.New("webrtc: SDP has no a=fingerprint attribute")
