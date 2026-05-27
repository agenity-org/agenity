// Package runtimehttp — Discovery Layer HTTP endpoints (#174).
//
// Endpoints:
//
//	GET   /api/v1/discovery/{token-id}          cached tree (auto-fetch on miss)
//	POST  /api/v1/discovery/{token-id}/refresh  force re-fetch
//	GET   /api/v1/discovery/{token-id}/repos    ?q= server-side repo search
//
// The {token-id} corresponds to a saved git-provider record in chepherd
// state. We look the secret up out of LoadGitProviders (which decrypts
// the TokenCipher) so secrets never cross the API boundary again.
package runtimehttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/chepherd/chepherd/internal/discovery"
	"github.com/chepherd/chepherd/internal/runtime"
)

// discoveryRouter dispatches /api/v1/discovery/{token-id}/* paths.
//
// Per #195 contract: {token-id} is the opaque vault entry ID of the
// saved git provider record (URL-safe by construction — UUID or short
// slug like "embedded"). UI MUST resolve to a vault UUID before
// calling; never pass a "provider:URL" composite (PR #185 regression).
//
// Empty token-id, ":" / "/" inside token-id, or unknown ID all return
// structured JSON 400/404 — never fall through to the global "unknown
// API path" 404.
func (s *Server) discoveryRouter(w http.ResponseWriter, r *http.Request) {
	if s.discovery == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "discovery service not initialised"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/discovery/")
	action := ""
	tokenID := ""
	switch {
	case strings.HasSuffix(path, "/refresh"):
		action = "refresh"
		tokenID = strings.TrimSuffix(path, "/refresh")
	case strings.HasSuffix(path, "/repos"):
		action = "repos"
		tokenID = strings.TrimSuffix(path, "/repos")
	default:
		tokenID = path
	}

	// #195 — empty token-id must return 400 with structured JSON.
	if tokenID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "token-id required",
			"hint":  "pass the saved-provider's vault UUID as the path segment",
		})
		return
	}
	// #195 — reject composite IDs containing ":" or "/" — UI must
	// resolve to a vault UUID first.
	if strings.ContainsAny(tokenID, ":/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "token-id must be the saved-provider's opaque ID (no ':' or '/')",
			"got":   tokenID,
			"hint":  "look up the saved git-provider's id via GET /api/v1/git-providers and pass that UUID",
		})
		return
	}

	kind, secret, err := s.resolveProviderToken(tokenID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":  "token not found",
			"detail": err.Error(),
		})
		return
	}
	if secret == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "saved provider has no token — re-paste it on the wizard",
		})
		return
	}

	switch action {
	case "":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		res, err := s.discovery.Discover(context.Background(), kind, tokenID, secret)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, res)
	case "refresh":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		res, err := s.discovery.Refresh(context.Background(), kind, tokenID, secret)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, res)
	case "repos":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query().Get("q")
		if _, err := s.discovery.Discover(context.Background(), kind, tokenID, secret); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		repos, err := s.discovery.Search(kind, tokenID, q)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
	default:
		http.NotFound(w, r)
	}
}

// resolveProviderToken pulls (kind, secret) for the given saved
// git-provider record. LoadGitProviders decrypts TokenCipher on load.
func (s *Server) resolveProviderToken(tokenID string) (discovery.Kind, string, error) {
	if s.rt == nil {
		return "", "", errors.New("runtime not initialised")
	}
	provs, err := runtime.LoadGitProviders(s.rt.StateDir())
	if err != nil {
		return "", "", err
	}
	for _, p := range provs {
		if p.ID != tokenID {
			continue
		}
		k := mapGitProviderKind(string(p.Kind))
		if k == "" {
			return "", "", fmt.Errorf("provider kind %q has no discovery backend", p.Kind)
		}
		return k, p.Token, nil
	}
	return "", "", fmt.Errorf("token-id %q not found", tokenID)
}

// mapGitProviderKind translates the existing git-provider kind names
// to discovery.Kind values. "embedded" is the chepherd embedded Gitea,
// resolved against its baked-in admin creds via a registered Gitea
// provider (out of scope for this PR — TODO).
func mapGitProviderKind(k string) discovery.Kind {
	switch k {
	case "github":
		return discovery.KindGitHub
	case "gitlab":
		return discovery.KindGitLab
	case "bitbucket":
		return discovery.KindBitbucket
	case "gitea", "embedded":
		return discovery.KindGitea
	}
	return ""
}
