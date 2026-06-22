// internal/federation/p0_487_mtls_test.go pins the v0.9.4 §15.1 +
// §22 cross-org daemon-to-daemon mTLS substrate (#487 Wave T3).
//
// Test cases drive REAL TLS handshakes between an in-process
// httptest server (configured with BuildServerTLSConfig) and an
// in-process http.Client (configured with BuildClientTLSConfig).
// No mocks: the same crypto/tls dial path the production binary
// uses runs end-to-end here.
//
// Refs #487 V0.9.2-ARCHITECTURE.md §15.1 §22.
package federation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// memoryAuthSecretRepo is the in-test AuthSecretRepository so the
// substrate doesn't depend on the sqlite package.
type memoryAuthSecretRepo struct {
	mu   sync.Mutex
	rows map[string]*persistence.AuthSecret
}

func newMemoryAuthRepo() *memoryAuthSecretRepo {
	return &memoryAuthSecretRepo{rows: map[string]*persistence.AuthSecret{}}
}

func (m *memoryAuthSecretRepo) Get(_ context.Context, purpose string) (*persistence.AuthSecret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sec, ok := m.rows[purpose]
	if !ok {
		return nil, fmt.Errorf("%s: not found", purpose)
	}
	cp := *sec
	return &cp, nil
}

func (m *memoryAuthSecretRepo) Save(_ context.Context, purpose string, key []byte, alg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[purpose] = &persistence.AuthSecret{
		Purpose: purpose, Key: append([]byte(nil), key...),
		Algorithm: alg, CreatedAt: time.Now().UTC(),
	}
	return nil
}

func TestWaveT3_LoadOrCreateMTLS_FirstCallMintsCert(t *testing.T) {
	t.Parallel()
	repo := newMemoryAuthRepo()
	cfg, err := LoadOrCreateMTLS(context.Background(), repo, "org-A")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if cfg.OrgID != "org-A" {
		t.Errorf("OrgID = %q, want org-A", cfg.OrgID)
	}
	if len(cfg.Certificate.Certificate) == 0 {
		t.Fatal("certificate.Certificate empty")
	}
	leaf, err := x509.ParseCertificate(cfg.Certificate.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if leaf.Subject.CommonName != "org-A" {
		t.Errorf("CN = %q, want org-A", leaf.Subject.CommonName)
	}
	if !leaf.NotAfter.After(time.Now()) {
		t.Errorf("cert already expired")
	}
}

func TestWaveT3_LoadOrCreateMTLS_RepeatLoadReturnsSameCert(t *testing.T) {
	t.Parallel()
	repo := newMemoryAuthRepo()
	first, _ := LoadOrCreateMTLS(context.Background(), repo, "org-B")
	second, err := LoadOrCreateMTLS(context.Background(), repo, "org-B")
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if !certEqual(first.Certificate, second.Certificate) {
		t.Error("repeat LoadOrCreate minted a new cert; should reuse persisted one")
	}
}

// crossOrgFixture sets up two MTLSConfig instances (orgA + orgB)
// with orgA's CA pinned in orgB's pool and vice versa — the
// canonical happy-path mutual-trust configuration.
type crossOrgFixture struct {
	orgA, orgB *MTLSConfig
}

func newCrossOrgFixture(t *testing.T) *crossOrgFixture {
	t.Helper()
	repoA := newMemoryAuthRepo()
	repoB := newMemoryAuthRepo()
	a, err := LoadOrCreateMTLS(context.Background(), repoA, "org-A")
	if err != nil {
		t.Fatalf("orgA: %v", err)
	}
	b, err := LoadOrCreateMTLS(context.Background(), repoB, "org-B")
	if err != nil {
		t.Fatalf("orgB: %v", err)
	}
	// Cross-pin via the same AddPinnedCA seam production uses.
	certPEMOfA, _ := CertPEMOf(a)
	certPEMOfB, _ := CertPEMOf(b)
	if err := AddPinnedCA(context.Background(), repoB, certPEMOfA); err != nil {
		t.Fatalf("pin A in B: %v", err)
	}
	if err := AddPinnedCA(context.Background(), repoA, certPEMOfB); err != nil {
		t.Fatalf("pin B in A: %v", err)
	}
	// Re-load both so the pinned CA pools include the just-added CAs.
	a, _ = LoadOrCreateMTLS(context.Background(), repoA, "org-A")
	b, _ = LoadOrCreateMTLS(context.Background(), repoB, "org-B")
	return &crossOrgFixture{orgA: a, orgB: b}
}

func TestWaveT3_CrossOrgHandshake_CrossPinnedCAsSucceed(t *testing.T) {
	t.Parallel()
	fix := newCrossOrgFixture(t)

	// Server runs as orgA, client runs as orgB.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Surface the verified peer CN so the client can assert it.
		if len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "no peer cert", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	srv.TLS = BuildServerTLSConfig(fix.orgA)
	srv.StartTLS()
	defer srv.Close()

	clientCfg := BuildClientTLSConfig(fix.orgB)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientCfg},
		Timeout:   3 * time.Second,
	}
	resp, err := client.Get(srv.URL + "/ping")
	if err != nil {
		t.Fatalf("cross-pinned handshake failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "org-B" {
		t.Errorf("server saw peer CN = %q, want org-B", body)
	}
}

func TestWaveT3_CrossOrgHandshake_WrongCARejected(t *testing.T) {
	t.Parallel()
	fix := newCrossOrgFixture(t)
	// Build a THIRD org whose cert is NOT pinned in A — the
	// canonical "outsider tries to connect" case.
	repoC := newMemoryAuthRepo()
	orgC, err := LoadOrCreateMTLS(context.Background(), repoC, "org-C")
	if err != nil {
		t.Fatalf("orgC: %v", err)
	}
	// Make orgC trust A (so client side accepts server) but A
	// doesn't trust orgC — the server-side cert verification is
	// what must reject the handshake.
	certPEMOfA, _ := CertPEMOf(fix.orgA)
	_ = AddPinnedCA(context.Background(), repoC, certPEMOfA)
	orgC, _ = LoadOrCreateMTLS(context.Background(), repoC, "org-C")

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "should never reach here")
	}))
	srv.TLS = BuildServerTLSConfig(fix.orgA)
	srv.StartTLS()
	defer srv.Close()

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: BuildClientTLSConfig(orgC)},
		Timeout:   3 * time.Second,
	}
	_, err = client.Get(srv.URL + "/ping")
	if err == nil {
		t.Fatal("expected TLS handshake error, got nil — server accepted untrusted client cert")
	}
	if !strings.Contains(err.Error(), "tls") && !strings.Contains(err.Error(), "certificate") {
		t.Errorf("error wasn't a TLS failure: %v", err)
	}
}

func TestWaveT3_BuildClientTLSConfig_NilReturnsNil(t *testing.T) {
	t.Parallel()
	if BuildClientTLSConfig(nil) != nil {
		t.Error("nil MTLSConfig should return nil client config (dev/test fallback)")
	}
	if BuildServerTLSConfig(nil) != nil {
		t.Error("nil MTLSConfig should return nil server config")
	}
}

func TestWaveT3_BuildClientTLSConfig_RequiresTLS13(t *testing.T) {
	t.Parallel()
	repo := newMemoryAuthRepo()
	cfg, _ := LoadOrCreateMTLS(context.Background(), repo, "org-X")
	clientCfg := BuildClientTLSConfig(cfg)
	if clientCfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("client MinVersion = %v, want TLS 1.3 (downgrade attacks fail closed)", clientCfg.MinVersion)
	}
	serverCfg := BuildServerTLSConfig(cfg)
	if serverCfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("server MinVersion = %v, want TLS 1.3", serverCfg.MinVersion)
	}
	if serverCfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth = %v, want RequireAndVerifyClientCert", serverCfg.ClientAuth)
	}
}

func TestWaveT3_AddPinnedCA_AccumulatesMultiplePEMs(t *testing.T) {
	t.Parallel()
	repo := newMemoryAuthRepo()
	// Mint three orgs' certs + pin all three into a single trust
	// pool so the pool can verify each.
	mintCert := func(org string) []byte {
		r := newMemoryAuthRepo()
		c, _ := LoadOrCreateMTLS(context.Background(), r, org)
		p, _ := CertPEMOf(c)
		return p
	}
	for _, org := range []string{"org-1", "org-2", "org-3"} {
		if err := AddPinnedCA(context.Background(), repo, mintCert(org)); err != nil {
			t.Fatalf("pin %s: %v", org, err)
		}
	}
	// Now load + verify the pool's subject pool length (3 CAs).
	cfg, err := LoadOrCreateMTLS(context.Background(), repo, "org-aggregator")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PinnedCAs == nil {
		t.Fatal("PinnedCAs nil")
	}
	if subs := cfg.PinnedCAs.Subjects(); len(subs) != 3 {
		t.Errorf("PinnedCAs subjects = %d, want 3", len(subs))
	}
}

func TestWaveT3_AddPinnedCA_RejectsNonPEM(t *testing.T) {
	t.Parallel()
	repo := newMemoryAuthRepo()
	if err := AddPinnedCA(context.Background(), repo, []byte("not a cert")); err == nil {
		t.Error("expected error for non-PEM input")
	}
}

func certEqual(a, b tls.Certificate) bool {
	if len(a.Certificate) != len(b.Certificate) {
		return false
	}
	for i := range a.Certificate {
		if string(a.Certificate[i]) != string(b.Certificate[i]) {
			return false
		}
	}
	return true
}
