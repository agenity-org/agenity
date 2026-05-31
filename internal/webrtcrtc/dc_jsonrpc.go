// internal/webrtcrtc/dc_jsonrpc.go — #492 Wave F2 JSON-RPC over WebRTC
// DataChannel. Each chepherd-runner publishes its A2A JSON-RPC handler
// over the DataChannel in addition to the HTTP /jsonrpc endpoint.
// Peers that advertise x-chepherd-p2p.supported=true on their Agent
// Card receive A2A traffic over the P2P link instead of HTTP.
//
// Wire shape: identical to the HTTP JSON-RPC envelope per A2A v1.0 spec.
// The DataChannel carries one JSON-RPC envelope per message (no
// framing — DataChannel is message-oriented, not stream-oriented in
// the WebRTC sense). Request/response correlation via the JSON-RPC
// `id` field.
//
// Refs #492 V0.9.2-ARCHITECTURE.md §20.
package webrtcrtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// RPCHandler processes one inbound JSON-RPC request envelope and
// returns the response envelope bytes. Implementations typically wrap
// an a2a.JSONRPCRouter.Dispatch invocation. The handler MUST NOT
// block beyond a reasonable handler timeout — the caller's DataChannel
// receive goroutine is the only consumer.
type RPCHandler func(requestJSON []byte) ([]byte, error)

// ServeJSONRPC attaches handler to pc's DataChannel. Every inbound
// message is passed to handler; the response is sent back over the
// same channel. Errors from handler are translated into JSON-RPC
// error responses with code -32603 (internal error) so the caller's
// awaitResponse code path always sees a parseable envelope.
//
// Safe to call exactly once per PeerConnection. Replacing an existing
// onMessage handler is intentional — the previous F1 substrate set no
// inbound handler, so this is additive without breaking F1 callers.
func ServeJSONRPC(pc *PeerConnection, handler RPCHandler) {
	pc.OnMessage(func(payload []byte) {
		resp, err := handler(payload)
		if err != nil {
			resp = jsonrpcInternalError(payload, err)
		}
		if resp == nil {
			return
		}
		if sendErr := pc.Send(resp); sendErr != nil {
			// Best-effort. We can't surface the error elsewhere — the
			// peer's DataChannel may have closed mid-response. The
			// next negotiation will reset state via PCStore.GetOrDial.
			_ = sendErr
		}
	})
}

// jsonrpcInternalError builds a JSON-RPC -32603 response envelope
// matching the inbound request's `id` so the caller's correlation
// table can pair the failure with its outbound call.
func jsonrpcInternalError(requestJSON []byte, handlerErr error) []byte {
	var req struct {
		ID      json.RawMessage `json:"id"`
		JSONRPC string          `json:"jsonrpc"`
	}
	_ = json.Unmarshal(requestJSON, &req)
	id := req.ID
	if len(id) == 0 {
		id = json.RawMessage(`null`)
	}
	envelope := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32603,
			"message": "Internal error: " + handlerErr.Error(),
		},
	}
	body, _ := json.Marshal(envelope)
	return body
}

// JSONRPCClient sends JSON-RPC envelopes over a PeerConnection's
// DataChannel + matches responses by `id` field. Safe for concurrent
// SendRPC calls — each call gets its own correlation slot.
type JSONRPCClient struct {
	pc *PeerConnection

	mu       sync.Mutex
	pending  map[string]chan []byte
	closed   bool
}

// NewJSONRPCClient wraps pc + installs the inbound message handler
// that demuxes responses by `id`. Returns the client. The caller
// owns pc's lifecycle; client.Close() detaches the handler but
// doesn't close pc.
func NewJSONRPCClient(pc *PeerConnection) *JSONRPCClient {
	c := &JSONRPCClient{
		pc:      pc,
		pending: map[string]chan []byte{},
	}
	pc.OnMessage(func(payload []byte) {
		var resp struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(payload, &resp); err != nil {
			return
		}
		key := string(resp.ID)
		c.mu.Lock()
		ch, ok := c.pending[key]
		if ok {
			delete(c.pending, key)
		}
		c.mu.Unlock()
		if !ok {
			// Response with no matching request — drop. Could be a
			// late response to a timed-out request.
			return
		}
		select {
		case ch <- payload:
		default:
		}
	})
	return c
}

// SendRPC ships request (a complete JSON-RPC request envelope as
// bytes) over the DataChannel and blocks until either the matching
// response arrives or ctx expires. Returns the response envelope
// bytes (success or error response — caller parses the body).
//
// The request MUST carry an `id` field; SendRPC reads it to match
// the inbound response. Notifications (no `id`) are rejected.
func (c *JSONRPCClient) SendRPC(ctx context.Context, request []byte) ([]byte, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("JSONRPCClient: closed")
	}
	c.mu.Unlock()
	var probe struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(request, &probe); err != nil {
		return nil, fmt.Errorf("JSONRPCClient.SendRPC: parse request id: %w", err)
	}
	if len(probe.ID) == 0 || string(probe.ID) == "null" {
		return nil, errors.New("JSONRPCClient.SendRPC: request missing id (notifications unsupported)")
	}
	key := string(probe.ID)
	ch := make(chan []byte, 1)
	c.mu.Lock()
	c.pending[key] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
	}()

	if err := c.pc.Send(request); err != nil {
		return nil, fmt.Errorf("JSONRPCClient.SendRPC: pc.Send: %w", err)
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("JSONRPCClient.SendRPC: %w", ctx.Err())
	}
}

// Close marks the client closed. Pending SendRPC calls will return
// when their context expires (ctx is the controlling lifecycle).
func (c *JSONRPCClient) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
}

// MeasuredSendRPC wraps SendRPC with a wall-clock timer + reports
// the elapsed time alongside the response. Used by the F2 latency
// live-walk to demonstrate DataChannel < HTTP baseline.
func (c *JSONRPCClient) MeasuredSendRPC(ctx context.Context, request []byte) ([]byte, time.Duration, error) {
	start := time.Now()
	resp, err := c.SendRPC(ctx, request)
	return resp, time.Since(start), err
}
