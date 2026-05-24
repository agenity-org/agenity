// Integration tests for the relay's push endpoints:
//   POST /v1/push/register
//   POST /v1/push/send
//
// These use httptest and bypass auth by registering the handlers
// directly (without the auth.Middleware wrapper). The dispatcher
// underneath is push.FromEnv with no env vars set → no backends, all
// sends will return ErrBackendDisabled but the registration + counting
// path is exercised.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chepherd/chepherd-relay/internal/auth"
)

// authHandler wraps a handler with a fake auth context so the push
// endpoints' ClaimsFromContext calls succeed without a real JWKS.
func authHandler(userID string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx := auth.WithClaims(req.Context(), &auth.Claims{
			UserID:   userID,
			Subject:  userID,
			Audience: "chepherd-rc",
		})
		h(w, req.WithContext(ctx))
	}
}

func newPushTestServer(t *testing.T, userID string) (*httptest.Server, *relay) {
	t.Helper()
	srv := newRelay()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/push/register", authHandler(userID, srv.pushRegister))
	mux.HandleFunc("/v1/push/send", authHandler(userID, srv.pushSend))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, srv
}

func TestPushRegister_HappyPath(t *testing.T) {
	ts, srv := newPushTestServer(t, "alice@example.com")
	body := `{
		"device_id": "device-1",
		"platform": "ios",
		"value": "abcd1234",
		"bundle_id": "io.chepherd.rc"
	}`
	resp, err := http.Post(
		ts.URL+"/v1/push/register",
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	regs, err := srv.pushTokens.ListByUser(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 reg, got %d", len(regs))
	}
	if regs[0].DeviceID != "device-1" {
		t.Errorf("device_id mismatch: %s", regs[0].DeviceID)
	}
}

func TestPushRegister_RejectsBadPlatform(t *testing.T) {
	ts, _ := newPushTestServer(t, "alice@example.com")
	body := `{"device_id":"d","platform":"linuxphone","value":"x"}`
	resp, err := http.Post(
		ts.URL+"/v1/push/register",
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestPushRegister_RejectsMissingFields(t *testing.T) {
	ts, _ := newPushTestServer(t, "alice@example.com")
	cases := []string{
		`{"platform":"ios","value":"x"}`,                       // missing device_id
		`{"device_id":"d","platform":"ios"}`,                   // missing value
		`{"device_id":"","platform":"ios","value":"x"}`,        // empty device_id
	}
	for i, body := range cases {
		resp, err := http.Post(
			ts.URL+"/v1/push/register",
			"application/json",
			strings.NewReader(body),
		)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("case %d: expected 400, got %d", i, resp.StatusCode)
		}
	}
}

func TestPushSend_NoDevicesNoBackend(t *testing.T) {
	ts, _ := newPushTestServer(t, "alice@example.com")
	body, _ := json.Marshal(map[string]any{
		"title":        "Test",
		"body":         "alice has an active session",
		"session_uuid": "s-1",
	})
	resp, err := http.Post(
		ts.URL+"/v1/push/send",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got struct {
		Attempted int `json:"devices_attempted"`
		Sent      int `json:"devices_sent"`
		Failed    int `json:"devices_failed"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.Attempted != 0 || got.Sent != 0 || got.Failed != 0 {
		t.Errorf("unexpected counts %+v", got)
	}
}

func TestPushSend_WithDevicesNoBackend(t *testing.T) {
	ts, srv := newPushTestServer(t, "alice@example.com")
	// Register two devices first.
	for _, did := range []string{"d-ios-1", "d-android-1"} {
		plat := "ios"
		if strings.Contains(did, "android") {
			plat = "android"
		}
		body, _ := json.Marshal(map[string]any{
			"device_id": did, "platform": plat,
			"value": "tok-" + did, "bundle_id": "io.chepherd.rc",
		})
		_, err := http.Post(ts.URL+"/v1/push/register", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
	}
	regs, _ := srv.pushTokens.ListByUser(context.Background(), "alice@example.com")
	if len(regs) != 2 {
		t.Fatalf("expected 2 registered devices, got %d", len(regs))
	}

	// Send — no backends configured → all 2 fail with ErrBackendDisabled.
	body, _ := json.Marshal(map[string]any{
		"title": "X", "body": "Y", "session_uuid": "s-1",
		"priority": "high",
	})
	resp, err := http.Post(ts.URL+"/v1/push/send", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got struct {
		Attempted int `json:"devices_attempted"`
		Sent      int `json:"devices_sent"`
		Failed    int `json:"devices_failed"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.Attempted != 2 || got.Sent != 0 || got.Failed != 2 {
		t.Errorf("counts mismatch: %+v", got)
	}
}
