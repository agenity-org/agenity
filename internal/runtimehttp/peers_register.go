// Package runtimehttp — Federation peer-registration endpoints (#669).
//
// Routes (mounted by Server.Handler):
//
//	POST   /api/v1/peers/register           — register an external A2A peer
//	POST   /api/v1/peers/{name}/heartbeat   — extend a peer's TTL
//	DELETE /api/v1/peers/{name}             — deregister a peer
//	GET    /api/v1/peers/registered         — list currently-registered peers
//
// These complement the existing GET /api/v1/peers (federated agent-card
// cache from the federation registry, #225 row C1) — registered peers
// are a separate concept: external A2A endpoints that have voluntarily
// joined a chepherd team as first-class members so @everyone broadcasts
// can reach them via HTTP POST to /jsonrpc (vs PTY write for chepherd-
// managed sessions).
//
// Wire format (POST /api/v1/peers/register):
//
//	{ "name": "external-peer-1", "team": "default",
//	  "agent_card_url": "http://peer:8080/.well-known/agent-card.json" }
//
// JSONRPCURL is derived server-side by replacing the agent-card path
// with /jsonrpc, matching the A2A v1.0 convention (peer advertises its
// JSON-RPC endpoint at the same host as its agent card).
//
// Refs #669.
package runtimehttp

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/agenity-org/agenity/internal/a2a"
	"github.com/agenity-org/agenity/internal/runtime"
)

// peerRegisterRequest is the JSON body of POST /api/v1/peers/register.
// Name + Team + AgentCardURL are required; missing/empty fields produce
// a 400 response.
type peerRegisterRequest struct {
	Name         string `json:"name"`
	Team         string `json:"team"`
	AgentCardURL string `json:"agent_card_url"`
}

// peersRegisterHandler dispatches POST /api/v1/peers/register. Returns
// 201 + the registered peer record on success, 400 on malformed input,
// 503 when the runtime isn't wired.
func (s *Server) peersRegisterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if s.rt == nil || s.rt.Peers() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "runtime not available"})
		return
	}
	var req peerRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "decode: " + err.Error()})
		return
	}
	name := strings.TrimSpace(req.Name)
	team := strings.TrimSpace(req.Team)
	cardURL := strings.TrimSpace(req.AgentCardURL)
	if name == "" || team == "" || cardURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "name, team, and agent_card_url are required",
		})
		return
	}
	jsonrpcURL, err := deriveJSONRPCURL(cardURL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid agent_card_url: " + err.Error(),
		})
		return
	}
	p := s.rt.Peers().Register(name, team, cardURL, jsonrpcURL)
	writeJSON(w, http.StatusCreated, p)
}

// peersByNameHandler dispatches DELETE /api/v1/peers/{name} +
// POST /api/v1/peers/{name}/heartbeat. The list endpoint
// (GET /api/v1/peers/registered) is matched by exact suffix first so
// "registered" isn't treated as a peer name.
func (s *Server) peersByNameHandler(w http.ResponseWriter, r *http.Request) {
	if s.rt == nil || s.rt.Peers() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "runtime not available"})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/peers/")
	// Exact match for the list-registered endpoint.
	if rest == "registered" {
		s.peersRegisteredList(w, r)
		return
	}
	// {name} / {name}/heartbeat
	parts := strings.SplitN(rest, "/", 2)
	name := strings.TrimSpace(parts[0])
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "peer name required"})
		return
	}
	if len(parts) == 2 && parts[1] == "heartbeat" {
		s.peerHeartbeat(w, r, name)
		return
	}
	if len(parts) == 2 && parts[1] != "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown peer sub-path", "path": r.URL.Path})
		return
	}
	// Bare /api/v1/peers/{name} — only DELETE is meaningful here.
	switch r.Method {
	case http.MethodDelete:
		s.peerDeregister(w, r, name)
	case http.MethodGet:
		// Convenience: GET one peer by name returns its record (or 404).
		p, ok := s.rt.Peers().Get(name)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "peer not registered", "name": name})
			return
		}
		writeJSON(w, http.StatusOK, p)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

// peerHeartbeat handles POST /api/v1/peers/{name}/heartbeat. Returns
// 204 on success, 404 when the peer isn't registered.
func (s *Server) peerHeartbeat(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if !s.rt.Peers().Heartbeat(name) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "peer not registered", "name": name})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// peerDeregister handles DELETE /api/v1/peers/{name}. Returns 204 on
// success, 404 when the peer wasn't registered.
func (s *Server) peerDeregister(w http.ResponseWriter, r *http.Request, name string) {
	if !s.rt.Peers().Deregister(name) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "peer not registered", "name": name})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// peersRegisteredList handles GET /api/v1/peers/registered. Returns the
// full snapshot of currently-registered peers (TTL-swept).
func (s *Server) peersRegisteredList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	peers := s.rt.Peers().List()
	if peers == nil {
		peers = []runtime.PeerInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": peers})
}

// deriveJSONRPCURL converts an agent-card URL into the peer's JSON-RPC
// endpoint URL by replacing the well-known path with /jsonrpc. Matches
// the A2A v1.0 convention (peer serves its agent card + jsonrpc on the
// same host).
//
// Inputs:
//
//	http://peer:8080/.well-known/agent-card.json → http://peer:8080/jsonrpc
//	http://peer:8080/agent-card.json             → http://peer:8080/jsonrpc
//	http://peer:8080/                            → http://peer:8080/jsonrpc
//	http://peer:8080                             → http://peer:8080/jsonrpc
func deriveJSONRPCURL(cardURL string) (string, error) {
	u, err := url.Parse(cardURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errInvalidURL
	}
	u.Path = "/jsonrpc"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// errInvalidURL is returned by deriveJSONRPCURL when the parsed URL
// lacks scheme/host (typically a bare path was passed).
var errInvalidURL = &urlErr{"missing scheme or host"}

type urlErr struct{ msg string }

func (e *urlErr) Error() string { return e.msg }

// compile-time imports for the package — keeps go vet from flagging the
// indirect import as unused when this file's main code path doesn't
// reach into a2a directly.
var _ = a2a.AgentCardPath
