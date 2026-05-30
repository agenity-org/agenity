// internal/a2a/jwks.go — v0.9.3 #225 row B2. Serves the JWKS
// document at /.well-known/jwks.json so peers can verify JWTs signed
// by this chepherd instance without out-of-band public-key sharing.
//
// The JWKS body is passed in as a pre-marshalled byte slice (built
// in internal/auth so this package doesn't need to know about ECDSA).
// Pass nil to skip the endpoint.
//
// Refs #225 row B2.
package a2a

import "net/http"

// JWKSPath is the canonical JWKS endpoint per RFC 8414.
const JWKSPath = "/.well-known/jwks.json"

// ServeJWKS returns an http.HandlerFunc that responds with the given
// JWKS document as application/json. Cache-Control is set to a short
// TTL so peers can refresh the key promptly during rotation events.
func ServeJWKS(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(body)
	}
}
