// internal/federation/b2b_deliverer.go — #320 (#225 row E4).
// InteractiveB2BDeliverer is the third a2a.Deliverer implementation:
// chepherd-A's operator sends to chepherd-B over the federation wire
// AND sees the outbound recorded in their LOCAL chepherd-A inbox.
// Peer responses land via push-notification webhook → persisted →
// dashboard surfaces them → operator types reply → cycle repeats.
//
// Refs #320 (#225 row E4) + #281 (FederatedDeliverer substrate).
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

	"github.com/agenity-org/agenity/internal/a2a"
)

// InboxRecorder is the seam between this Deliverer and chepherd's
// runtime InboxStore. Stub in tests; real impl satisfies it.
type InboxRecorder interface {
	RecordEvent(from, body string)
}

// InteractiveB2BDeliverer forwards messages to a remote chepherd
// peer AND records the outbound as an inbox event for the local
// operator to track. Distinct from FederatedDeliverer (transparent
// routing) — this is EXPLICIT outbound-recording flow.
type InteractiveB2BDeliverer struct {
	PeerURL        string         // remote chepherd base URL
	PeerSID        string         // peer SID for inbox notation "@<sid>"
	Inbox          InboxRecorder  // optional; nil suppresses recording
	HTTPClient     *http.Client   // optional; defaults to 15s timeout
	OutboundBearer string         // optional Authorization: Bearer header for B3
}

func (d *InteractiveB2BDeliverer) Deliver(ctx context.Context, msg a2a.Message) (*a2a.Task, error) {
	if d.PeerURL == "" {
		return nil, errors.New("InteractiveB2BDeliverer: empty PeerURL")
	}
	if d.PeerSID == "" {
		return nil, errors.New("InteractiveB2BDeliverer: empty PeerSID")
	}
	text, err := a2a.ExtractText(msg)
	if err != nil {
		return d.failed(msg, err.Error()), err
	}
	task, err := d.forward(ctx, msg)
	if err != nil {
		return d.failed(msg, "forward: "+err.Error()), err
	}
	if d.Inbox != nil {
		d.Inbox.RecordEvent("operator", fmt.Sprintf("→ @%s: %s", d.PeerSID, truncate(text, 80)))
	}
	return task, nil
}

func (d *InteractiveB2BDeliverer) forward(ctx context.Context, msg a2a.Message) (*a2a.Task, error) {
	hc := d.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	envelope := map[string]any{
		"jsonrpc": "2.0",
		"id":      "b2b-" + msg.MessageID,
		"method":  "message/send",
		"params":  map[string]any{"message": msg},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(d.PeerURL, "/")+"/jsonrpc", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if d.OutboundBearer != "" {
		req.Header.Set("Authorization", "Bearer "+d.OutboundBearer)
	}
	resp, err := hc.Do(req)
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
		Error *a2a.JSONRPCError `json:"error"`
	}
	if err := json.Unmarshal(respBody, &rpc); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("peer JSON-RPC %d: %s", rpc.Error.Code, rpc.Error.Message)
	}
	if rpc.Result == nil || rpc.Result.Task == nil {
		return nil, errors.New("peer response missing task")
	}
	return rpc.Result.Task, nil
}

func (d *InteractiveB2BDeliverer) failed(msg a2a.Message, reason string) *a2a.Task {
	taskID := msg.TaskID
	if taskID == "" {
		taskID = fmt.Sprintf("b2b-failed-%d", time.Now().UnixNano())
	}
	return &a2a.Task{
		ID:        taskID,
		ContextID: msg.ContextID,
		Kind:      "task",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateFailed,
			Message: &a2a.Message{
				Role: "agent", Kind: "message",
				Parts: []a2a.Part{{Kind: "text", Text: reason}},
			},
		},
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var _ a2a.Deliverer = (*InteractiveB2BDeliverer)(nil)
