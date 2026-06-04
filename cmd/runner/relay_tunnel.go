// cmd/runner/relay_tunnel.go — #556 Wave F7.1 runner-side reverse-
// proxy tunnel client. Complements the F7 #497 hub surface
// (cmd/chepherd-hub/tunnel.go) by dialing the hub's WS endpoint and
// serving inbound proxied A2A requests via the runner's local
// http.Handler.
//
// Architecture:
//
//   chepherd-runner                              chepherd-hub
//   ───────────────                              ────────────
//   relayTunnelClient.Dial
//     ws.Dial /v1/relay/tunnel ─────────────────► handleRelayTunnel
//                                                  tunnelManager.register
//   ◄──── relayFrame{Direction:"to-runner",   ─── inbound A2A from peer
//                    method, path, headers, body}
//   route via runner's local A2A handler
//     (httptest.NewRecorder pattern reuses the
//      runner's existing JSON-RPC mux unchanged)
//   ──── relayFrame{Direction:"to-hub",         ──► hub matches reqID +
//                   status, headers, body} ────►   writes response to
//                                                   waiting caller
//
// Auth: daemon-minted JWT in Authorization: Bearer header on the
// initial WS dial (T1 #530 substrate). The hub verifies the JWT
// via the daemon's JWKS (T2 #510). Same chain as the existing
// runner→daemon WS, so the credential lifecycle and rotation
// behavior reuse R1/R5 patterns.
//
// Fallback condition: activated when ICE + TURN both fail
// (BlockedNAT detection). F7.1 ships the client; the spawn-time
// decision to enable it lives in cmd/run.go's --hub-relay-url
// flag wiring + runtime's transport-fallback chain.
//
// Refs #556 #497 V0.9.2-ARCHITECTURE.md §5 #28 §10 Pattern 4.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// relayFrame mirrors the hub-side wire format (see
// cmd/chepherd-hub/tunnel.go.relayFrame). Kept as a private copy
// here so this package doesn't import cmd/chepherd-hub (cmd/*
// packages can't import each other).
type relayFrame struct {
	RequestID string            `json:"requestId"`
	Direction string            `json:"direction"`
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Status    int               `json:"status,omitempty"`
	Body      []byte            `json:"body,omitempty"`
}

// relayTunnelClient is the runner-side tunnel connector. One
// instance per runner; lifecycle owned by main.go's Setup() chain.
type relayTunnelClient struct {
	hubURL      string
	orgID       string
	bearerToken string
	handler     http.Handler

	conn    *websocket.Conn
	writeMu sync.Mutex

	state          atomic.Int32 // 0=closed, 1=open
	totalFrames    atomic.Int64
	totalHandlerOK atomic.Int64

	closeOnce sync.Once
	done      chan struct{}
}

const (
	relayTunnelStateClosed = 0
	relayTunnelStateOpen   = 1

	relayTunnelDialTimeout = 10 * time.Second
)

// newRelayTunnelClient constructs a client without dialing. Call
// Dial to bring up the connection; Close to tear it down.
func newRelayTunnelClient(hubURL, orgID, bearerToken string, handler http.Handler) *relayTunnelClient {
	return &relayTunnelClient{
		hubURL:      hubURL,
		orgID:       orgID,
		bearerToken: bearerToken,
		handler:     handler,
		done:        make(chan struct{}),
	}
}

// Dial opens the WS connection to the hub. Returns a non-nil error
// if dial or registration fails. The read pump runs as a goroutine;
// the caller can use IsOpen()/Close() to manage lifecycle.
func (c *relayTunnelClient) Dial(ctx context.Context) error {
	if c.hubURL == "" {
		return errors.New("relayTunnelClient: empty hubURL")
	}
	if c.orgID == "" {
		return errors.New("relayTunnelClient: empty orgID")
	}
	if c.handler == nil {
		return errors.New("relayTunnelClient: nil handler")
	}
	url := strings.TrimRight(c.hubURL, "/")
	if strings.HasPrefix(url, "http://") {
		url = "ws://" + strings.TrimPrefix(url, "http://")
	} else if strings.HasPrefix(url, "https://") {
		url = "wss://" + strings.TrimPrefix(url, "https://")
	}
	url += "/v1/relay/tunnel"

	dialer := websocket.Dialer{
		HandshakeTimeout: relayTunnelDialTimeout,
		ReadBufferSize:   64 * 1024,
		WriteBufferSize:  64 * 1024,
	}
	hdr := http.Header{}
	hdr.Set("X-Chepherd-Org", c.orgID)
	if c.bearerToken != "" {
		hdr.Set("Authorization", "Bearer "+c.bearerToken)
	}
	dialCtx, cancel := context.WithTimeout(ctx, relayTunnelDialTimeout)
	defer cancel()
	conn, _, err := dialer.DialContext(dialCtx, url, hdr)
	if err != nil {
		return fmt.Errorf("relayTunnelClient: dial %s: %w", url, err)
	}
	c.conn = conn
	c.state.Store(relayTunnelStateOpen)
	go c.readPump()
	return nil
}

// IsOpen reports whether the tunnel is currently connected. Set to
// true after a successful Dial; set to false after Close or after
// the readPump observes a ReadMessage error.
func (c *relayTunnelClient) IsOpen() bool {
	return c.state.Load() == relayTunnelStateOpen
}

// TotalFrames returns the count of inbound frames processed.
// Surfaced by /healthz on the runner side (when wired in cmd/run.go).
func (c *relayTunnelClient) TotalFrames() int64 { return c.totalFrames.Load() }

// TotalHandlerOK returns the count of frames that resulted in a 2xx
// from the local handler. Operators use the diff against TotalFrames
// to spot saturation or routing errors.
func (c *relayTunnelClient) TotalHandlerOK() int64 { return c.totalHandlerOK.Load() }

// Close tears down the WS connection. Idempotent.
func (c *relayTunnelClient) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.state.Store(relayTunnelStateClosed)
		if c.conn != nil {
			err = c.conn.Close()
		}
		close(c.done)
	})
	return err
}

// Done returns a channel closed when Close is called or the read
// pump exits. Useful for callers that want to react to disconnects
// (e.g., re-dial loop).
func (c *relayTunnelClient) Done() <-chan struct{} { return c.done }

// runHubRelayTunnel is the reconnect-with-backoff loop that keeps the
// F7.1 tunnel open against a chepherd-hub for the runner's lifetime.
// Activated by main.go when --hub-relay-url is set + the A2A endpoint
// is bound. Closes the inner relayTunnelClient + retries with
// exponential backoff on every disconnect; exits cleanly when ctx
// is cancelled (SIGTERM / shutdown).
//
// Backoff starts at 1s, doubles to 30s cap. Caller-provided ctx
// short-circuits any pending sleep. #556 #585 Wave F7.1.
func runHubRelayTunnel(ctx context.Context, hubURL, orgID, bearer string, handler http.Handler) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	attempt := 0
	for {
		attempt++
		select {
		case <-ctx.Done():
			return
		default:
		}
		client := newRelayTunnelClient(hubURL, orgID, bearer, handler)
		dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
		err := client.Dial(dialCtx)
		dialCancel()
		if err != nil {
			log.Printf("[chepherd-runner] F7.1 hub-relay dial attempt %d FAILED: %v (next retry in %s)", attempt, err, backoff)
		} else {
			log.Printf("[chepherd-runner] F7.1 hub-relay tunnel up: hub=%s org=%s (attempt %d)", hubURL, orgID, attempt)
			// Reset backoff after a successful dial so the next
			// disconnect retries quickly. Block on Done() until the
			// readPump exits (network error, hub close, ctx cancel).
			backoff = time.Second
			attempt = 0
			select {
			case <-ctx.Done():
				_ = client.Close()
				return
			case <-client.Done():
				log.Printf("[chepherd-runner] F7.1 hub-relay tunnel CLOSED (frames=%d ok=%d); reconnecting", client.TotalFrames(), client.TotalHandlerOK())
			}
		}
		// Backoff before next attempt; honor ctx cancel during sleep.
		sleep := backoff
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep):
		}
	}
}

// readPump runs as a goroutine. Reads frames, dispatches to the
// runner's local A2A handler via the httptest.NewRecorder pattern
// (same trick F7 #497's hub-side jsonRPCAdapter uses), and writes
// the response back over the same WS.
func (c *relayTunnelClient) readPump() {
	defer c.Close() // route through closeOnce so done-close races stay safe
	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var frame relayFrame
		if err := json.Unmarshal(payload, &frame); err != nil {
			continue
		}
		if frame.Direction != "to-runner" {
			continue
		}
		c.totalFrames.Add(1)
		go c.handleFrame(&frame)
	}
}

// handleFrame dispatches one inbound proxied request to the
// runner's handler + sends the response back. Runs in its own
// goroutine so slow handlers don't stall the read pump.
//
// Body-blind preservation: the request body and response body are
// forwarded bytes-exact; the runner's handler is the only consumer
// that decodes them.
func (c *relayTunnelClient) handleFrame(frame *relayFrame) {
	req, err := http.NewRequest(frame.Method, frame.Path, bytes.NewReader(frame.Body))
	if err != nil {
		c.sendErrorResponse(frame.RequestID, http.StatusBadGateway,
			"build request: "+err.Error())
		return
	}
	for k, v := range frame.Headers {
		if isHopByHopHeader(k) {
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	resp := &relayFrame{
		RequestID: frame.RequestID,
		Direction: "to-hub",
		Status:    rec.Code,
		Headers:   map[string]string{},
		Body:      rec.Body.Bytes(),
	}
	for k, vs := range rec.Header() {
		if isHopByHopHeader(k) {
			continue
		}
		if len(vs) > 0 {
			resp.Headers[k] = vs[0]
		}
	}
	// #711 — bump BEFORE the send so the counter is causally consistent:
	// the requester can only observe its 2xx after these bytes leave,
	// which now happens-after the Add (the #688/#699 ordering family —
	// CI saw TotalHandlerOK=0 while the response had demonstrably
	// arrived). On a failed send the tunnel drops and reconnects, so a
	// one-frame overcount on the final failed send is moot.
	if rec.Code >= 200 && rec.Code < 300 {
		c.totalHandlerOK.Add(1)
	}
	if err := c.send(resp); err != nil {
		// Tunnel must have dropped — readPump will observe + exit.
		return
	}
}

// sendErrorResponse builds a synthetic error envelope when the
// inbound frame fails before reaching the handler. Maintains
// correlation by echoing the RequestID.
func (c *relayTunnelClient) sendErrorResponse(reqID string, status int, msg string) {
	body, _ := json.Marshal(map[string]string{"error": msg})
	_ = c.send(&relayFrame{
		RequestID: reqID,
		Direction: "to-hub",
		Status:    status,
		Headers:   map[string]string{"Content-Type": "application/json"},
		Body:      body,
	})
}

// send writes one frame to the WS under writeMu so concurrent
// handleFrame goroutines don't interleave.
func (c *relayTunnelClient) send(frame *relayFrame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteJSON(frame)
}

// isHopByHopHeader mirrors the hub-side definition (RFC 7230 §6.1
// + the well-known pseudo-set). Kept in-package so this file has
// zero cross-package dependencies beyond gorilla/websocket.
func isHopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding",
		"upgrade", "host":
		return true
	}
	return false
}
