// Package runtimehttp — first-class Agent entity HTTP handlers (#172).
//
// Endpoints:
//
//	GET    /api/v1/agents          list (filters: operator, agent_type, deleted)
//	POST   /api/v1/agents          create (mints UUID, provisions PVC handle)
//	GET    /api/v1/agents/{id}     fetch one
//	PATCH  /api/v1/agents/{id}     update — label is the ONLY mutable field
//	DELETE /api/v1/agents/{id}     soft-delete (PVC retained 7d)
//
// Auth: routed through the same authMiddleware (Bearer token) as every
// other /api/v1/* surface — no extra checks here.
package runtimehttp

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/chepherd/chepherd/internal/agent"
)

// agentsEntity handles the collection — list + create.
func (s *Server) agentsEntity(w http.ResponseWriter, r *http.Request) {
	store := s.rt.AgentRegistry()
	if store == nil {
		http.Error(w, "agent registry not initialised", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		opts := agent.ListOpts{}
		q := r.URL.Query()
		if q.Get("include_deleted") == "true" {
			opts.IncludeDeleted = true
		}
		if t := q.Get("agent_type"); t != "" {
			opts.AgentType = t
		}
		if op := q.Get("operator"); op != "" {
			if id, err := uuid.Parse(op); err == nil {
				opts.Operator = &id
			}
		}
		list, err := store.List(opts)
		if err != nil {
			http.Error(w, "list: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"agents": list})
	case http.MethodPost:
		// Body: {agent_type, label?, creator_account?}. Mints UUID +
		// PVC handle but does NOT provision the actual volume — that
		// happens on first Spawn() so reservation is free.
		var body struct {
			AgentType      string `json:"agent_type"`
			Label          string `json:"label"`
			CreatorAccount string `json:"creator_account"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.AgentType == "" {
			http.Error(w, "agent_type required", http.StatusBadRequest)
			return
		}
		a := agent.New(body.AgentType, body.Label, body.CreatorAccount)
		if err := store.Save(a); err != nil {
			http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, a)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// agentEntityByID handles single-record routes: /api/v1/agents/{id}.
func (s *Server) agentEntityByID(w http.ResponseWriter, r *http.Request) {
	store := s.rt.AgentRegistry()
	if store == nil {
		http.Error(w, "agent registry not initialised", http.StatusInternalServerError)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
	idStr := strings.SplitN(path, "/", 2)[0]
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid agent id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		a, err := store.Get(id)
		if err != nil {
			http.Error(w, "get: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if a == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, a)
	case http.MethodPatch:
		// Only label is mutable. Reject any other field with 400.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		for k := range body {
			if k != "label" {
				http.Error(w, "field '"+k+"' is immutable; only 'label' may be patched", http.StatusBadRequest)
				return
			}
		}
		label, ok := body["label"].(string)
		if !ok || label == "" {
			http.Error(w, "label must be a non-empty string", http.StatusBadRequest)
			return
		}
		if err := store.SetLabel(id, label); err != nil {
			http.Error(w, "set-label: "+err.Error(), http.StatusInternalServerError)
			return
		}
		a, _ := store.Get(id)
		writeJSON(w, http.StatusOK, a)
	case http.MethodDelete:
		if err := store.SoftDelete(id); err != nil {
			http.Error(w, "delete: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
