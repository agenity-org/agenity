package a2a

import "net/http"

// RegisterRoutes wires the A2A scaffold's two HTTP endpoints onto
// the given mux:
//
//   - GET /.well-known/agent-card.json → ServeAgentCard
//   - POST /jsonrpc                    → router.ServeHTTP
//
// Callers (runner-as-process / runner-as-pod) provide both their
// AgentCard + Router and pass the resulting mux to http.Serve.
//
// Refs #208.
func RegisterRoutes(mux *http.ServeMux, card *AgentCard, router *Router) {
	mux.Handle(AgentCardPath, ServeAgentCard(card))
	mux.Handle("/jsonrpc", router)
}
