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
	"net/url"
	"strings"

	"github.com/agenity-org/agenity/internal/discovery"
	"github.com/agenity-org/agenity/internal/runtime"
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
	// Two URL forms supported (see DiscoveryTree.svelte):
	//
	//   PATH FORM (opaque UUID, the #195 contract):
	//     GET   /api/v1/discovery/{token-id}
	//     POST  /api/v1/discovery/{token-id}/refresh
	//     GET   /api/v1/discovery/{token-id}/repos?q=
	//
	//   QUERY FORM (composite IDs containing ':' or '/'):
	//     GET   /api/v1/discovery/?token-id=<composite>
	//     POST  /api/v1/discovery/refresh?token-id=<composite>
	//     GET   /api/v1/discovery/repos?token-id=<composite>&q=
	//
	// The query form exists because Go's http.ServeMux issues a 301
	// redirect that collapses "//" → "/" on any path containing
	// double-slashes — which mangles composite IDs like
	// "github:https://github.com/<org>/<repo>" before any handler can
	// run. Query parameters survive the mux's path-cleaner untouched.
	//
	// We resolve in priority order: query param ?token-id= wins if
	// present (it's the explicit composite-ID form); otherwise we
	// fall back to the path segment.
	pathTail := strings.TrimPrefix(r.URL.Path, "/api/v1/discovery/")
	action := ""
	switch {
	case pathTail == "refresh" || strings.HasSuffix(pathTail, "/refresh"):
		action = "refresh"
		pathTail = strings.TrimSuffix(pathTail, "/refresh")
		pathTail = strings.TrimPrefix(pathTail, "refresh")
	case pathTail == "repos" || strings.HasSuffix(pathTail, "/repos"):
		action = "repos"
		pathTail = strings.TrimSuffix(pathTail, "/repos")
		pathTail = strings.TrimPrefix(pathTail, "repos")
	}

	tokenID := r.URL.Query().Get("token-id")
	if tokenID == "" {
		// Path-segment form (opaque UUID). EscapedPath preserves any
		// percent-encoding the mux didn't redirect-clean away.
		rawPathTail := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v1/discovery/")
		switch action {
		case "refresh":
			rawPathTail = strings.TrimSuffix(rawPathTail, "/refresh")
		case "repos":
			rawPathTail = strings.TrimSuffix(rawPathTail, "/repos")
		}
		tokenID, _ = url.QueryUnescape(rawPathTail)
	}
	_ = pathTail // path tail kept only for action discrimination above

	// #195 — empty token-id must return 400 with structured JSON.
	if tokenID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "token-id required",
			"hint":  "pass the saved-provider's ID as the path segment",
		})
		return
	}

	// Operator walk 2026-05-28: the strict "no ':' or '/'" rejection
	// from #195 was overzealous — the existing git-providers list
	// stores IDs in composite form ("github:https://github.com/<org>/<repo>")
	// because the v0.8 path predates the vault-UUID design. The
	// resolveProviderToken lookup below is the authoritative check; if
	// the composite ID matches a registered provider, the call succeeds.
	// We only refuse IDs that look like attempted directory traversal
	// or are otherwise invalid AFTER the lookup fails (404).

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
