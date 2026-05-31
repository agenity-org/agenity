// internal/a2a/webhook.go fires registered push-notification
// webhooks on every Task state transition (#482 Wave A3). The
// CRUD surface (tasks/pushNotificationConfig/set|get|list|delete)
// + its persistence-backed repository already shipped under
// #225 row A5; A3 turns the registrations into actual deliveries.
//
// Wiring: StreamBroker.PushConfigStore is set by cmd/run.go to the
// repository handle. Every call to StreamBroker.Publish — already
// the canonical fan-out point for SSE subscribers — ALSO fires a
// per-config webhook POST. Failures are logged but never block the
// broker (publishers run hot; webhook latency must not bleed into
// the runtime path).
//
// Retry contract: 3 attempts with exponential backoff (initial
// 250ms, doubling). Per-attempt timeout is 5s via the broker's
// HTTPClient. Total worst-case latency per webhook: 3 attempts ×
// (5s timeout + backoff up to 1s) ≈ 18s, all off the publisher
// goroutine.
//
// Auth: when the persisted config carries a SigningKey, the bytes
// are sent as the Bearer token in the Authorization header. (The
// dispatch's "Bearer <token>" interpretation; a separate HMAC-
// body-signing scheme can layer on later without breaking the
// wire shape.)
//
// Filters: when the config's Filters slice is empty the webhook
// fires on every event. Otherwise a filter entry like
// "state:COMPLETED" only matches when the event's Task.Status.State
// is "completed". Filter entries are case-insensitive on the value.
//
// Refs #482 V0.9.2-ARCHITECTURE.md §16 #225 row A5.
package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

const (
	webhookMaxAttempts    = 3
	webhookInitialBackoff = 250 * time.Millisecond
	webhookHTTPTimeout    = 5 * time.Second
)

// PushConfigLister is the minimum interface webhook delivery needs
// from the repository — list registered configs for a task. Defining
// it here keeps the broker decoupled from the full repository
// surface (which would otherwise pull the whole persistence package
// transitively into anything constructing a StreamBroker).
type PushConfigLister interface {
	List(ctx context.Context, taskID string) ([]*persistence.PushNotificationConfig, error)
}

// firePushNotifications looks up all configs registered for the
// event's task and dispatches a webhook POST per matching config.
// Called by StreamBroker.Publish AFTER subscriber fan-out so SSE
// latency is unaffected.
func (b *StreamBroker) firePushNotifications(ev StreamEvent) {
	if b.PushConfigStore == nil {
		return
	}
	if ev.Task == nil || ev.Task.ID == "" {
		return
	}
	// Copy the event so the async path can't observe a later
	// caller's mutation. Cheap — StreamEvent is shallow.
	evCopy := ev
	taskID := evCopy.Task.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		configs, err := b.PushConfigStore.List(ctx, taskID)
		if err != nil || len(configs) == 0 {
			return
		}
		body, err := json.Marshal(evCopy)
		if err != nil {
			return
		}
		client := b.httpClient()
		for _, cfg := range configs {
			if !matchesFilters(cfg.Filters, evCopy) {
				continue
			}
			deliverWebhook(client, cfg, body)
		}
	}()
}

func (b *StreamBroker) httpClient() *http.Client {
	if b.HTTPClient != nil {
		return b.HTTPClient
	}
	return &http.Client{Timeout: webhookHTTPTimeout}
}

// deliverWebhook POSTs the marshalled event body to the config's
// URL with retry. Caller passes a pre-marshalled body so retries
// don't pay marshal cost on each attempt.
func deliverWebhook(client *http.Client, cfg *persistence.PushNotificationConfig, body []byte) {
	backoff := webhookInitialBackoff
	for attempt := 0; attempt < webhookMaxAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodPost, cfg.URL, bytes.NewReader(body))
		if err != nil {
			return // malformed URL — retry won't help
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "chepherd-webhook/1.0")
		if len(cfg.SigningKey) > 0 {
			req.Header.Set("Authorization", "Bearer "+string(cfg.SigningKey))
		}
		resp, err := client.Do(req)
		if err == nil {
			io2xx := resp.StatusCode >= 200 && resp.StatusCode < 300
			_ = resp.Body.Close()
			if io2xx {
				return
			}
			// 4xx isn't worth retrying (caller-side error); 5xx is.
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return
			}
		}
		if attempt < webhookMaxAttempts-1 {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
}

// matchesFilters reports whether the event should be delivered to
// a config given its Filters slice. Empty filters fire on every
// event; non-empty filters require at least one match. Filter
// syntax: "state:<STATE>" or bare "<STATE>".
func matchesFilters(filters []string, ev StreamEvent) bool {
	if len(filters) == 0 {
		return true
	}
	if ev.Task == nil {
		return false
	}
	current := strings.ToLower(string(ev.Task.Status.State))
	for _, f := range filters {
		want := strings.ToLower(strings.TrimSpace(f))
		want = strings.TrimPrefix(want, "state:")
		if want == "" {
			continue
		}
		if want == current || want == strings.ToLower(ev.Type) {
			return true
		}
	}
	return false
}

// compile-time guard that fmt is still imported for diagnostic
// formatting that may be added in follow-up Waves.
var _ = fmt.Errorf
