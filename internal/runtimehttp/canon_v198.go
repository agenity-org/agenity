// Canon HTTP API — Layer 1 of the 3-layer agent context model (#198).
//
// Routes:
//
//	GET    /api/v1/canon            → current canon (always returns 200, version=0 if unset)
//	PUT    /api/v1/canon            → replace body; bumps version; archives prior
//	GET    /api/v1/canon/history    → list prior versions newest-first (?limit=N)
//	POST   /api/v1/canon/rollback   → restore a prior version as current
//
// Canon is a singleton (id="default") stored under
// $stateDir/canon/. Mutations are operator-only and auth is enforced
// by the same Bearer-token middleware as the rest of /api/v1/*.
package runtimehttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/agenity-org/agenity/internal/canon"
)

// canonRoot — GET / PUT on the singleton canon record.
func (s *Server) canonRoot(w http.ResponseWriter, r *http.Request) {
	if s.canon == nil {
		http.Error(w, "canon store not initialised", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		c, err := s.canon.Get()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, c)
	case http.MethodPut:
		var body struct {
			Body      string `json:"body"`
			Title     string `json:"title,omitempty"`
			UpdatedBy string `json:"updated_by,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		actor := body.UpdatedBy
		if actor == "" {
			actor = "operator"
		}
		next, err := s.canon.Put(body.Body, actor, body.Title)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, next)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// canonHistory — GET /api/v1/canon/history?limit=N
func (s *Server) canonHistory(w http.ResponseWriter, r *http.Request) {
	if s.canon == nil {
		http.Error(w, "canon store not initialised", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	hist, err := s.canon.History(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, hist)
}

// canonRollback — POST /api/v1/canon/rollback {to_version, actor}
func (s *Server) canonRollback(w http.ResponseWriter, r *http.Request) {
	if s.canon == nil {
		http.Error(w, "canon store not initialised", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ToVersion int    `json:"to_version"`
		Actor     string `json:"actor,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.ToVersion <= 0 {
		http.Error(w, "to_version must be positive", http.StatusBadRequest)
		return
	}
	actor := body.Actor
	if actor == "" {
		actor = "operator"
	}
	next, err := s.canon.Rollback(body.ToVersion, actor)
	if err != nil {
		if errors.Is(err, canon.ErrVersionNotFound) {
			http.Error(w, "version not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, next)
}
