// internal/webrtcrtc/hub_turn_test.go — #672 hub-relayed WebRTC A2A
// (epic). Pins the hub TURN-credential fetch + ICE-config merge.
package webrtcrtc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchHubTURN_ParsesCreds(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Chepherd-Org") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(HubTURNCredentials{
			Username: "1234:org-a", Password: "secret", TTL: 600,
			URIs: []string{"turn:hub.example.com:3478?transport=udp"}, Realm: "chepherd-hub",
		})
	}))
	defer srv.Close()

	creds, err := FetchHubTURN(context.Background(), srv.Client(), srv.URL, "org-a")
	if err != nil {
		t.Fatalf("FetchHubTURN: %v", err)
	}
	if creds.Username != "1234:org-a" || creds.Password != "secret" || len(creds.URIs) != 1 {
		t.Fatalf("creds = %+v", creds)
	}
	if got := creds.Expiry(time.Unix(0, 0)); got != time.Unix(600, 0) {
		t.Errorf("expiry = %v, want %v", got, time.Unix(600, 0))
	}
}

func TestFetchHubTURN_503IsSTUNOnly(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	creds, err := FetchHubTURN(context.Background(), srv.Client(), srv.URL, "org-a")
	if err != nil {
		t.Fatalf("503 must be non-error (STUN-only), got %v", err)
	}
	if len(creds.URIs) != 0 {
		t.Fatalf("expected empty creds on 503, got %+v", creds)
	}
}

func TestMergeTURN_PreservesSTUNDefaults(t *testing.T) {
	t.Parallel()
	creds := HubTURNCredentials{
		Username: "u", Password: "p",
		URIs: []string{"turn:hub:3478?transport=udp"},
	}
	merged := MergeTURN(Config{}, creds)
	// STUN defaults + the TURN entry.
	if len(merged.ICEServers) != len(DefaultICEServers())+1 {
		t.Fatalf("merged ICE servers = %d, want %d", len(merged.ICEServers), len(DefaultICEServers())+1)
	}
	last := merged.ICEServers[len(merged.ICEServers)-1]
	if last.Username != "u" || last.Credential != "p" {
		t.Errorf("TURN entry = %+v", last)
	}
}

func TestMergeTURN_NoURIsReturnsUnchanged(t *testing.T) {
	t.Parallel()
	in := Config{ChannelLabel: "a2a"}
	out := MergeTURN(in, HubTURNCredentials{})
	if len(out.ICEServers) != 0 || out.ChannelLabel != "a2a" {
		t.Fatalf("expected unchanged cfg, got %+v", out)
	}
}
