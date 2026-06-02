// internal/federation/hub_deliverer.go — #672 hub-relayed WebRTC A2A
// (epic). HubDeliverer is the OUTBOUND selection seam: it wraps a
// fallback a2a.Deliverer (typically the HTTP FederatedDeliverer) and
// diverts delivery to a hub-relayed WebRTC DataChannel when the target
// peer is hub-only.
//
// Selection logic (explicit + tested):
//
//   - ContextID must be `@<peerSID>/<rest>` (the same federation routing
//     prefix FederatedDeliverer uses). Non-peer contextIDs and our own
//     SID delegate straight to the fallback.
//   - Look the peer SID up in the AgentCardRepository. If its card's
//     `url` field is of the form `hub://<org>` (a peer that exposes NO
//     directly-dialable HTTP endpoint), route over the hub:
//     PCStore.GetOrDial("hub://<org>") → JSONRPCClient.SendRPC(message/send)
//   - Otherwise (a normal http(s):// url, or card miss) delegate to the
//     fallback deliverer which does the HTTP /jsonrpc forward.
//
// The PCStore handed in MUST be constructed with a webrtcrtc.HubSignaler
// + GatherBeforeOffer=true (see cmd/run.go) so dials go through the hub
// with bundled ICE. HubDeliverer never touches the signaler directly —
// it only keys GetOrDial by `hub://<org>`.
//
// Refs #672.
package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/webrtcrtc"
)

// HubDeliverer routes outbound A2A over a hub-relayed WebRTC DataChannel
// for hub-only peers, delegating everything else to Fallback. Implements
// a2a.Deliverer.
type HubDeliverer struct {
	// Fallback handles local delivery + HTTP-reachable peer forwards.
	// Typically a *FederatedDeliverer.
	Fallback a2a.Deliverer
	// Cards resolves peer SIDs to their agent cards (to read the `url`).
	Cards persistence.AgentCardRepository
	// PCStore dials hub peers (keyed by `hub://<org>`). MUST be built
	// with a HubSignaler + GatherBeforeOffer=true.
	PCStore *webrtcrtc.PCStore
	// SelfSID is this daemon's instance UUID; a contextID naming our own
	// SID delegates to Fallback (local delivery).
	SelfSID string
	// DialTimeout caps the hub WebRTC dial. Default 12s when zero (hub
	// round-trip + ICE gather is slower than a direct HTTP forward).
	DialTimeout time.Duration
	// SendTimeout caps the JSON-RPC SendRPC over the DataChannel.
	// Default 15s when zero.
	SendTimeout time.Duration
}

// compile-time assertion that HubDeliverer satisfies a2a.Deliverer.
var _ a2a.Deliverer = (*HubDeliverer)(nil)

// Deliver routes msg over the hub when the target peer is hub-only,
// else delegates to Fallback.
func (d *HubDeliverer) Deliver(ctx context.Context, msg a2a.Message) (*a2a.Task, error) {
	sid, rest, ok := parsePeerContextID(msg.ContextID)
	if !ok || sid == d.SelfSID {
		return d.Fallback.Deliver(ctx, msg)
	}
	hubOrg, isHub := d.hubPeerOrg(ctx, sid)
	if !isHub {
		return d.Fallback.Deliver(ctx, msg)
	}
	// Hub-relayed path. Strip the @<peer>/ prefix so the peer sees its
	// own bare session id (mirrors FederatedDeliverer.Deliver).
	forwarded := msg
	forwarded.ContextID = rest
	task, err := d.deliverOverHub(ctx, hubOrg, forwarded)
	if err != nil {
		return failedTask(msg, "hub-relay to "+hubOrg+": "+err.Error()), err
	}
	// Restore the @<peer>/ prefix so downstream pollers keep a stable
	// handle (mirrors FederatedDeliverer).
	if task != nil && task.ContextID != "" {
		task.ContextID = "@" + sid + "/" + task.ContextID
	}
	return task, nil
}

// hubPeerOrg returns (org, true) when the peer SID's agent card carries a
// `hub://<org>` url (a hub-only peer). Returns ("", false) for HTTP peers
// or card misses so the caller delegates to the HTTP fallback.
func (d *HubDeliverer) hubPeerOrg(ctx context.Context, sid string) (string, bool) {
	if d.Cards == nil {
		return "", false
	}
	card, err := d.Cards.Get(ctx, sid)
	if err != nil || card == nil {
		return "", false
	}
	url, err := extractPeerURL(card)
	if err != nil {
		return "", false
	}
	if !strings.HasPrefix(url, webrtcrtc.HubPeerScheme) {
		return "", false
	}
	org := strings.Trim(strings.TrimPrefix(url, webrtcrtc.HubPeerScheme), "/")
	if org == "" {
		return "", false
	}
	return org, true
}

// deliverOverHub dials the hub peer, ships a message/send JSON-RPC over
// the DataChannel, and returns the embedded Task.
func (d *HubDeliverer) deliverOverHub(ctx context.Context, hubOrg string, msg a2a.Message) (*a2a.Task, error) {
	if d.PCStore == nil {
		return nil, errors.New("HubDeliverer: nil PCStore")
	}
	dialTimeout := d.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 12 * time.Second
	}
	pc, err := d.PCStore.GetOrDial(ctx, webrtcrtc.HubPeerScheme+hubOrg, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	client := webrtcrtc.NewJSONRPCClient(pc)
	defer client.Close()

	envelope := map[string]any{
		"jsonrpc": "2.0",
		"id":      "hub-" + msg.MessageID,
		"method":  "message/send",
		"params":  map[string]any{"message": msg},
	}
	reqBytes, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	sendTimeout := d.SendTimeout
	if sendTimeout <= 0 {
		sendTimeout = 15 * time.Second
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	respBytes, err := client.SendRPC(sendCtx, reqBytes)
	if err != nil {
		return nil, fmt.Errorf("SendRPC: %w", err)
	}
	var rpc struct {
		Result *struct {
			Task *a2a.Task `json:"task"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBytes, &rpc); err != nil {
		return nil, fmt.Errorf("decode peer response: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("peer rpc error %d: %s", rpc.Error.Code, rpc.Error.Message)
	}
	if rpc.Result == nil || rpc.Result.Task == nil {
		return nil, errors.New("peer response missing result.task")
	}
	return rpc.Result.Task, nil
}

// failedTask builds a failed-state Task mirroring FederatedDeliverer.failed
// so callers see a consistent shape regardless of transport.
func failedTask(msg a2a.Message, reason string) *a2a.Task {
	return &a2a.Task{
		ID:        msg.TaskID,
		ContextID: msg.ContextID,
		Kind:      "task",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateFailed,
			Message: &a2a.Message{
				Role:      "agent",
				Parts:     []a2a.Part{{Kind: "text", Text: reason}},
				MessageID: "hub-failed-" + msg.MessageID,
				Kind:      "message",
			},
		},
	}
}
