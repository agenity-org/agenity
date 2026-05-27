// Roles HTTP API — Layer 2 identity catalog of the 3-layer agent
// context model (#194 architect 2026-05-28 FINAL+).
//
// Routes:
//
//	GET    /api/v1/roles            → list (builtins sort_order asc, then user updated_at desc)
//	POST   /api/v1/roles            → create user-defined role
//	GET    /api/v1/roles/{id}       → get one role
//	PUT    /api/v1/roles/{id}       → update (builtins → 405)
//	DELETE /api/v1/roles/{id}       → delete (builtins → 405)
//
// Builtin role IDs are operator-immutable; user-defined roles get
// ID "user-{uuid}" minted server-side.
package runtimehttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/chepherd/chepherd/internal/roles"
)

// rolesRoot — list + create.
func (s *Server) rolesRoot(w http.ResponseWriter, r *http.Request) {
	if s.roles == nil {
		http.Error(w, "roles store not initialised", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		all, err := s.roles.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, all)
	case http.MethodPost:
		var body roles.Role
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		created, err := s.roles.Create(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// roleByID — get, update, delete a single role.
func (s *Server) roleByID(w http.ResponseWriter, r *http.Request) {
	if s.roles == nil {
		http.Error(w, "roles store not initialised", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/roles/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		role, err := s.roles.Get(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if role == nil {
			http.Error(w, "role not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, role)
	case http.MethodPut:
		var patch roles.Role
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		upd, err := s.roles.Update(id, patch)
		if err != nil {
			if errors.Is(err, roles.ErrReadOnly) {
				http.Error(w, "builtin role is read-only", http.StatusMethodNotAllowed)
				return
			}
			if errors.Is(err, roles.ErrNotFound) {
				http.Error(w, "role not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, upd)
	case http.MethodDelete:
		if err := s.roles.Delete(id); err != nil {
			if errors.Is(err, roles.ErrReadOnly) {
				http.Error(w, "builtin role is read-only", http.StatusMethodNotAllowed)
				return
			}
			if errors.Is(err, roles.ErrNotFound) {
				http.Error(w, "role not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
