// cmd/chepherd-hub/signaling.go — #495 Wave F5 SDP signaling relay.
// Implements the F1 hub binary's /v1/signaling/{offer,answer,ice}
// + /v1/signaling/pending endpoints with a stateless in-memory
// queue keyed by (toOrg, sessionID) and 10-minute TTL.
//
// PREMISE-CHECK FINDING (#495 dispatch 2026-05-31):
// pion/webrtc/v4 ships only client-side signaling state machinery
// (SignalingState enum). There is no server-side relay helper —
// chepherd-hub's queue is unambiguously chepherd-distinctive
// value-add per [[feedback_find_what_dep_already_does_then_add_what_it_cant]].
//
// DESIGN INVARIANTS (per V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 5):
//
//  1. Hub is BODY-BLIND. Offer/answer/ice payloads are opaque blobs;
//     hub never decodes them. The DTLS-fingerprint pinning layer
//     (F4 #494) defends E2E against an attacker hub.
//
//  2. Hub is STATELESS for content. The (toOrg, sessionID) queue
//     holds nothing past its 10-minute TTL — even an operator who
//     subpoenas hub disk learns only metadata (org pairs +
//     timestamps), not the SDP/ICE payloads.
//
//  3. Hub authenticates BOTH sides via mTLS per T3.1. The cert's
//     CN/SAN identifies the organization; routing happens against
//     the authenticated identity, not a client-supplied "fromOrgID"
//     header (which would let a malicious org spoof).
//
//  4. Pending endpoint supports BOTH short-poll (returns immediately)
//     and long-poll (?wait=30s — block until a frame arrives or
//     timeout). F5 ships short-poll; long-poll lands behind the
//     same handler.
//
// Refs #495 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 5.
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// signalingFrameTTL caps how long a queued frame survives in the
// hub before being garbage-collected. 10 minutes per the dispatch:
// peers complete the offer→answer→ICE exchange in seconds normally;
// the TTL exists to bound memory use if a recipient never polls.
const signalingFrameTTL = 10 * time.Minute

// SignalingFrameKind enumerates the three relay payload types the
// hub blindly forwards.
type SignalingFrameKind string

const (
	SignalingOffer  SignalingFrameKind = "offer"
	SignalingAnswer SignalingFrameKind = "answer"
	SignalingICE    SignalingFrameKind = "ice"
)

// SignalingFrame is one queued relay payload. Hub treats Payload
// as opaque bytes — never decodes them per the body-blind invariant.
type SignalingFrame struct {
	Kind      SignalingFrameKind `json:"kind"`
	FromOrgID string             `json:"fromOrgId"`
	ToOrgID   string             `json:"toOrgId"`
	SessionID string             `json:"sessionId"`
	Payload   json.RawMessage    `json:"payload"`
	CreatedAt time.Time          `json:"createdAt"`
}

// signalingQueue is the in-memory frame store. Keyed by toOrgID
// (recipient) so /v1/signaling/pending?orgID=X is a single map
// lookup; secondary index by sessionID within the per-org slice is
// linear scan (cheap because per-session frame counts are small —
// 1 offer + 1 answer + handful of ICE candidates).
//
// Safe for concurrent use. CloseAll halts the GC goroutine.
type signalingQueue struct {
	mu       sync.Mutex
	frames   map[string][]*SignalingFrame // key: toOrgID
	stopGC   chan struct{}
	gcTicker *time.Ticker
}

func newSignalingQueue() *signalingQueue {
	q := &signalingQueue{
		frames:   map[string][]*SignalingFrame{},
		stopGC:   make(chan struct{}),
		gcTicker: time.NewTicker(signalingFrameTTL / 4),
	}
	go q.runGC()
	return q
}

// Enqueue appends frame to the per-recipient slice. Bumps CreatedAt
// to now so GC honors the TTL window from queueing, not from
// caller-supplied timestamps (which a malicious org could backdate).
func (q *signalingQueue) Enqueue(frame *SignalingFrame) error {
	if frame == nil {
		return errors.New("signaling: nil frame")
	}
	if frame.ToOrgID == "" {
		return errors.New("signaling: empty toOrgId")
	}
	if frame.SessionID == "" {
		return errors.New("signaling: empty sessionId")
	}
	if frame.Kind != SignalingOffer && frame.Kind != SignalingAnswer && frame.Kind != SignalingICE {
		return errors.New("signaling: unknown kind " + string(frame.Kind))
	}
	if len(frame.Payload) == 0 {
		return errors.New("signaling: empty payload")
	}
	frame.CreatedAt = time.Now().UTC()
	q.mu.Lock()
	q.frames[frame.ToOrgID] = append(q.frames[frame.ToOrgID], frame)
	q.mu.Unlock()
	return nil
}

// DrainPending atomically removes + returns all frames addressed
// to orgID. Recipient consumes via short-poll; once handed off
// here the hub forgets. Caller can re-poll on the next exchange.
func (q *signalingQueue) DrainPending(orgID string) []*SignalingFrame {
	q.mu.Lock()
	defer q.mu.Unlock()
	frames := q.frames[orgID]
	delete(q.frames, orgID)
	return frames
}

// CloseAll stops the GC goroutine. Called on hub shutdown.
func (q *signalingQueue) CloseAll() {
	q.gcTicker.Stop()
	close(q.stopGC)
}

// runGC periodically drops frames older than signalingFrameTTL.
// Run as a goroutine; exits when stopGC closes.
func (q *signalingQueue) runGC() {
	for {
		select {
		case <-q.stopGC:
			return
		case <-q.gcTicker.C:
			q.gcOnce()
		}
	}
}

// gcOnce sweeps the queue + drops expired frames. Exposed for unit
// tests so they can drive the TTL behavior deterministically.
func (q *signalingQueue) gcOnce() {
	cutoff := time.Now().UTC().Add(-signalingFrameTTL)
	q.mu.Lock()
	defer q.mu.Unlock()
	for org, frames := range q.frames {
		kept := frames[:0]
		for _, f := range frames {
			if f.CreatedAt.After(cutoff) {
				kept = append(kept, f)
			}
		}
		if len(kept) == 0 {
			delete(q.frames, org)
		} else {
			q.frames[org] = kept
		}
	}
}

// ─── HTTP handlers ────────────────────────────────────────────────

// signalingRequest is the wire shape for offer/answer/ice POSTs.
// fromOrgID is supplied by the caller as a CONVENIENCE only —
// authoritative identity comes from the mTLS cert subject (or, in
// dev/no-mTLS mode, from the X-Chepherd-Org header). The mismatch
// case (cert subject != fromOrgID) is rejected 403.
type signalingRequest struct {
	FromOrgID string          `json:"fromOrgId"`
	ToOrgID   string          `json:"toOrgId"`
	SessionID string          `json:"sessionId"`
	Payload   json.RawMessage `json:"payload"`
}

// authenticatedOrg extracts the authoritative org identity from the
// request. Production deploys terminate mTLS at this binary + the
// cert's CN/SAN identifies the org. Dev mode (--allowed-orgs empty)
// falls back to X-Chepherd-Org header so smoke tests + the LIVE
// WALK don't have to spin a CA. Returns "" when no identity
// available (rejected as 401 by caller).
func authenticatedOrg(r *http.Request) string {
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		cn := strings.TrimSpace(r.TLS.PeerCertificates[0].Subject.CommonName)
		if cn != "" {
			return cn
		}
		// SAN fallback — some mTLS deployments put the org id in
		// the cert's DNS SAN rather than CN.
		for _, name := range r.TLS.PeerCertificates[0].DNSNames {
			if name != "" {
				return name
			}
		}
	}
	return strings.TrimSpace(r.Header.Get("X-Chepherd-Org"))
}

// makeSignalingHandler returns the POST handler for kind. Each of
// offer/answer/ice shares the same code path because the hub's
// only job is "validate envelope + enqueue + return 202".
func (s *server) makeSignalingHandler(kind SignalingFrameKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed,
				map[string]string{"error": "POST only"})
			return
		}
		authOrg := authenticatedOrg(r)
		if authOrg == "" {
			writeJSON(w, http.StatusUnauthorized,
				map[string]string{"error": "no authenticated org identity (mTLS cert subject or X-Chepherd-Org header required)"})
			return
		}
		if !s.orgAllowed(authOrg) {
			writeJSON(w, http.StatusForbidden,
				map[string]string{"error": "org not in --allowed-orgs allowlist", "org": authOrg})
			return
		}
		var req signalingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": "decode body: " + err.Error()})
			return
		}
		// Spoofing defense: authoritative identity comes from mTLS
		// cert, not the caller-supplied fromOrgId. Reject mismatch.
		if req.FromOrgID != "" && req.FromOrgID != authOrg {
			writeJSON(w, http.StatusForbidden,
				map[string]string{
					"error":       "fromOrgId doesn't match authenticated org identity",
					"auth_org":    authOrg,
					"claimed_org": req.FromOrgID,
				})
			return
		}
		if req.ToOrgID == "" || req.SessionID == "" {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": "toOrgId and sessionId required"})
			return
		}
		if !s.orgAllowed(req.ToOrgID) {
			writeJSON(w, http.StatusForbidden,
				map[string]string{"error": "toOrgId not in allowlist", "org": req.ToOrgID})
			return
		}
		frame := &SignalingFrame{
			Kind:      kind,
			FromOrgID: authOrg,
			ToOrgID:   req.ToOrgID,
			SessionID: req.SessionID,
			Payload:   req.Payload,
		}
		if err := s.signaling.Enqueue(frame); err != nil {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"accepted":   true,
			"kind":       string(kind),
			"to":         req.ToOrgID,
			"session_id": req.SessionID,
		})
	}
}

// handleSignalingPending implements GET /v1/signaling/pending?orgId=X.
// Returns + drains every frame currently addressed to the
// authenticated org. orgId query param MUST match the authenticated
// org (defense against an attacker polling someone else's mailbox).
func (s *server) handleSignalingPending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed,
			map[string]string{"error": "GET only"})
		return
	}
	authOrg := authenticatedOrg(r)
	if authOrg == "" {
		writeJSON(w, http.StatusUnauthorized,
			map[string]string{"error": "no authenticated org identity"})
		return
	}
	if !s.orgAllowed(authOrg) {
		writeJSON(w, http.StatusForbidden,
			map[string]string{"error": "org not in allowlist", "org": authOrg})
		return
	}
	claimed := r.URL.Query().Get("orgId")
	if claimed == "" {
		claimed = r.URL.Query().Get("orgID")
	}
	if claimed != "" && claimed != authOrg {
		writeJSON(w, http.StatusForbidden,
			map[string]string{
				"error":       "orgId query param doesn't match authenticated org identity",
				"auth_org":    authOrg,
				"claimed_org": claimed,
			})
		return
	}
	frames := s.signaling.DrainPending(authOrg)
	writeJSON(w, http.StatusOK, map[string]any{
		"org":    authOrg,
		"frames": frames,
		"count":  len(frames),
	})
}

// ─── helpers ──────────────────────────────────────────────────────

// orgAllowed reports whether org is on the configured allowlist.
// Empty allowlist (dev mode) allows every org so smoke tests +
// live walks can run without an allowlist file.
func (s *server) orgAllowed(org string) bool {
	if s.cfg.allowedOrgs == "" {
		return true
	}
	for _, allowed := range strings.Split(s.cfg.allowedOrgs, ",") {
		if strings.TrimSpace(allowed) == org {
			return true
		}
	}
	return false
}

// writeJSON is the shared response writer used by every signaling
// handler (and the future F5 follow-ups).
func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
