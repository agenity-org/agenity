// Package runtimehttp — operator-to-operator handoff endpoints (#173).
//
// Endpoints:
//
//	POST   /api/v1/agents/{id}/handoff           request handoff to another operator
//	POST   /api/v1/agents/{id}/release           voluntarily release (also accepts force-release flag)
//	POST   /api/v1/agents/{id}/bind              accept a pending handoff (or bind unbound agent)
//	GET    /api/v1/agents/{id}/handoffs          audit log
//	POST   /api/v1/admin/agents/{id}/force-release  admin-only forced release
//
// Operator identity model (interim): each request carries an
// X-Operator-Id header. When absent, falls back to a sentinel
// "legacy spawner" operator-id so single-operator deployments keep
// working. A full Operator entity lands in a later epic.
package runtimehttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/chepherd/chepherd/internal/agent"
)

// operatorFromRequest extracts the caller's operator-id from
// X-Operator-Id header. Falls back to the legacy-spawner sentinel
// (matches runtime.legacySpawnerOperator) so existing single-operator
// instances don't break.
func operatorFromRequest(r *http.Request) uuid.UUID {
	if h := r.Header.Get("X-Operator-Id"); h != "" {
		if id, err := uuid.Parse(h); err == nil {
			return id
		}
	}
	// Same sentinel as runtime.legacySpawnerOperator.
	return uuid.MustParse("00000000-0000-0000-0000-000000000001")
}

// handoffRouter dispatches /api/v1/agents/{id}/{action} for the four
// handoff actions. Falls through to the entity router for non-handoff
// paths.
func (s *Server) handoffRouter(w http.ResponseWriter, r *http.Request) bool {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return false
	}
	idStr, action := parts[0], parts[1]
	id, err := uuid.Parse(idStr)
	if err != nil {
		return false
	}
	switch action {
	case "handoff":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.handoffRequest(w, r, id)
		return true
	case "release":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.handoffRelease(w, r, id, nil)
		return true
	case "bind":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.handoffBind(w, r, id)
		return true
	case "handoffs":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.handoffAuditLog(w, r, id)
		return true
	}
	return false
}

func (s *Server) handoffRequest(w http.ResponseWriter, r *http.Request, agentID uuid.UUID) {
	store := s.rt.AgentRegistry()
	var body struct {
		To     string `json:"to"`
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	to, err := uuid.Parse(body.To)
	if err != nil {
		http.Error(w, "to must be a valid uuid", http.StatusBadRequest)
		return
	}
	requester := operatorFromRequest(r)
	ev, err := store.RequestHandoff(agentID, requester, to, body.Reason)
	if err != nil {
		writeHandoffError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, ev)
}

func (s *Server) handoffRelease(w http.ResponseWriter, r *http.Request, agentID uuid.UUID, forcedBy *uuid.UUID) {
	store := s.rt.AgentRegistry()
	actor := operatorFromRequest(r)
	if err := store.Release(agentID, actor, forcedBy); err != nil {
		writeHandoffError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handoffBind(w http.ResponseWriter, r *http.Request, agentID uuid.UUID) {
	store := s.rt.AgentRegistry()
	binder := operatorFromRequest(r)
	if err := store.Bind(agentID, binder); err != nil {
		writeHandoffError(w, err)
		return
	}
	a, _ := store.Get(agentID)
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handoffAuditLog(w http.ResponseWriter, _ *http.Request, agentID uuid.UUID) {
	store := s.rt.AgentRegistry()
	events, err := store.HandoffEvents(agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"handoffs": events})
}

// adminForceRelease — POST /api/v1/admin/agents/{id}/force-release.
// Bypasses the requester-equals-current-operator check, attributes the
// forced release to the caller's operator-id in the audit log.
func (s *Server) adminForceRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/agents/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[1] != "force-release" {
		http.NotFound(w, r)
		return
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		http.Error(w, "invalid agent id", http.StatusBadRequest)
		return
	}
	admin := operatorFromRequest(r)
	s.handoffRelease(w, r, id, &admin)
}

func writeHandoffError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, agent.ErrSelfHandoff), errors.Is(err, agent.ErrInvalidState):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, agent.ErrForbidden):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
