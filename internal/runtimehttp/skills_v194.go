// Package runtimehttp — Skill Library HTTP endpoints (#194).
//
// Endpoints:
//
//	GET    /api/v1/skills              list (?tag= ?compat=)
//	POST   /api/v1/skills              create user-defined
//	GET    /api/v1/skills/{id}         fetch one
//	PATCH  /api/v1/skills/{id}         update (builtins → 403)
//	DELETE /api/v1/skills/{id}         delete (builtins → 403)
//
// Import endpoint (POST /api/v1/skills/import?from=gstack|agent-skills|ECC)
// is scaffolded as a stub returning 501 — real importers land in a
// follow-up; v0.9 ships 12 builtins as the curated set.
package runtimehttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/chepherd/chepherd/internal/skills"
)

func (s *Server) skillsRoot(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "skill registry not initialised"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		opts := skills.ListOpts{
			Tag:    r.URL.Query().Get("tag"),
			Compat: r.URL.Query().Get("compat"),
		}
		all, err := s.skills.List(opts)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"skills": all})
	case http.MethodPost:
		var sk skills.Skill
		if err := json.NewDecoder(r.Body).Decode(&sk); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json: " + err.Error()})
			return
		}
		created, err := s.skills.Create(sk)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) skillByID(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "skill registry not initialised"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/skills/")
	// Bare /api/v1/skills/ with no id — same as the root.
	if id == "" {
		s.skillsRoot(w, r)
		return
	}
	// Special-case the import endpoint.
	if id == "import" {
		s.skillsImport(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		sk, err := s.skills.Get(id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if sk == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "skill not found", "id": id})
			return
		}
		writeJSON(w, http.StatusOK, sk)
	case http.MethodPatch:
		var patch skills.Skill
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json: " + err.Error()})
			return
		}
		updated, err := s.skills.Update(id, patch)
		if err != nil {
			writeSkillErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		if err := s.skills.Delete(id); err != nil {
			writeSkillErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

// skillsImport — POST /api/v1/skills/import?from=gstack|agent-skills|ECC
// Stub for v0.9 (12 builtins are pre-seeded; bulk import is follow-up
// work — see #194 acceptance "if any fail to parse cleanly").
func (s *Server) skillsImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	from := r.URL.Query().Get("from")
	if from == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "?from= required (gstack|agent-skills|ECC|url)"})
		return
	}
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"error":  "skill import is a follow-up; 12 builtins ship pre-seeded",
		"from":   from,
		"status": "stub",
	})
}

func writeSkillErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, skills.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
	case errors.Is(err, skills.ErrReadOnly):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}
