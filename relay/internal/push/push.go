// Package push — push notification dispatch for chepherd-rc clients.
//
// Two backends: APNs (iOS) and FCM (Android + web push when over a
// service worker). Both are gated by env vars; when credentials are
// absent the backend becomes a no-op (Dispatcher.Send returns
// ErrBackendDisabled rather than failing the whole request).
//
// Notification policy (matches protocol §6):
//   - Verdict 'intervene' on the operator's currently-foregrounded
//     session  → high-priority push with sound (the operator must see this)
//   - Verdict 'coach' on a session the operator has subscribed to
//     → standard-priority push, no sound
//   - 'silent' + 'praise' → NEVER pushed
//   - The relay never sees verdict content over a P2P transport; it
//     pushes ONLY based on metadata the daemon sends to /v1/push/send
//     explicitly. P2P privacy holds.

package push

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// ErrBackendDisabled is returned when the backend was not configured.
var ErrBackendDisabled = errors.New("push: backend disabled")

// Priority — APNs apns-priority + FCM android.priority.
type Priority int

const (
	PriorityNormal Priority = iota
	PriorityHigh
)

// Platform — which backend handles a given delivery.
type Platform int

const (
	PlatformiOS Platform = iota
	PlatformAndroid
	PlatformWebPush
)

// Token — opaque device token for the relevant platform.
type Token struct {
	Platform Platform
	// APNs device token / FCM registration token / Web Push endpoint.
	Value string
	// Optional bundle/app identifier — APNs needs this to route.
	BundleID string
}

// Notification — the canonical chepherd push shape. Both backends
// shape this into their native envelope.
type Notification struct {
	// Title shown in the system notification banner.
	Title string
	// Body shown beneath the title.
	Body string
	// Sound — empty for silent, "default" for system sound, "chepherd-crisis"
	// for our custom crisis chime (must be bundled in the app).
	Sound string
	// Priority — high for verdict=intervene, normal for verdict=coach.
	Priority Priority
	// SessionUUID — included as a custom data field so the app can
	// deep-link straight to the session detail screen.
	SessionUUID string
	// CollapseKey — APNs apns-collapse-id / FCM collapse_key. When the
	// operator hasn't opened the device, multiple notifications for the
	// same session collapse into one.
	CollapseKey string
	// TTL — how long the push provider should hold an undeliverable msg.
	TTL time.Duration
}

// Dispatcher is the dispatch contract. Both backends satisfy it.
type Dispatcher interface {
	Send(ctx context.Context, tok Token, n Notification) error
	Kind() string
}

// MultiDispatcher routes by Platform.
type MultiDispatcher struct {
	mu       sync.RWMutex
	byPlatform map[Platform]Dispatcher
}

// NewMulti constructs a router from the available platform backends.
func NewMulti(backends ...Dispatcher) *MultiDispatcher {
	m := &MultiDispatcher{byPlatform: map[Platform]Dispatcher{}}
	for _, b := range backends {
		switch b.Kind() {
		case "apns":
			m.byPlatform[PlatformiOS] = b
		case "fcm":
			m.byPlatform[PlatformAndroid] = b
			m.byPlatform[PlatformWebPush] = b
		}
	}
	return m
}

// Send routes to the platform-specific backend.
func (m *MultiDispatcher) Send(ctx context.Context, tok Token, n Notification) error {
	m.mu.RLock()
	d, ok := m.byPlatform[tok.Platform]
	m.mu.RUnlock()
	if !ok || d == nil {
		return fmt.Errorf("push: no dispatcher for platform %v: %w", tok.Platform, ErrBackendDisabled)
	}
	return d.Send(ctx, tok, n)
}

// Kind returns "multi".
func (m *MultiDispatcher) Kind() string { return "multi" }

// FromEnv constructs a MultiDispatcher from environment variables:
//
//	CHEPHERD_RELAY_APNS_CERT_PATH   .p12/.pem path for APNs auth
//	CHEPHERD_RELAY_APNS_TEAM_ID     team id for token-based APNs (alt)
//	CHEPHERD_RELAY_APNS_KEY_ID      key id  for token-based APNs (alt)
//	CHEPHERD_RELAY_APNS_KEY_PATH    .p8     for token-based APNs (alt)
//	CHEPHERD_RELAY_APNS_PRODUCTION  "true" → Production gateway (default: sandbox)
//	CHEPHERD_RELAY_FCM_CRED_PATH    service-account JSON for FCM v1
//
// When no credentials are configured, the returned MultiDispatcher
// has no backends and every Send returns ErrBackendDisabled.
func FromEnv(ctx context.Context) *MultiDispatcher {
	var backends []Dispatcher
	if a := newAPNsFromEnv(ctx); a != nil {
		backends = append(backends, a)
	}
	if f := newFCMFromEnv(ctx); f != nil {
		backends = append(backends, f)
	}
	if len(backends) == 0 {
		log.Println("push: no backends configured")
	}
	return NewMulti(backends...)
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
