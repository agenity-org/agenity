// internal/a2a/push_webhook.go — #308 (#225 row A5). Outbound webhook
// delivery for A2A task-state push notifications.
//
// When a Task transitions to a terminal state (completed / failed /
// canceled) or input-required, DispatchTaskUpdate loads every
// PushNotificationConfig registered for that task and fires an
// HMAC-SHA256-signed POST to each URL.
//
// Signature shape:
//
//	X-Chepherd-Signature: sha256=<hex(HMAC-SHA256(body, config.SigningKey))>
//
// Receivers verify the signature using the same SigningKey they
// supplied when calling tasks/pushNotificationConfig/set; without
// the key, replays + body-tampering land identically — the verifier
// rejects.
//
// Delivery is BEST-EFFORT: a non-2xx response or transport error is
// logged via the optional ErrorSink but does NOT propagate to the
// caller (the Task state machine has already committed; the webhook
// is a notification side-effect). Future hardening: bounded retry
// queue with exponential backoff.
//
// Refs #308 (#225 row A5) + #208.
package a2a

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// PushConfigStore is the seam between the dispatcher and the
// persistence layer. The full PushNotificationConfigRepository
// satisfies this interface naturally; tests use a stub.
type PushConfigStore interface {
	List(ctx context.Context, taskID string) ([]*persistence.PushNotificationConfig, error)
}

// WebhookDispatcher fires outbound HMAC-signed POSTs to every
// PushNotificationConfig registered for a given task.
//
// Fields:
//   - Store: where to load configs from (typically Store.PushConfigs()).
//   - HTTPClient: optional, defaults to a 5s-timeout client when nil.
//   - ErrorSink: optional callback for per-webhook delivery errors.
//     nil suppresses error reporting (still best-effort).
//
// Refs #308 (#225 row A5).
type WebhookDispatcher struct {
	Store      PushConfigStore
	HTTPClient *http.Client
	ErrorSink  func(taskID, url string, err error)
}

// TaskUpdate is the body shape POSTed to each webhook URL. Matches
// A2A v1.0 push-notification payload conventions.
type TaskUpdate struct {
	TaskID    string    `json:"taskId"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Optional inline Task — null when caller doesn't include it.
	// Receivers wanting the full Task can GET /a2a/tasks/<taskId>
	// after the webhook to fetch the canonical record.
	Task *Task `json:"task,omitempty"`
}

// DispatchTaskUpdate loads every PushNotificationConfig for taskID
// and fires the webhook to each. Best-effort — errors logged via
// ErrorSink but never returned (caller's state machine has already
// committed).
//
// The signature header value is `sha256=<lowercase-hex(HMAC)>`.
// Empty SigningKey yields no signature header — receivers that
// require auth should reject configs without a key at registration
// time.
//
// Refs #308 (#225 row A5).
func (d *WebhookDispatcher) DispatchTaskUpdate(ctx context.Context, update TaskUpdate) {
	if d == nil || d.Store == nil || update.TaskID == "" {
		return
	}
	configs, err := d.Store.List(ctx, update.TaskID)
	if err != nil || len(configs) == 0 {
		return
	}
	body, err := json.Marshal(update)
	if err != nil {
		return
	}
	hc := d.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	for _, cfg := range configs {
		// Each delivery is independent; one failure doesn't gate the others.
		go d.deliver(ctx, hc, cfg, body, update.TaskID)
	}
}

func (d *WebhookDispatcher) deliver(ctx context.Context, hc *http.Client, cfg *persistence.PushNotificationConfig, body []byte, taskID string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		d.report(taskID, cfg.URL, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "chepherd-webhook/0.9.3")
	if len(cfg.SigningKey) > 0 {
		mac := hmac.New(sha256.New, cfg.SigningKey)
		mac.Write(body)
		req.Header.Set("X-Chepherd-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := hc.Do(req)
	if err != nil {
		d.report(taskID, cfg.URL, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		d.report(taskID, cfg.URL, fmt.Errorf("non-2xx %d from %s", resp.StatusCode, cfg.URL))
	}
}

func (d *WebhookDispatcher) report(taskID, url string, err error) {
	if d.ErrorSink != nil {
		d.ErrorSink(taskID, url, err)
	}
}

// VerifySignature is a convenience helper for webhook RECEIVERS to
// validate an inbound POST's X-Chepherd-Signature header against the
// body bytes + shared SigningKey. Returns nil on match, an error
// describing the mismatch otherwise.
//
// Refs #308 (#225 row A5).
func VerifySignature(body []byte, signingKey []byte, signatureHeader string) error {
	if !hmac.Equal([]byte(signatureHeader[:7]), []byte("sha256=")) {
		return errors.New("VerifySignature: header missing 'sha256=' prefix")
	}
	got, err := hex.DecodeString(signatureHeader[7:])
	if err != nil {
		return fmt.Errorf("VerifySignature: decode hex: %w", err)
	}
	mac := hmac.New(sha256.New, signingKey)
	mac.Write(body)
	want := mac.Sum(nil)
	if !hmac.Equal(got, want) {
		return errors.New("VerifySignature: HMAC mismatch")
	}
	return nil
}
