// internal/federation/hub_answerer.go — #672 hub-relayed WebRTC A2A
// (epic). HubAnswerer is the inbound half of the hub-relay data path:
// the background loop that lets a no-inbound-HTTP daemon ACCEPT WebRTC
// connections through the central chepherd-hub.
//
// Flow (all requests OUTBOUND to the hub — nothing listens for inbound):
//
//	loop every ~500ms:
//	  GET <hub>/v1/signaling/pending?orgId=<me>   (drains my mailbox)
//	  for each kind=="offer" frame:
//	     pc := answerer PeerConnection
//	     answer := pc.SetRemoteOfferGathered(offer)   (bundled ICE)
//	     POST <hub>/v1/signaling/answer  {to=offerer, sessionId, answer}
//	     ServeJSONRPC(pc, handler)  → A2A message/send → local Deliverer
//
// The answer SDP carries all ICE candidates inline (non-trickle) because
// the hub's /v1/signaling/ice receiver is scaffold; the connection
// completes from offer+answer alone.
//
// The JSON-RPC handler mirrors a2a.makeSendMessageHandler /
// internal/runtimehttp's /jsonrpc path: decode SendMessageParams,
// Deliver to the local a2a.Deliverer, wrap the resulting Task in a
// JSON-RPC {result:{task}} envelope. Other A2A methods over the
// DataChannel return method-not-found (the hub-relay path carries
// message/send only in #672; tasks/get etc. still flow over the
// daemon's normal surfaces).
//
// Refs #672.
package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/webrtcrtc"
)

// hubAnswererPollInterval is the cadence at which the answerer drains
// its hub mailbox looking for inbound offer frames. 500ms keeps inbound
// connection setup sub-second without hammering the hub. Pinned, not
// slept-on, in tests (the in-process hub answers on the first poll).
const hubAnswererPollInterval = 500 * time.Millisecond

// HubAnswerer drains inbound offer frames from the hub + brings up an
// answerer PeerConnection per session, serving A2A over each. Construct
// via NewHubAnswerer; drive with Start(ctx).
type HubAnswerer struct {
	hubURL    string
	myOrgID   string
	cfg       webrtcrtc.Config
	deliverer a2a.Deliverer
	http      *http.Client

	// PollInterval overrides hubAnswererPollInterval. Zero uses the
	// default. Tests set a small value to keep the round-trip tight.
	PollInterval time.Duration

	mu   sync.Mutex
	pcs  map[string]*webrtcrtc.PeerConnection // keyed by sessionId
	done chan struct{}
}

// NewHubAnswerer constructs the answerer. httpClient may be nil (a
// 30s-timeout client is used). cfg carries the ICE servers (STUN
// defaults + hub TURN) the answerer PeerConnections gather against.
func NewHubAnswerer(hubURL, myOrgID string, cfg webrtcrtc.Config, deliverer a2a.Deliverer, httpClient *http.Client) *HubAnswerer {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &HubAnswerer{
		hubURL:    hubURL,
		myOrgID:   myOrgID,
		cfg:       cfg,
		deliverer: deliverer,
		http:      httpClient,
		pcs:       map[string]*webrtcrtc.PeerConnection{},
		done:      make(chan struct{}),
	}
}

// Start runs the poll loop until ctx is canceled. Blocking — callers
// typically `go answerer.Start(ctx)`. On ctx cancel it closes every
// live answerer PeerConnection + signals Stop waiters.
func (a *HubAnswerer) Start(ctx context.Context) {
	interval := a.PollInterval
	if interval <= 0 {
		interval = hubAnswererPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer a.closeAll()
	defer close(a.done)
	for {
		a.pollOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Wait blocks until Start has returned (ctx canceled + cleanup done).
// Used by tests + graceful shutdown to confirm the loop has drained.
func (a *HubAnswerer) Wait() { <-a.done }

// pollOnce drains the hub mailbox + handles every offer frame found.
func (a *HubAnswerer) pollOnce(ctx context.Context) {
	frames, err := webrtcrtc.HubPending(ctx, a.http, a.hubURL, a.myOrgID)
	if err != nil {
		// Transient (hub down, network blip) — next tick retries.
		fmt.Fprintf(stderrPrintf, "[hub-answerer] pending poll: %v\n", err)
		return
	}
	for _, f := range frames {
		if f.Kind != "offer" {
			// Stray ice/answer frame — the offerer is the only one that
			// consumes answers; ignore here.
			continue
		}
		if err := a.handleOffer(ctx, f); err != nil {
			fmt.Fprintf(stderrPrintf, "[hub-answerer] handle offer session=%s from=%s: %v\n",
				f.SessionID, f.FromOrgID, err)
		}
	}
}

// handleOffer brings up an answerer PeerConnection for one offer frame,
// posts the gathered answer back to the offerer via the hub, and wires
// the A2A JSON-RPC handler onto the DataChannel.
func (a *HubAnswerer) handleOffer(ctx context.Context, f webrtcrtc.HubFrame) error {
	var offer = mustSessionDescription(f.Payload)
	pc, err := webrtcrtc.NewPeerConnectionForAnswerer(a.cfg)
	if err != nil {
		return fmt.Errorf("new answerer PC: %w", err)
	}
	answer, err := pc.SetRemoteOfferGathered(offer)
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("set remote offer: %w", err)
	}
	// Wire the A2A handler BEFORE posting the answer so the DataChannel
	// is ready to serve the instant the offerer's first message arrives.
	webrtcrtc.ServeJSONRPC(pc, a.makeRPCHandler(ctx))

	payload, err := json.Marshal(answer)
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("marshal answer: %w", err)
	}
	if err := webrtcrtc.HubPostFrame(ctx, a.http, a.hubURL, a.myOrgID, "answer", webrtcrtc.HubFrame{
		FromOrgID: a.myOrgID,
		ToOrgID:   f.FromOrgID,
		SessionID: f.SessionID,
		Payload:   payload,
	}); err != nil {
		_ = pc.Close()
		return fmt.Errorf("post answer: %w", err)
	}

	a.mu.Lock()
	// Replace any stale PC for this session (re-offer on the same id).
	if old, ok := a.pcs[f.SessionID]; ok {
		_ = old.Close()
	}
	a.pcs[f.SessionID] = pc
	a.mu.Unlock()
	return nil
}

// makeRPCHandler returns the webrtcrtc.RPCHandler that processes inbound
// A2A JSON-RPC over the DataChannel. message/send (canonical
// "SendMessage", legacy "message/send") routes to the local Deliverer;
// every other method returns -32601 method-not-found.
func (a *HubAnswerer) makeRPCHandler(parent context.Context) webrtcrtc.RPCHandler {
	return func(requestJSON []byte) ([]byte, error) {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(requestJSON, &req); err != nil {
			return rpcError(json.RawMessage("null"), a2a.ErrCodeParseError, "parse JSON: "+err.Error()), nil
		}
		method := canonicalA2AMethod(req.Method)
		if method != "SendMessage" {
			return rpcError(req.ID, a2a.ErrCodeMethodNotFound,
				"hub-relay DataChannel carries message/send only; got "+req.Method), nil
		}
		var params a2a.SendMessageParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return rpcError(req.ID, a2a.ErrCodeInvalidParams, "decode SendMessageParams: "+err.Error()), nil
		}
		// Inherit the loop's lifetime but cap each delivery so a stuck
		// handler can't pin the DataChannel goroutine forever.
		delCtx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		task, err := a.deliverer.Deliver(delCtx, params.Message)
		if err != nil {
			return rpcError(req.ID, a2a.ErrCodeInternalError, "deliver: "+err.Error()), nil
		}
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  a2a.SendMessageResult{Task: task},
		}
		body, err := json.Marshal(resp)
		if err != nil {
			return rpcError(req.ID, a2a.ErrCodeInternalError, "marshal result: "+err.Error()), nil
		}
		return body, nil
	}
}

func (a *HubAnswerer) closeAll() {
	a.mu.Lock()
	pcs := a.pcs
	a.pcs = map[string]*webrtcrtc.PeerConnection{}
	a.mu.Unlock()
	for _, pc := range pcs {
		_ = pc.Close()
	}
}

// canonicalA2AMethod resolves a legacy slash-camelCase alias to its
// PascalCase form (reusing the a2a package's alias table) so the
// handler accepts both "message/send" and "SendMessage".
func canonicalA2AMethod(m string) string {
	if c, ok := a2a.MethodAliases()[m]; ok {
		return c
	}
	return m
}

// rpcError builds a JSON-RPC error envelope as bytes.
func rpcError(id json.RawMessage, code int, message string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
	return body
}

// mustSessionDescription unmarshals a frame payload into a
// webrtc.SessionDescription, returning the zero value on error (the
// caller's SetRemoteOfferGathered then fails loudly with a real SDP
// parse error).
func mustSessionDescription(payload json.RawMessage) webrtcrtc.SessionDescription {
	var sd webrtcrtc.SessionDescription
	_ = json.Unmarshal(payload, &sd)
	return sd
}
