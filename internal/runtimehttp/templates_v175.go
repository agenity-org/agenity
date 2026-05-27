// Package runtimehttp — Team Template Registry HTTP endpoints (#175).
//
// Endpoints:
//
//	GET    /api/v1/team-templates              list builtins + user-defined
//	POST   /api/v1/team-templates              create user-defined
//	GET    /api/v1/team-templates/{id}         fetch one
//	PATCH  /api/v1/team-templates/{id}         update (builtins → 403)
//	DELETE /api/v1/team-templates/{id}         delete (builtins → 403)
//
// Distinct from the legacy /api/v1/templates which serves YAML catalog
// profiles (2pizza / council / etc — runtime team-apply path).
package runtimehttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/chepherd/chepherd/internal/templateregistry"
)

func (s *Server) teamTemplatesRoot(w http.ResponseWriter, r *http.Request) {
	if s.templates == nil {
		http.Error(w, "template registry not initialised", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		all, err := s.templates.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"templates": all})
	case http.MethodPost:
		var t templateregistry.Template
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		created, err := s.templates.Create(t, operatorFromHeader(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) teamTemplateByID(w http.ResponseWriter, r *http.Request) {
	if s.templates == nil {
		http.Error(w, "template registry not initialised", http.StatusInternalServerError)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/team-templates/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		t, err := s.templates.Get(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if t == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, t)
	case http.MethodPatch:
		var patch templateregistry.Template
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		updated, err := s.templates.Update(id, patch)
		if err != nil {
			writeTemplateErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		if err := s.templates.Delete(id); err != nil {
			writeTemplateErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeTemplateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, templateregistry.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, templateregistry.ErrReadOnly):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// operatorFromHeader is the lightweight stand-in until #173's
// X-Operator-Id lands here (the discovery layer + handoff branches both
// use it but each branch is independent of the others).
func operatorFromHeader(r *http.Request) string {
	if v := r.Header.Get("X-Operator-Id"); v != "" {
		return v
	}
	return "anonymous"
}
