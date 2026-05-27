// Package runtimehttp — Team Template Registry HTTP endpoints
// (#175, re-spec'd 2026-05-27 on Skill Library #194).
//
// Endpoints (separate namespace from the legacy /api/v1/templates
// YAML-apply path which serves runtime team-spawn):
//
//	GET    /api/v1/team-templates              list (?visible=true for Stage 1)
//	POST   /api/v1/team-templates              create user-defined
//	GET    /api/v1/team-templates/{id}         fetch one
//	PATCH  /api/v1/team-templates/{id}         update (builtins → 403)
//	DELETE /api/v1/team-templates/{id}         delete (builtins → 403)
//	POST   /api/v1/team-templates/{id}/visibility  toggle Visible (allowed on builtins)
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
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "template registry not initialised"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		opts := templateregistry.ListOpts{
			VisibleOnly: r.URL.Query().Get("visible") == "true",
		}
		all, err := s.templates.List(opts)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"templates": all})
	case http.MethodPost:
		var t templateregistry.Template
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json: " + err.Error()})
			return
		}
		actor := r.Header.Get("X-Operator-Id")
		if actor == "" {
			actor = "anonymous"
		}
		created, err := s.templates.Create(t, actor)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) teamTemplateByID(w http.ResponseWriter, r *http.Request) {
	if s.templates == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "template registry not initialised"})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/team-templates/")
	if rest == "" {
		s.teamTemplatesRoot(w, r)
		return
	}
	// Sub-resource: /api/v1/team-templates/{id}/visibility
	if strings.HasSuffix(rest, "/visibility") {
		id := strings.TrimSuffix(rest, "/visibility")
		s.teamTemplateSetVisibility(w, r, id)
		return
	}
	id := rest
	switch r.Method {
	case http.MethodGet:
		t, err := s.templates.Get(id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if t == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found", "id": id})
			return
		}
		writeJSON(w, http.StatusOK, t)
	case http.MethodPatch:
		var patch templateregistry.Template
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json: " + err.Error()})
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
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) teamTemplateSetVisibility(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		Visible bool `json:"visible"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json: " + err.Error()})
		return
	}
	if err := s.templates.SetVisibility(id, body.Visible); err != nil {
		writeTemplateErr(w, err)
		return
	}
	t, _ := s.templates.Get(id)
	writeJSON(w, http.StatusOK, t)
}

func writeTemplateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, templateregistry.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
	case errors.Is(err, templateregistry.ErrReadOnly):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}
