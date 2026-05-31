// cmd/chepherd-hub/tunnel.go — #497 Wave F7 reverse-proxy tunnel.
// Replaces the F1 hub's /v1/relay/* 501 stubs with a real WS-tunnel-
// backed reverse-proxy: runners that can't reach peers via WebRTC
// (ICE + TURN both blocked — corporate firewall blocks UDP entirely
// AND TURN-over-tcp:443) open a persistent outbound HTTPS WebSocket
// to chepherd-hub. The hub then forwards inbound A2A traffic to the
// runner over that tunnel.
//
// PREMISE-CHECK FINDING (#497 dispatch 2026-06-01):
// pion has no tunnel/reverse-proxy helper — it's a WebRTC stack,
// not an HTTP relay. gorilla/websocket is already in chepherd's
// dependency tree (cmd/runner/daemon_ws.go uses it for the
// daemon↔runner control channel). F7 reuses gorilla/websocket on
// the hub side and the existing daemon_ws.go pattern on the runner
// side (runner-side wiring lands in a follow-up; this PR ships the
// hub surface + a smoke test exercising it via a stub runner WS).
//
// Architecture:
//
//   runner-X (org X)                  chepherd-hub
//   ────────                          ────────────
//   ws.Dial → /v1/relay/tunnel ──────► tunnelManager.register(orgX, conn)
//                                      stores in map[orgID]→tunnel
//   ◄──── relayFrame{reqID, body} ──── inbound A2A → handleRelayInbound
//                                      lookup orgID's tunnel + push frame
//   ──── relayFrame{reqID, body} ────► tunnel.send(response frame)
//                                      hub matches reqID + writes HTTP response
//
// Body-blind invariant (per V0.9.2-ARCH §10 Pattern 4):
//   Bodies traverse the tunnel opaquely (DTLS-or-mTLS wrapping
//   intact from runner perspective; hub never decodes). Hub stores
//   nothing past the request lifecycle.
//
// Refs #497 V0.9.2-ARCHITECTURE.md §5 #28 §10 Pattern 4.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// relayRequestTimeout caps how long the hub waits for a runner to
// respond over the tunnel before timing out the inbound request.
// Matches the F5 signaling-frame TTL (10min) but capped much
// shorter because reverse-proxy requests are interactive (operator
// is waiting). 30s strikes a balance between slow runners and
// hung tunnels.
const relayRequestTimeout = 30 * time.Second

// relayFrame is the wire payload between hub and runner over the
// tunnel. Bidirectional:
//
//   - Hub → runner: forward inbound HTTP request to the runner;
//     RequestID = correlation key for matching the response
//   - Runner → hub: response to a previously-forwarded request
//
// Body is opaque bytes (the hub MUST NOT decode it). Path, Method,
// Headers carry HTTP routing info so the runner can route through
// its own mux as if the request came in directly.
type relayFrame struct {
	RequestID string            `json:"requestId"`
	Direction string            `json:"direction"` // "to-runner" | "to-hub"
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Status    int               `json:"status,omitempty"`
	Body      []byte            `json:"body,omitempty"`
}

// tunnel is one runner's open WS connection. Safe for concurrent
// use — every Write goes through writeMu so frames don't interleave.
type tunnel struct {
	orgID string
	conn  *websocket.Conn

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]chan *relayFrame
	closed  bool
	openedAt time.Time
}

func (t *tunnel) send(frame *relayFrame) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.conn.WriteJSON(frame)
}

// awaitResponse registers a correlation slot + waits for the
// matching response frame (or timeout). The runner side replies
// with the same RequestID so the lookup succeeds.
func (t *tunnel) awaitResponse(ctx context.Context, reqID string) (*relayFrame, error) {
	ch := make(chan *relayFrame, 1)
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errors.New("tunnel closed")
	}
	t.pending[reqID] = ch
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.pending, reqID)
		t.mu.Unlock()
	}()
	select {
	case f := <-ch:
		return f, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// dispatch is the read pump's hand-off: an incoming response frame
// finds its waiter via RequestID. Frames with no waiter (unsolicited
// or post-timeout) are dropped silently.
func (t *tunnel) dispatch(frame *relayFrame) {
	t.mu.Lock()
	ch, ok := t.pending[frame.RequestID]
	t.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- frame:
	default:
	}
}

// close shuts the tunnel + signals all pending awaiters. Idempotent.
func (t *tunnel) close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	pending := t.pending
	t.pending = nil
	t.mu.Unlock()
	_ = t.conn.Close()
	for _, ch := range pending {
		close(ch)
	}
}

// ─── tunnelManager ─────────────────────────────────────────────────

// tunnelManager owns the per-org tunnel registry. Atomically
// thread-safe — register and lookup happen concurrently with the
// reverse-proxy request handler.
type tunnelManager struct {
	mu sync.Mutex
	// One tunnel per orgID for v0.9.4. Multi-tunnel-per-org load-
	// balancing lands in a follow-up; today the second register
	// for the same orgID evicts the first (so a runner that
	// reconnects after a network blip replaces its stale slot).
	tunnels map[string]*tunnel
	total   atomic.Int64
}

func newTunnelManager() *tunnelManager {
	return &tunnelManager{tunnels: map[string]*tunnel{}}
}

func (m *tunnelManager) register(orgID string, conn *websocket.Conn) *tunnel {
	t := &tunnel{
		orgID:    orgID,
		conn:     conn,
		pending:  map[string]chan *relayFrame{},
		openedAt: time.Now().UTC(),
	}
	m.mu.Lock()
	if existing, ok := m.tunnels[orgID]; ok {
		existing.close()
	}
	m.tunnels[orgID] = t
	m.mu.Unlock()
	m.total.Add(1)
	return t
}

func (m *tunnelManager) deregister(orgID string, t *tunnel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.tunnels[orgID]; ok && cur == t {
		delete(m.tunnels, orgID)
	}
}

func (m *tunnelManager) lookup(orgID string) *tunnel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tunnels[orgID]
}

func (m *tunnelManager) active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tunnels)
}

func (m *tunnelManager) closeAll() {
	m.mu.Lock()
	tunnels := m.tunnels
	m.tunnels = map[string]*tunnel{}
	m.mu.Unlock()
	for _, t := range tunnels {
		t.close()
	}
}

// ─── HTTP handlers ────────────────────────────────────────────────

// relayUpgrader is the gorilla/websocket Upgrader used to accept
// runner-initiated tunnel connections. CheckOrigin permissive
// because the auth gate is the X-Chepherd-Org header (mTLS in
// production) — origin spoofing doesn't grant access.
var relayUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(_ *http.Request) bool { return true },
}

// handleRelayTunnel accepts the runner-initiated WS connection.
// The runner side dials with its org identity (X-Chepherd-Org
// header or mTLS cert subject). The hub upgrades + registers the
// tunnel + spawns a read pump that demuxes inbound response frames
// into the awaitResponse waiters.
func (s *server) handleRelayTunnel(w http.ResponseWriter, r *http.Request) {
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
	conn, err := relayUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// gorilla's Upgrade already wrote the HTTP error.
		return
	}
	t := s.tunnels.register(authOrg, conn)
	defer func() {
		s.tunnels.deregister(authOrg, t)
		t.close()
	}()
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var frame relayFrame
		if err := json.Unmarshal(payload, &frame); err != nil {
			continue
		}
		// Direction enforcement: tunnel-side messages MUST be
		// "to-hub" responses; "to-runner" frames flowing the
		// wrong way are protocol violations and dropped.
		if frame.Direction != "to-hub" {
			continue
		}
		t.dispatch(&frame)
	}
}

// handleRelayInbound is the reverse-proxy HTTP handler. Inbound
// A2A traffic addressed to a tunneled org gets forwarded via the
// tunnel + the runner's response is mirrored back to the original
// caller. Body-blind: hub never decodes the payload.
//
// URL shape: /v1/relay/{orgID}/{path...}
// Method + Body + Headers (filtered) forwarded as-is.
func (s *server) handleRelayInbound(w http.ResponseWriter, r *http.Request) {
	// Tunnel control path lives at /v1/relay/tunnel — handled
	// separately by handleRelayTunnel mounted distinctly. Everything
	// else under /v1/relay/* is a reverse-proxy candidate.
	if r.URL.Path == "/v1/relay/tunnel" {
		s.handleRelayTunnel(w, r)
		return
	}
	// Caller MUST authenticate — defends against an unauthenticated
	// attacker tunnelling garbage into a victim org's runner.
	callerOrg := authenticatedOrg(r)
	if callerOrg == "" {
		writeJSON(w, http.StatusUnauthorized,
			map[string]string{"error": "no authenticated org identity"})
		return
	}
	if !s.orgAllowed(callerOrg) {
		writeJSON(w, http.StatusForbidden,
			map[string]string{"error": "caller org not in allowlist"})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/relay/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 1 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "path must be /v1/relay/{orgID}/{path}"})
		return
	}
	targetOrg := parts[0]
	if !s.orgAllowed(targetOrg) {
		writeJSON(w, http.StatusForbidden,
			map[string]string{"error": "target org not in allowlist"})
		return
	}
	t := s.tunnels.lookup(targetOrg)
	if t == nil {
		writeJSON(w, http.StatusBadGateway,
			map[string]string{"error": "no tunnel for org", "org": targetOrg})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "read body: " + err.Error()})
		return
	}
	subPath := "/"
	if len(parts) == 2 {
		subPath = "/" + parts[1]
	}
	headers := map[string]string{}
	for k, v := range r.Header {
		// Strip hop-by-hop headers that the runner side would
		// otherwise see twice. The Connection / Upgrade / Host
		// headers belong to the hub-runner WS, not the proxied req.
		if isHopByHop(k) {
			continue
		}
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	frame := &relayFrame{
		RequestID: uuid.NewString(),
		Direction: "to-runner",
		Method:    r.Method,
		Path:      subPath,
		Headers:   headers,
		Body:      body,
	}
	if err := t.send(frame); err != nil {
		writeJSON(w, http.StatusBadGateway,
			map[string]string{"error": "tunnel send: " + err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), relayRequestTimeout)
	defer cancel()
	resp, err := t.awaitResponse(ctx, frame.RequestID)
	if err != nil {
		writeJSON(w, http.StatusGatewayTimeout,
			map[string]string{"error": "tunnel response: " + err.Error()})
		return
	}
	for k, v := range resp.Headers {
		if isHopByHop(k) {
			continue
		}
		w.Header().Set(k, v)
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(resp.Body)
}

// isHopByHop reports whether header name is hop-by-hop per RFC 7230
// §6.1 + the well-known Upgrade pseudo-set. Used to strip headers
// that would otherwise be wrongly propagated across the tunnel.
func isHopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding",
		"upgrade", "host":
		return true
	}
	return false
}

// ─── healthz extension ────────────────────────────────────────────

func (s *server) tunnelsStatus() map[string]any {
	if s.tunnels == nil {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled":        true,
		"active":         s.tunnels.active(),
		"total_lifetime": s.tunnels.total.Load(),
	}
}
