package a2a

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// RegisterRoutes wires the A2A scaffold's HTTP endpoints onto the
// given mux:
//
//   - GET /.well-known/agent-card.json → ServeAgentCard
//   - POST /jsonrpc                    → router.ServeHTTP
//   - GET /a2a/stream/<streamID>       → broker.Handler (when broker != nil)
//
// When `authValidator` is non-nil, every POST /jsonrpc request is
// gated by Bearer-token validation: missing/invalid token returns
// HTTP 401 + JSON-RPC error code -32001 ("authentication required").
// Pass nil to skip enforcement (back-compat for dev mode).
//
// When `broker` is non-nil, /a2a/stream/<streamID> is wired for SSE
// streaming of task state transitions (#225 row A2). nil disables
// the stream endpoint — callers of SendStreamingMessage /
// ResubscribeTask will receive -32004 from the method bodies (which
// is the same state as before A2 landed).
//
// Refs #208 #225 row B1 row A2.
func RegisterRoutes(mux *http.ServeMux, card *AgentCard, router *Router, authValidator TokenValidator, broker *StreamBroker) {
	mux.Handle(AgentCardPath, ServeAgentCard(card))
	if authValidator == nil {
		mux.Handle("/jsonrpc", router)
	} else {
		mux.Handle("/jsonrpc", AuthMiddleware(router, authValidator))
	}
	if broker != nil {
		mux.Handle("/a2a/stream/", broker.Handler())
	}
}

// RegisterJWKS publishes the JWKS document at /.well-known/jwks.json
// so peers can verify JWTs signed by this chepherd instance without
// out-of-band public-key sharing (v0.9.3 #225 row B2). Caller supplies
// the marshalled JSON (built in internal/auth via PublicJWK so this
// package stays ECDSA-free). Empty body skips the endpoint.
func RegisterJWKS(mux *http.ServeMux, jwksBody []byte) {
	if len(jwksBody) == 0 {
		return
	}
	mux.HandleFunc(JWKSPath, ServeJWKS(jwksBody))
}

// TokenValidator is the minimal seam between RegisterRoutes and an
// AuthProvider. Defined here so internal/a2a doesn't import
// internal/auth (cyclic dep — auth depends on persistence which
// indirectly references a2a via the runtime layer). The caller wraps
// its AuthProvider.Validate as a TokenValidator before passing in.
type TokenValidator interface {
	Validate(ctx context.Context, token string) (subject string, err error)
}

// AuthMiddleware enforces Bearer-token presence + validity on every
// request before the wrapped handler runs. On failure it writes:
//
//	HTTP/1.1 401 Unauthorized
//	Content-Type: application/json
//	{"jsonrpc":"2.0","error":{"code":-32001,"message":"authentication required"}}
//
// per the A2A v1.0 spec's auth-failure shape.
//
// Refs #225 row B1.
func AuthMiddleware(next http.Handler, validator TokenValidator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		token := extractBearer(req)
		if token == "" {
			writeAuthError(w, "authentication required: missing or malformed Authorization header (expected 'Bearer <token>')")
			return
		}
		subject, err := validator.Validate(req.Context(), token)
		if err != nil || subject == "" {
			msg := "authentication failed"
			if err != nil {
				msg = "authentication failed: " + err.Error()
			}
			writeAuthError(w, msg)
			return
		}
		// Attach subject to request context so downstream handlers (the
		// JSON-RPC method bodies, later RBAC checks) can read it without
		// re-parsing the Authorization header.
		ctx := context.WithValue(req.Context(), authSubjectCtxKey{}, subject)
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

type authSubjectCtxKey struct{}

// SubjectFromContext returns the authenticated subject set by
// AuthMiddleware, or empty string when the request was not
// authenticated (dev mode / TokenValidator nil at registration).
func SubjectFromContext(ctx context.Context) string {
	v, _ := ctx.Value(authSubjectCtxKey{}).(string)
	return v
}

func extractBearer(req *http.Request) string {
	h := req.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 {
		return ""
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func writeAuthError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="chepherd-a2a"`)
	w.WriteHeader(http.StatusUnauthorized)
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		Error: &JSONRPCError{
			Code:    -32001,
			Message: msg,
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
