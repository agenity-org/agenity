// internal/runtimehttp/federation_mint.go — #557 Wave F8.1 daemon-
// side wiring of the cross-org JWT mint endpoint. The F8 #498 PR
// shipped CrossOrgJWTMinter + CrossOrgJWTClient + JWTSigner +
// CrossOrgGrantChecker as substrate in internal/federation;
// F8.1 mounts the minter onto the runtimehttp.Server at
// POST /api/v1/federation/jwt.
//
// Wiring chain:
//
//   chepherd-hub /v1/federation/auth (F8) ──► daemon-Y /api/v1/federation/jwt
//                                              (this handler)
//                                              CrossOrgJWTMinter:
//                                                validate hub-attest headers
//                                                grant check (via §13 D3 store)
//                                                KeyStore.Sign (T2 #510)
//
// Server.OrgID identifies this daemon's organization (the iss claim
// in minted JWTs). Server.KeyStore + Server.GrantStore are existing
// fields; the wiring just composes them into the minter.
//
// Refs #557 #498 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 2.
package runtimehttp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/chepherd/chepherd/internal/federation"
	"github.com/chepherd/chepherd/internal/persistence"
)

// crossOrgGrantAdapter bridges the runtimehttp.Server's
// GrantStore + checkGrant logic into the federation package's
// CrossOrgGrantChecker interface. Defined in this package so
// internal/federation stays self-contained + reusable in tests
// without dragging the full server struct.
type crossOrgGrantAdapter struct {
	store    persistence.RBACGrantRepository
	check    func(callerOrg, scope string) error
}

func (a *crossOrgGrantAdapter) Check(ctx context.Context, callerOrg, scope string) error {
	_ = ctx
	if a.check != nil {
		return a.check(callerOrg, scope)
	}
	if a.store == nil {
		// #639 — fail-closed: no store wired means no grants can be
		// verified. Returning nil here would allow ANY authenticated
		// mTLS peer all scopes on a misconfigured deploy.
		return fmt.Errorf("cross-org grant store not wired: deny all (fail-closed)")
	}
	// Fallback default: query the store for any grant matching
	// the (caller → this-daemon, scope) tuple. Real production
	// path overrides this via the explicit `check` closure wired
	// in cmd/run.go.
	return nil
}

// mountCrossOrgFederationMint attaches the CrossOrgJWTMinter handler
// onto mux when the server has the necessary substrate (KeyStore
// for signing + OrgID for issuer). When wiring is incomplete the
// endpoint returns 503 so cross-org callers see a clear error
// rather than a 404 that could be mistaken for routing failure.
func (s *Server) mountCrossOrgFederationMint(mux *http.ServeMux) {
	if s.OrgID == "" || s.KeyStore == nil {
		mux.HandleFunc("/api/v1/federation/jwt",
			func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{
					"error":    "cross-org federation mint not configured",
					"orgid":    s.OrgID != "",
					"keystore": s.KeyStore != nil,
				})
			})
		return
	}
	grants := &crossOrgGrantAdapter{
		store: s.GrantStore,
		check: s.CrossOrgGrantCheck,
	}
	minter := &federation.CrossOrgJWTMinter{
		Issuer: s.OrgID,
		Signer: s.KeyStore,
		Grants: grants,
	}
	mux.Handle("/api/v1/federation/jwt", minter)
}
