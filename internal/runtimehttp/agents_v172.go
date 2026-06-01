// Package runtimehttp — first-class Agent entity HTTP handlers (#172).
//
// Endpoints:
//
//	GET    /api/v1/agents          list (filters: operator, agent_type, deleted)
//	POST   /api/v1/agents          create (mints UUID, provisions PVC handle)
//	GET    /api/v1/agents/         curated directory of live runners,
//	                               v0.9.4 §12.2 shape (#467 Wave D1)
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

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/agent"
	"github.com/chepherd/chepherd/internal/runtime"
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
// When the path is bare /api/v1/agents/ (no id), it delegates to
// agentsDirectory to serve the v0.9.4 §12.2 curated directory.
func (s *Server) agentEntityByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
	idStr := strings.SplitN(path, "/", 2)[0]
	if idStr == "" {
		s.agentsDirectory(w, r)
		return
	}
	store := s.rt.AgentRegistry()
	if store == nil {
		http.Error(w, "agent registry not initialised", http.StatusInternalServerError)
		return
	}
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
		// label and skills are the only mutable fields. (#194 added
		// skills.) Reject any other field with 400.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		for k := range body {
			if k != "label" && k != "skills" {
				http.Error(w, "field '"+k+"' is immutable; only 'label' and 'skills' may be patched", http.StatusBadRequest)
				return
			}
		}
		if v, ok := body["label"]; ok {
			label, isStr := v.(string)
			if !isStr || label == "" {
				http.Error(w, "label must be a non-empty string", http.StatusBadRequest)
				return
			}
			if err := store.SetLabel(id, label); err != nil {
				http.Error(w, "set-label: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if v, ok := body["skills"]; ok {
			arr, isArr := v.([]any)
			if !isArr {
				http.Error(w, "skills must be a string array", http.StatusBadRequest)
				return
			}
			ids := make([]string, 0, len(arr))
			for _, e := range arr {
				s, isStr := e.(string)
				if !isStr {
					http.Error(w, "skills entries must be strings (skill IDs)", http.StatusBadRequest)
					return
				}
				ids = append(ids, s)
			}
			if err := store.SetSkills(id, ids); err != nil {
				http.Error(w, "set-skills: "+err.Error(), http.StatusInternalServerError)
				return
			}
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

// directoryEntry is the v0.9.4 §12.2 wire shape for a single agent.
// JSON field names are spec-frozen — do NOT rename without an ADR.
type directoryEntry struct {
	SID          string `json:"sid"`
	Name         string `json:"name"`
	AgentCardURL string `json:"agent_card_url"`
}

// agentsDirectory serves GET /api/v1/agents/ per V0.9.2-ARCHITECTURE.md
// §12.2: a curated directory of all live runners in this daemon's org.
//
// Source: today's live session registry (rt.List()). When Wave R6/R7
// lands the runner-WS-registration table, the data source switches
// server-side without changing the wire shape.
//
// agent_card_url is a stub URL templated per §12.1 well-known URI
// pattern, using this daemon as a stand-in until Wave R3 ships
// per-session Agent Cards on the runner processes themselves. The
// URL shape is stable; only the host segment migrates.
//
// Refs #467.
func (s *Server) agentsDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	host := r.Host
	entries := []directoryEntry{}
	if s.rt != nil {
		for _, info := range s.rt.List() {
			if info.Exited {
				continue
			}
			entries = append(entries, directoryEntry{
				SID:          info.ID,
				Name:         info.Name,
				AgentCardURL: scheme + "://" + host + "/a2a/" + info.ID + a2a.AgentCardPath,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": entries})
}

// a2aSessionCardHandler serves GET /a2a/<sid>/.well-known/agent-card.json.
// The D1 directory advertises these URLs; the daemon serves the card from
// its runtime session index since sibling-container runners don't expose
// their own HTTP listener. Fixes #650.
func (s *Server) a2aSessionCardHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	// Path: /a2a/<sid>/<subpath>
	// Strip leading "/a2a/" prefix and split on first "/"
	rest := strings.TrimPrefix(r.URL.Path, "/a2a/")
	slash := strings.Index(rest, "/")
	if slash < 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	sid := rest[:slash]
	sub := rest[slash:]
	if sub != a2a.AgentCardPath && sub != a2a.AgentCardAliasPath {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found", "path": r.URL.Path})
		return
	}
	if s.rt == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "runtime not available"})
		return
	}
	_, info := s.rt.GetByContextID(sid)
	if info == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such session", "sid": sid})
		return
	}
	writeJSON(w, http.StatusOK, runtime.BuildPeerAgentCard(info))
}
