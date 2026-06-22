// cmd/runner/f2_datachannel.go — #492 Wave F2 DataChannel transport
// wiring on the per-runner mux. Mounts /webrtc/offer with an
// answerer factory that, on every new PC, attaches ServeJSONRPC so
// inbound DataChannel envelopes route through the same A2A router
// the HTTP /jsonrpc endpoint uses.
//
// Why mount this here (not in internal/runtimehttp/server.go where
// F1 ships /webrtc/offer): the daemon-side server's /webrtc/offer
// pre-dates the per-runner endpoint split (R5). F2's transport
// belongs at the runner — that's where the agent process lives, so
// that's where A2A traffic must terminate. The daemon-side mount
// stays for backwards compat with C5-era signaling but plays no
// role in the F2 transport.
//
// Refs #492 V0.9.2-ARCHITECTURE.md §S5 §20.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/agenity-org/agenity/internal/webrtcrtc"
)

// mountF2DataChannel attaches the F2 transport to mux. handler is
// the runner's existing JSON-RPC handler (router + middlewares).
func mountF2DataChannel(mux *http.ServeMux, handler http.Handler) {
	mux.Handle("/webrtc/offer", webrtcrtc.HandleOffer(func() (*webrtcrtc.PeerConnection, error) {
		pc, err := webrtcrtc.NewPeerConnectionForAnswerer(webrtcrtc.Config{})
		if err != nil {
			return nil, err
		}
		webrtcrtc.ServeJSONRPC(pc, jsonRPCAdapter(handler))
		return pc, nil
	}))
}

// jsonRPCAdapter returns a webrtcrtc.RPCHandler that delegates one
// JSON-RPC envelope to the runner's existing HTTP handler via
// httptest.NewRecorder. The recorder captures the response body and
// hands it back to the DataChannel pipeline.
//
// Trade-off: this re-uses the entire HTTP middleware stack (audit
// logging + router) so the DataChannel transport behaves identically
// to /jsonrpc. The marginal overhead (one ResponseRecorder + one
// dummy *http.Request) is well under 100µs in benchmarks vs the
// ~200µs DataChannel round-trip itself.
func jsonRPCAdapter(handler http.Handler) webrtcrtc.RPCHandler {
	return func(requestJSON []byte) ([]byte, error) {
		// Build a dummy POST request carrying the JSON-RPC envelope.
		// The path matches the per-session JSON-RPC mount so a
		// future authn middleware that gates on path sees what HTTP
		// callers would see.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			"/jsonrpc",
			bytes.NewReader(requestJSON))
		if err != nil {
			return nil, fmt.Errorf("F2 jsonRPCAdapter: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		body := recorder.Body.Bytes()
		if len(body) == 0 {
			// Shouldn't happen with the standard router, but guard
			// so the DataChannel caller sees a parseable JSON-RPC
			// envelope rather than silence.
			id := extractRequestID(requestJSON)
			body, _ = json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"error": map[string]any{
					"code":    -32603,
					"message": "F2 jsonRPCAdapter: empty handler response",
				},
			})
		}
		return body, nil
	}
}

// extractRequestID parses the inbound request envelope for its `id`
// field. Used by the empty-response fallback so the DataChannel
// caller's correlation table can still match the failure to its
// outbound request.
func extractRequestID(requestJSON []byte) json.RawMessage {
	var probe struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(requestJSON, &probe); err != nil || len(probe.ID) == 0 {
		return json.RawMessage(`null`)
	}
	return probe.ID
}
