// internal/runtimehttp/jwks.go serves the v0.9.4 §15.2 + §22
// daemon-owned JWKS endpoint dynamically from the KeyStore (#505
// Wave T2). The static-body fallback in internal/a2a.RegisterJWKS
// stays for unit-test boots that don't wire a KeyStore.
//
// Refs #505.
package runtimehttp

import (
	"net/http"
)

// jwksDynamic serves /.well-known/jwks.json by re-marshalling the
// current KeyStore on every request. This is the correct shape for
// rotation: the moment Rotate() ships a new active key, the next
// GET /.well-known/jwks.json call reflects it without restart, and
// any retired-but-within-overlap keys keep appearing until they
// age out.
func (s *Server) jwksDynamic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.KeyStore == nil {
		http.Error(w, "keystore not initialised", http.StatusServiceUnavailable)
		return
	}
	body, err := s.KeyStore.JWKS()
	if err != nil {
		http.Error(w, "jwks: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Disable caching so peers re-fetch after rotation events. The
	// daemon's KeyStore is the single source of truth; a stale cached
	// JWKS would silently break verification across rotations.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}
