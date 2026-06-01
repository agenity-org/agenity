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

func (a *crossOrgGrantAdapter) Check(ctx context.Context, callerOrg, scope string) (*federation.GrantMeta, error) {
	if a.check != nil {
		if err := a.check(callerOrg, scope); err != nil {
			return nil, err
		}
		// Authorization passed. Query the store for the matching grant's
		// metadata (grant ID + rate window) so the minter can embed the
		// §15.2 claims. Store may be nil in dev mode → nil meta is safe.
		if a.store != nil {
			return grantMetaFor(ctx, a.store, callerOrg)
		}
		return nil, nil
	}
	if a.store == nil {
		// No store → permissive (matches the F8 minter's behavior
		// when Grants is nil: every authenticated caller passes).
		// Production deploys MUST wire a store + check function.
		return nil, nil
	}
	return grantMetaFor(ctx, a.store, callerOrg)
}

// grantMetaFor looks up the first active grant from granteeOrg in the
// store and returns its metadata for embedding as §15.2 JWT claims.
// A lookup error or empty result is non-fatal (caller is still authorized);
// we return nil meta so the JWT is issued without grant claims.
func grantMetaFor(ctx context.Context, store persistence.RBACGrantRepository, granteeOrg string) (*federation.GrantMeta, error) {
	grants, err := store.List(ctx, persistence.GrantListOpts{
		GranteeOrg: granteeOrg,
		OnlyActive: true,
	})
	if err != nil || len(grants) == 0 {
		return nil, nil
	}
	g := grants[0]
	meta := &federation.GrantMeta{GrantID: g.ID}
	if g.RateLimit != nil {
		meta.RateWindow = &federation.RateWindow{
			CallsPerMinute: g.RateLimit.CallsPerMinute,
			CallsPerDay:    g.RateLimit.CallsPerDay,
		}
	}
	return meta, nil
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
	}
	minter := &federation.CrossOrgJWTMinter{
		Issuer: s.OrgID,
		Signer: s.KeyStore,
		Grants: grants,
	}
	mux.Handle("/api/v1/federation/jwt", minter)
}
