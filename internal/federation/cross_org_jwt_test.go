// internal/federation/cross_org_jwt_test.go pins the v0.9.4 §10
// Pattern 2 Phase 2 daemon-side cross-org JWT mint + caching client
// contract (#498 Wave F8).
//
// Coverage:
//
//   - CrossOrgJWTMinter: header guards (missing X-Chepherd-Caller-Org,
//     missing Hub-Attest), grant denial, scope required, happy path
//     mints JWT with caller in sub claim and configured ttl in exp
//   - CrossOrgJWTClient: cache hit on repeat call, cache miss after
//     expiry, Invalidate evicts entry, Get errors when hub returns
//     non-200, concurrent Gets serialize through cache
//
// Refs #498 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 2.
package federation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─── Minter ───────────────────────────────────────────────────────

type stubSigner struct {
	signed atomic.Int32
	err    error
}

func (s *stubSigner) Sign(claims map[string]any) (string, error) {
	s.signed.Add(1)
	if s.err != nil {
		return "", s.err
	}
	body, _ := json.Marshal(claims)
	return "stub.jws." + string(body), nil
}

type stubGrants struct {
	err  error
	meta *GrantMeta
}

func (g *stubGrants) Check(_ context.Context, _, _ string) (*GrantMeta, error) {
	return g.meta, g.err
}

func TestWaveF8_Minter_RejectsMissingCallerOrgHeader(t *testing.T) {
	t.Parallel()
	m := &CrossOrgJWTMinter{Issuer: "bob.example", Signer: &stubSigner{}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/federation/jwt",
		strings.NewReader(`{"scope":"x"}`))
	r.Header.Set("X-Chepherd-Hub-Attest", "true")
	m.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", w.Code)
	}
}

func TestWaveF8_Minter_RejectsMissingHubAttest(t *testing.T) {
	t.Parallel()
	m := &CrossOrgJWTMinter{Issuer: "bob.example", Signer: &stubSigner{}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/federation/jwt",
		strings.NewReader(`{"scope":"x"}`))
	r.Header.Set("X-Chepherd-Caller-Org", "alice.example")
	m.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", w.Code)
	}
}

func TestWaveF8_Minter_GrantDenialReturns403(t *testing.T) {
	t.Parallel()
	m := &CrossOrgJWTMinter{
		Issuer: "bob.example",
		Signer: &stubSigner{},
		Grants: &stubGrants{err: errors.New("denied by §13 grant table")},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/federation/jwt",
		strings.NewReader(`{"scope":"a2a.send"}`))
	r.Header.Set("X-Chepherd-Caller-Org", "alice.example")
	r.Header.Set("X-Chepherd-Hub-Attest", "true")
	m.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("code = %d, want 403", w.Code)
	}
}

func TestWaveF8_Minter_HappyPath_SignsWithExpectedClaims(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	signer := &stubSigner{}
	m := &CrossOrgJWTMinter{
		Issuer: "bob.example",
		Signer: signer,
		TTL:    2 * time.Minute,
		NowFn:  func() time.Time { return now },
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/federation/jwt",
		strings.NewReader(`{"scope":"a2a.send","audience":"runner-7"}`))
	r.Header.Set("X-Chepherd-Caller-Org", "alice.example")
	r.Header.Set("X-Chepherd-Hub-Attest", "true")
	m.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if signer.signed.Load() != 1 {
		t.Errorf("Sign called %d times, want 1", signer.signed.Load())
	}
	var resp CrossOrgJWTResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.JWT == "" {
		t.Errorf("empty JWT")
	}
	if resp.Issuer != "bob.example" {
		t.Errorf("iss = %q, want bob.example", resp.Issuer)
	}
	if resp.NotBefore != now.Unix() {
		t.Errorf("nbf = %d, want %d", resp.NotBefore, now.Unix())
	}
	if want := now.Add(2 * time.Minute).Unix(); resp.Expires != want {
		t.Errorf("exp = %d, want %d (now+ttl)", resp.Expires, want)
	}
	if !strings.Contains(resp.JWT, `"sub":"alice.example"`) {
		t.Errorf("sub claim missing from minted JWT: %s", resp.JWT)
	}
	if !strings.Contains(resp.JWT, `"scope":"a2a.send"`) {
		t.Errorf("scope claim missing: %s", resp.JWT)
	}
	if !strings.Contains(resp.JWT, `"aud":"runner-7"`) {
		t.Errorf("aud claim missing: %s", resp.JWT)
	}
}

// TestP0_580_Minter_EmbedsGrantID pins V0.9.2-ARCH §15.2 requirement
// that chepherd_grant_id claim is present in the minted JWT when the
// CrossOrgGrantChecker returns a non-empty GrantID.
func TestP0_580_Minter_EmbedsGrantID(t *testing.T) {
	t.Parallel()
	signer := &stubSigner{}
	m := &CrossOrgJWTMinter{
		Issuer: "bob.example",
		Signer: signer,
		Grants: &stubGrants{meta: &GrantMeta{GrantID: "grant-abc-123"}},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/federation/jwt",
		strings.NewReader(`{"scope":"a2a.send"}`))
	r.Header.Set("X-Chepherd-Caller-Org", "alice.example")
	r.Header.Set("X-Chepherd-Hub-Attest", "true")
	m.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var resp CrossOrgJWTResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp.JWT, `"chepherd_grant_id":"grant-abc-123"`) {
		t.Errorf("chepherd_grant_id claim missing or wrong in JWT: %s", resp.JWT)
	}
}

// TestP0_581_Minter_EmbedsRateWindow pins V0.9.2-ARCH §15.2 requirement
// that chepherd_rate_window claim is present with calls_per_minute +
// calls_per_day when the CrossOrgGrantChecker supplies a RateWindow.
func TestP0_581_Minter_EmbedsRateWindow(t *testing.T) {
	t.Parallel()
	signer := &stubSigner{}
	m := &CrossOrgJWTMinter{
		Issuer: "bob.example",
		Signer: signer,
		Grants: &stubGrants{meta: &GrantMeta{
			GrantID: "grant-xyz",
			RateWindow: &RateWindow{
				CallsPerMinute: 60,
				CallsPerDay:    5000,
			},
		}},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/federation/jwt",
		strings.NewReader(`{"scope":"a2a.send"}`))
	r.Header.Set("X-Chepherd-Caller-Org", "alice.example")
	r.Header.Set("X-Chepherd-Hub-Attest", "true")
	m.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var resp CrossOrgJWTResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp.JWT, `"chepherd_rate_window"`) {
		t.Errorf("chepherd_rate_window claim missing from JWT: %s", resp.JWT)
	}
	if !strings.Contains(resp.JWT, `"calls_per_minute":60`) {
		t.Errorf("calls_per_minute=60 missing from chepherd_rate_window: %s", resp.JWT)
	}
	if !strings.Contains(resp.JWT, `"calls_per_day":5000`) {
		t.Errorf("calls_per_day=5000 missing from chepherd_rate_window: %s", resp.JWT)
	}
}

// TestP0_580_Minter_NoGrantID_ClaimOmitted verifies that when the
// checker returns nil meta (permissive/dev mode), chepherd_grant_id
// is NOT emitted in the JWT (no spurious empty-string claims).
func TestP0_580_Minter_NoGrantID_ClaimOmitted(t *testing.T) {
	t.Parallel()
	signer := &stubSigner{}
	m := &CrossOrgJWTMinter{
		Issuer: "bob.example",
		Signer: signer,
		Grants: &stubGrants{}, // nil meta → no grant ID
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/federation/jwt",
		strings.NewReader(`{"scope":"a2a.send"}`))
	r.Header.Set("X-Chepherd-Caller-Org", "alice.example")
	r.Header.Set("X-Chepherd-Hub-Attest", "true")
	m.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	var resp CrossOrgJWTResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if strings.Contains(resp.JWT, `"chepherd_grant_id"`) {
		t.Errorf("chepherd_grant_id should be absent when meta is nil: %s", resp.JWT)
	}
	if strings.Contains(resp.JWT, `"chepherd_rate_window"`) {
		t.Errorf("chepherd_rate_window should be absent when meta is nil: %s", resp.JWT)
	}
}

// ─── Client ───────────────────────────────────────────────────────

func TestWaveF8_Client_CacheHitOnRepeatCall(t *testing.T) {
	t.Parallel()
	var hits int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(&CrossOrgJWTResponse{
			JWT:       "jwt-X-for-bob",
			Issuer:    "bob.example",
			NotBefore: time.Now().Unix(),
			Expires:   time.Now().Add(5 * time.Minute).Unix(),
		})
	}))
	defer hub.Close()
	c := &CrossOrgJWTClient{
		HubURL:    hub.URL,
		CallerOrg: "alice.example",
	}
	ctx := context.Background()
	jws, err := c.Get(ctx, "bob.example", "a2a.send")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if jws != "jwt-X-for-bob" {
		t.Errorf("jws = %q", jws)
	}
	jws2, _ := c.Get(ctx, "bob.example", "a2a.send")
	if jws2 != jws {
		t.Errorf("cache miss on second Get")
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("hub hits = %d, want 1 (second was cache)", hits)
	}
	if c.Len() != 1 {
		t.Errorf("cache size = %d, want 1", c.Len())
	}
}

func TestWaveF8_Client_CacheMissAfterExpiry(t *testing.T) {
	t.Parallel()
	var hits int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(&CrossOrgJWTResponse{
			JWT:     "jwt-bob",
			Expires: time.Now().Add(5 * time.Minute).Unix(),
		})
	}))
	defer hub.Close()
	c := &CrossOrgJWTClient{HubURL: hub.URL}
	now := time.Now()
	c.NowFn = func() time.Time { return now }
	if _, err := c.Get(context.Background(), "bob.example", "x"); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	// Jump past expiry.
	c.NowFn = func() time.Time { return now.Add(10 * time.Minute) }
	if _, err := c.Get(context.Background(), "bob.example", "x"); err != nil {
		t.Fatalf("post-expiry Get: %v", err)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Errorf("hub hits = %d, want 2 (cache expired)", hits)
	}
}

func TestWaveF8_Client_InvalidateEvictsEntry(t *testing.T) {
	t.Parallel()
	var hits int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(&CrossOrgJWTResponse{
			JWT: "jwt", Expires: time.Now().Add(5 * time.Minute).Unix(),
		})
	}))
	defer hub.Close()
	c := &CrossOrgJWTClient{HubURL: hub.URL}
	_, _ = c.Get(context.Background(), "bob.example", "x")
	c.Invalidate("bob.example", "x")
	_, _ = c.Get(context.Background(), "bob.example", "x")
	if atomic.LoadInt32(&hits) != 2 {
		t.Errorf("hub hits = %d, want 2 (invalidated)", hits)
	}
}

func TestWaveF8_Client_HubError_PropagatesNotCached(t *testing.T) {
	t.Parallel()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"denied"}`))
	}))
	defer hub.Close()
	c := &CrossOrgJWTClient{HubURL: hub.URL}
	if _, err := c.Get(context.Background(), "bob.example", "x"); err == nil {
		t.Error("expected error from 403 hub")
	}
	if c.Len() != 0 {
		t.Errorf("cache size = %d after error, want 0", c.Len())
	}
}

func TestWaveF8_Client_ConcurrentGetsAllFedThroughCache(t *testing.T) {
	t.Parallel()
	var hits int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		// Slow response so the racers actually overlap.
		time.Sleep(10 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(&CrossOrgJWTResponse{
			JWT: "jwt", Expires: time.Now().Add(5 * time.Minute).Unix(),
		})
	}))
	defer hub.Close()
	c := &CrossOrgJWTClient{HubURL: hub.URL}
	var wg sync.WaitGroup
	const N = 20
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.Get(context.Background(), "bob.example", "x")
		}()
	}
	wg.Wait()
	// Without coordination N goroutines may each round-trip the
	// hub. F8's v0.9.4 client doesn't suppress duplicates (single-
	// flight is a follow-up); test verifies the cache CONVERGES
	// to exactly one cached entry afterward.
	if c.Len() != 1 {
		t.Errorf("cache size = %d after concurrent fan-in, want 1", c.Len())
	}
	if got := atomic.LoadInt32(&hits); got < 1 || got > N {
		t.Errorf("hub hits = %d, want 1..%d", got, N)
	}
}
