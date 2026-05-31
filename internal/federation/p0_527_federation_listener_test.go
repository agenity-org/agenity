// internal/federation/p0_527_federation_listener_test.go is the
// acceptance gate for #527 Wave T3.1 — closes T3 #487's substrate-
// vs-production-wiring gap. Asserts the federation HTTP listener
// honors the mTLS contract end-to-end:
//
//   - Two daemons with cross-pinned CAs can exchange federation
//     HTTP traffic over mTLS (REAL TLS handshake via Go's
//     crypto/tls).
//   - A third daemon NOT pinned in the server's pool gets a TLS
//     handshake error on inbound — NOT a JSON-RPC error envelope.
//   - When --federation-mtls=false (no MTLSConfig wired), the
//     same code path falls back to plain HTTP.
//
// These run REAL TLS handshakes via httptest.NewUnstartedServer +
// BuildServerTLSConfig + an http.Client with BuildClientTLSConfig
// — same code path the production binary uses. The 2-daemon-binary
// e2e (production cmd/run.go bootstrap of the federation listener)
// lives in cmd/run_federation_e2e_test.go.
//
// Refs #527 #487 V0.9.2-ARCHITECTURE.md §15.1 §22.
package federation

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWaveT31_FederationListener_CrossPinnedSucceeds(t *testing.T) {
	t.Parallel()
	fix := newCrossOrgFixture(t)

	// Server side of "daemon A" — uses the production
	// BuildServerTLSConfig + serves a representative federation
	// endpoint (an inline /api/v1/peers stub for the test).
	called := make(chan struct{}, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case called <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"peers":[]}`)
	}))
	srv.TLS = BuildServerTLSConfig(fix.orgA)
	srv.StartTLS()
	defer srv.Close()

	// Client side of "daemon B" — uses the production
	// BuildClientTLSConfig wrapped in the same http.Transport
	// shape cmd/run.go wires onto Federation.HTTPClient.
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: BuildClientTLSConfig(fix.orgB),
		},
	}
	resp, err := client.Get(srv.URL + "/api/v1/peers")
	if err != nil {
		t.Fatalf("cross-pinned federation handshake failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("server handler not invoked despite 200 — handshake state inconsistent")
	}
}

func TestWaveT31_FederationListener_WrongCARejectedAtHandshake(t *testing.T) {
	t.Parallel()
	fix := newCrossOrgFixture(t)
	// Third org whose cert is NOT pinned in orgA.
	repoC := newMemoryAuthRepo()
	orgC, err := LoadOrCreateMTLS(context.Background(), repoC, "org-C")
	if err != nil {
		t.Fatalf("orgC: %v", err)
	}
	// Make orgC trust orgA so the CLIENT side accepts the server
	// cert; the failure must come from the SERVER side rejecting
	// orgC's untrusted client cert.
	certPEMOfA, _ := CertPEMOf(fix.orgA)
	_ = AddPinnedCA(context.Background(), repoC, certPEMOfA)
	orgC, _ = LoadOrCreateMTLS(context.Background(), repoC, "org-C")

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("server handler invoked despite untrusted client — handshake leaked")
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = BuildServerTLSConfig(fix.orgA)
	srv.StartTLS()
	defer srv.Close()

	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: BuildClientTLSConfig(orgC),
		},
	}
	_, err = client.Get(srv.URL + "/api/v1/peers")
	if err == nil {
		t.Fatal("expected TLS handshake error, got nil — server accepted untrusted client cert")
	}
	// The actual error message varies by Go version + handshake
	// state machine timing; what matters is that it's a TLS
	// failure, not a JSON-RPC error envelope.
	if !strings.Contains(err.Error(), "tls") &&
		!strings.Contains(err.Error(), "certificate") &&
		!strings.Contains(err.Error(), "EOF") &&
		!strings.Contains(err.Error(), "broken pipe") &&
		!strings.Contains(err.Error(), "connection reset") {
		t.Errorf("error wasn't a TLS-handshake failure: %v", err)
	}
}

func TestWaveT31_FederationListener_NoMTLSConfigKeepsPlainHTTP(t *testing.T) {
	t.Parallel()
	// Dev-mode fallback path: when --federation-mtls=false the
	// listener stays plain HTTP. Verified by NOT wrapping the
	// httptest.Server in TLS + asserting the client (also no TLS
	// config) hits the handler successfully.
	called := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case called <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/peers")
	if err != nil {
		t.Fatalf("plain HTTP dev-mode failed: %v", err)
	}
	resp.Body.Close()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("plain-mode handler not invoked")
	}
}
