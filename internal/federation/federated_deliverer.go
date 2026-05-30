// internal/federation/federated_deliverer.go — v0.9.3 #225 row C2.
// Wraps a local a2a.Deliverer with cross-instance routing: when an
// inbound SendMessage's ContextID points at a peer SID known to the
// AgentCardRepository, the message is forwarded to that peer's
// /jsonrpc endpoint instead of being delivered locally.
//
// ContextID format for peer routing:
//
//	@<peer-sid>/<session-id-or-name>
//
// Operator-friendly: the `@` prefix is unambiguous (chepherd session
// IDs are UUID-shaped and short @-names never start with `@`), and the
// `<peer-sid>` is the 8-char #270 fingerprint visible in every peer's
// agent card. Pre-C2 ContextID semantics (raw session ID / short
// @-name) are preserved — without the `@<sid>/` prefix the wrapped
// local Deliverer fires normally.
//
// Refs #225 row C2 #277 (method bodies).
package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/persistence"
)

// FederatedDeliverer wraps a local a2a.Deliverer + an AgentCardRepository
// + an HTTP client. Implements a2a.Deliverer so it slots into the
// existing Router.WireDeliverer call without API change.
type FederatedDeliverer struct {
	Local      a2a.Deliverer
	Cards      persistence.AgentCardRepository
	HTTPClient *http.Client
	// SelfSID is this chepherd's instance UUID; if a contextID names
	// our own SID we route locally (operator can pass `@<self-sid>/<sess>`
	// for explicit clarity in scripts without breaking routing).
	SelfSID string
	// OutboundBearer is the bearer token sent on the peer's /jsonrpc
	// POST. v0.9.3 ships with a single shared secret (operator
	// pre-shares between instances); B3 layers per-peer per-instance
	// JWTs on top of this same seam.
	OutboundBearer string
	// ForwardTimeout caps each peer forward. Default 15s when zero.
	ForwardTimeout time.Duration
}

// parsePeerContextID splits "@<sid>/<rest>" into (sid, rest). Returns
// ("", input, false) when the contextID isn't peer-prefixed.
func parsePeerContextID(ctxID string) (sid, rest string, ok bool) {
	if !strings.HasPrefix(ctxID, "@") {
		return "", ctxID, false
	}
	slash := strings.IndexByte(ctxID, '/')
	if slash < 2 {
		return "", ctxID, false
	}
	sid = ctxID[1:slash]
	rest = ctxID[slash+1:]
	if sid == "" || rest == "" {
		return "", ctxID, false
	}
	return sid, rest, true
}

// extractPeerURL returns the `url` field from the AgentCard JSON body.
// AgentCards are signed JSON; the url field is mandatory per A2A spec.
func extractPeerURL(card *persistence.AgentCard) (string, error) {
	if card == nil || len(card.Body) == 0 {
		return "", errors.New("federation: AgentCard has empty body")
	}
	var parsed map[string]any
	if err := json.Unmarshal(card.Body, &parsed); err != nil {
		return "", fmt.Errorf("federation: AgentCard body not JSON: %w", err)
	}
	url, _ := parsed["url"].(string)
	if url == "" {
		return "", fmt.Errorf("federation: AgentCard %q missing `url` field", card.SID)
	}
	return strings.TrimRight(url, "/"), nil
}

// Deliver routes msg to a peer when ContextID is `@<sid>/<rest>` and
// the SID is in our AgentCardRepository (and not ourselves). Otherwise
// delegates to the wrapped Local deliverer.
func (f *FederatedDeliverer) Deliver(ctx context.Context, msg a2a.Message) (*a2a.Task, error) {
	sid, rest, ok := parsePeerContextID(msg.ContextID)
	if !ok || sid == f.SelfSID {
		// Local fallback: strip the @<self>/ prefix if present so the
		// local deliverer sees the bare session-id/name it expects.
		if ok && sid == f.SelfSID {
			msg.ContextID = rest
		}
		return f.Local.Deliver(ctx, msg)
	}
	card, err := f.Cards.Get(ctx, sid)
	if err != nil || card == nil {
		return f.failed(msg, "peer "+sid+" not in AgentCard cache"),
			fmt.Errorf("federation.Deliver: peer %q unknown (cache miss); run `--federation-registry-url` to discover", sid)
	}
	peerURL, err := extractPeerURL(card)
	if err != nil {
		return f.failed(msg, err.Error()), err
	}
	// Forward to the peer's /jsonrpc. Strip the peer SID from
	// ContextID so the peer sees its own session-id/name.
	forwarded := msg
	forwarded.ContextID = rest
	task, err := f.forward(ctx, peerURL, forwarded)
	if err != nil {
		return f.failed(msg, "forward to "+peerURL+": "+err.Error()), err
	}
	// Restore the @<peer>/ prefix on the returned Task's ContextID so
	// downstream pollers (GetTask) keep a stable handle.
	if task != nil && task.ContextID != "" {
		task.ContextID = "@" + sid + "/" + task.ContextID
	}
	return task, nil
}

// forward POSTs the message to the peer's /jsonrpc endpoint, parses
// the JSON-RPC envelope, and returns the embedded Task. The outbound
// JWT/bearer model is intentionally simple in v0.9.3 — B3 layers per-
// instance signing keys on top without changing this seam.
func (f *FederatedDeliverer) forward(ctx context.Context, peerURL string, msg a2a.Message) (*a2a.Task, error) {
	if f.HTTPClient == nil {
		f.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	timeout := f.ForwardTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	fwdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	envelope := map[string]any{
		"jsonrpc": "2.0",
		"id":      "fed-" + msg.MessageID,
		"method":  "SendMessage",
		"params":  map[string]any{"message": msg},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	req, err := http.NewRequestWithContext(fwdCtx, http.MethodPost,
		peerURL+"/jsonrpc", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if f.OutboundBearer != "" {
		req.Header.Set("Authorization", "Bearer "+f.OutboundBearer)
	}
	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peer HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
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
	if err := json.Unmarshal(respBody, &rpc); err != nil {
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

func (f *FederatedDeliverer) failed(msg a2a.Message, reason string) *a2a.Task {
	return &a2a.Task{
		ID:        msg.TaskID,
		ContextID: msg.ContextID,
		Kind:      "task",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateFailed,
			Message: &a2a.Message{
				Role:      "agent",
				Parts:     []a2a.Part{{Kind: "text", Text: reason}},
				MessageID: "fed-failed-" + msg.MessageID,
				Kind:      "message",
			},
		},
	}
}

// compile-time assertion that FederatedDeliverer satisfies a2a.Deliverer.
var _ a2a.Deliverer = (*FederatedDeliverer)(nil)
