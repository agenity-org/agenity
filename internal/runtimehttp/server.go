// Package runtimehttp serves chepherd's runtime over HTTP + WebSocket
// for the web / mobile / TUI-remote clients.
//
// Routes:
//
//	GET    /healthz                       liveness
//	GET    /api/v1/sessions               list sessions (JSON)
//	GET    /api/v1/inbox                  human-inbox stream (JSON)
//	GET    /api/v1/sessions/{name}        describe one session
//	POST   /api/v1/sessions               spawn (JSON: name, agent, tribe, role, cwd)
//	DELETE /api/v1/sessions/{name}        stop
//	POST   /api/v1/sessions/{name}/pause  pause/unpause (JSON: paused bool)
//	WS     /api/v1/sessions/{name}/attach attach to live output + accept stdin
//
// The WS attach frame format mirrors openova's pty-server (binary
// frames of raw PTY bytes — see internal/ptyhost/LICENSE-NOTICE for the
// shape contract). Clients can use xterm.js directly.
package runtimehttp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chepherd/chepherd/internal/auth"
	"github.com/chepherd/chepherd/internal/catalog"
	"github.com/chepherd/chepherd/internal/profile"
	"github.com/chepherd/chepherd/internal/prompts"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
	"github.com/chepherd/chepherd/internal/runtime"
	"github.com/chepherd/chepherd/internal/vault"
)

// Server hosts chepherd runtime endpoints. Caller is responsible for
// listening on a port + calling http.Serve(listener, server.Handler()).
type Server struct {
	rt     *runtime.Runtime
	WebDir string        // optional: serve Astro static build from this dir
	Vault   *vault.Vault      // optional: credential vault (nil = vault API returns 503)
	Auth    auth.AuthProvider // optional: auth provider (nil = no token validation, dev only)
	Profile *profile.Profile  // optional: deployment profile, surfaced via /healthz (#129)

	upgrader websocket.Upgrader
}

// New constructs a Server bound to the runtime.
func New(rt *runtime.Runtime) *Server {
	return &Server{
		rt: rt,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				// Loopback-only by default; the bp-chepherd Pod-mode
				// runs behind Cilium Gateway which does origin checks.
				return true
			},
		},
	}
}

// Handler returns the HTTP mux ready to be served.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/api/v1/sessions", s.sessionsRoot)
	mux.HandleFunc("/api/v1/sessions/", s.sessionByName)
	mux.HandleFunc("/api/v1/inbox", s.inbox)
	mux.HandleFunc("/api/v1/inbox/", s.inboxByID)
	mux.HandleFunc("/api/v1/inbox/read-all", s.inboxReadAll)
	mux.HandleFunc("/api/v1/claude-sessions", s.claudeSessions)
	mux.HandleFunc("/api/v1/folders/recent", s.recentFolders)

	// v0.6 unified-model endpoints
	mux.HandleFunc("/api/v1/teams", s.teamsHandler)
	mux.HandleFunc("/api/v1/teams/", s.teamByName)
	mux.HandleFunc("/api/v1/memberships", s.membershipsHandler)
	mux.HandleFunc("/api/v1/reviews/", s.reviewsByTarget)
	mux.HandleFunc("/api/v1/workspaces", s.workspacesHandler)
	mux.HandleFunc("/api/v1/workspaces/", s.workspaceByName)
	mux.HandleFunc("/api/v1/events", s.eventsHandler)
	mux.HandleFunc("/api/v1/events/stream", s.eventsStream)
	mux.HandleFunc("/api/v1/templates", s.templatesHandler)
	mux.HandleFunc("/api/v1/templates/", s.templateApply)
	mux.HandleFunc("/api/v1/prompts/", s.promptsHandler) // /api/v1/prompts/{role}
	mux.HandleFunc("/api/v1/runtime/claude-status", s.claudeStatusHandler)
	mux.HandleFunc("/api/v1/runtime/claude-profile", s.claudeProfileHandler)
	mux.HandleFunc("/api/v1/runtime/global-md", s.globalMDHandler)
	mux.HandleFunc("/api/v1/folders/git-info", s.gitInfoHandler)
	mux.HandleFunc("/api/v1/folders/git-setup", s.gitSetupHandler)
	mux.HandleFunc("/api/v1/git-providers", s.gitProvidersHandler)
	mux.HandleFunc("/api/v1/git-providers/", s.gitProviderByID)
	mux.HandleFunc("/api/v1/teams/saved", s.savedTeamsHandler)
	mux.HandleFunc("/api/v1/kanban", s.kanbanIssues)
	mux.HandleFunc("/api/v1/kanban/move", s.kanbanMove)

	// Credential vault
	mux.HandleFunc("/api/v1/vault", s.vaultRoot)
	mux.HandleFunc("/api/v1/vault/", s.vaultByID)
	mux.HandleFunc("/api/v1/vault/providers", s.vaultProviders)

	// Claude OAuth credentials (the "Claude account" picker — see R5 / #136)
	mux.HandleFunc("/api/v1/claude-tokens", s.claudeTokensHandler)
	mux.HandleFunc("/api/v1/claude-tokens/paste", s.claudeTokensPaste)
	mux.HandleFunc("/api/v1/claude-tokens/harvest", s.claudeTokensHarvest)
	// R5 redo (#136): full OAuth capture flow — spawn ephemeral agent,
	// expose its OAuth URL, accept the auth code, harvest credentials.
	mux.HandleFunc("/api/v1/claude-tokens/login-begin", s.claudeLoginBegin)
	mux.HandleFunc("/api/v1/claude-tokens/login-url/", s.claudeLoginURL)
	mux.HandleFunc("/api/v1/claude-tokens/login-submit/", s.claudeLoginSubmit)
	mux.HandleFunc("/api/v1/claude-tokens/login-cancel/", s.claudeLoginCancel)

	// /api-v08/ prefix alias — strips to /api/ so the Astro static build
	// (which uses the versioned dev-proxy path) works against this server
	// without rewriting every fetch URL.
	apiHandler := http.Handler(mux)
	mux.Handle("/api-v08/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/api/" + strings.TrimPrefix(r.URL.Path, "/api-v08/")
		r2.RequestURI = r2.URL.RequestURI()
		apiHandler.ServeHTTP(w, r2)
	}))

	// Static file serving — only active when --web-dir is set.
	// SPA fallback: any path not matching a real file returns index.html.
	if s.WebDir != "" {
		fs := http.FileServer(http.Dir(s.WebDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// Let API routes pass through (already registered with longer prefix).
			p := filepath.Join(s.WebDir, filepath.Clean("/"+r.URL.Path))
			if _, err := os.Stat(p); os.IsNotExist(err) {
				http.ServeFile(w, r, filepath.Join(s.WebDir, "index.html"))
				return
			}
			fs.ServeHTTP(w, r)
		})
	}

	return logMiddleware(mux)
}

// claudeProfileHandler — proxy to Anthropic's OAuth profile endpoint to
// surface the operator's account email. Best-effort: if Anthropic doesn't
// respond or the endpoint shape has changed, returns logged_in=true with
// email="".  Never returns the access token itself.
func (s *Server) claudeProfileHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"logged_in": false})
		return
	}
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken      string `json:"accessToken"`
			SubscriptionType string `json:"subscriptionType"`
			RateLimitTier    string `json:"rateLimitTier"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(b, &creds); err != nil || creds.ClaudeAiOauth.AccessToken == "" {
		writeJSON(w, http.StatusOK, map[string]any{"logged_in": false})
		return
	}
	// Try Anthropic's OAuth profile endpoint. Path is the canonical OAuth
	// userinfo path used by their internal API. Best-effort — if the
	// endpoint shape changes or the call is rate-limited we silently
	// return the parts we can derive locally.
	out := map[string]any{
		"logged_in":    true,
		"subscription": creds.ClaudeAiOauth.SubscriptionType,
		"rate_tier":    creds.ClaudeAiOauth.RateLimitTier,
		"email":        "",
		"name":         "",
	}
	for _, url := range []string{
		"https://api.anthropic.com/api/oauth/profile",
		"https://api.anthropic.com/v1/oauth/profile",
		"https://api.anthropic.com/api/oauth/user",
	} {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+creds.ClaudeAiOauth.AccessToken)
		req.Header.Set("User-Agent", "chepherd-v06")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp == nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			continue
		}
		var profile map[string]any
		if err := json.Unmarshal(body, &profile); err != nil {
			continue
		}
		// Capture whatever fields the response gives us, regardless of
		// the exact shape (defensive against Anthropic API churn).
		for _, k := range []string{"email", "email_address", "primary_email"} {
			if v, ok := profile[k].(string); ok && v != "" {
				out["email"] = v
				break
			}
		}
		for _, k := range []string{"name", "full_name", "display_name"} {
			if v, ok := profile[k].(string); ok && v != "" {
				out["name"] = v
				break
			}
		}
		if out["email"] != "" || out["name"] != "" {
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// savedTeamsHandler — list every team apply on disk that's now dormant
// (no live members) so the wizard's Stage 1 can offer one-click
// resurrect. Each team carries the template name, member list, last-
// observed claude_uuid per member, and last_active timestamp.
func (s *Server) savedTeamsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	root := filepath.Join(s.rt.StateDir(), "teams")
	entries, _ := os.ReadDir(root)
	live := map[string]bool{}
	for _, info := range s.rt.List() {
		if !info.Exited {
			live[info.Team] = true
		}
	}
	out := []map[string]any{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name(), "apply.json")
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(b, &rec); err != nil {
			continue
		}
		rec["name"] = e.Name()
		rec["live"] = live[e.Name()]
		out = append(out, rec)
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": out})
}

// gitInfoHandler — GET /api/v1/folders/git-info?cwd=<abs>
// Reports whether the given cwd is a git repo, its current branch, and
// the origin remote URL (if set). Used by the SpawnWizard to decide
// whether to offer init / connect-remote / no-git options.
func (s *Server) gitInfoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "cwd required"})
		return
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		writeJSON(w, http.StatusOK, map[string]any{"exists": false, "is_git": false})
		return
	}
	// Cheap heuristic: .git directory or .git file (worktree).
	_, gerr := os.Stat(filepath.Join(cwd, ".git"))
	if gerr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"exists": true, "is_git": false})
		return
	}
	branch := readGitBranchAt(cwd)
	remote := readGitRemoteAt(cwd)
	writeJSON(w, http.StatusOK, map[string]any{
		"exists": true, "is_git": true,
		"branch": branch, "remote": remote,
	})
}

// gitSetupHandler — POST /api/v1/folders/git-setup
// {cwd, mode: "init-new" | "connect-remote", remote?: "..."}
// Best-effort: runs `git init` and/or `git remote add origin <url>` in cwd.
// Skips silently on "no-git" mode.
func (s *Server) gitSetupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct{ Cwd, Mode, Remote string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.Cwd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "cwd required"})
		return
	}
	if err := os.MkdirAll(req.Cwd, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if req.Mode == "init-new" || req.Mode == "connect-remote" {
		if _, err := os.Stat(filepath.Join(req.Cwd, ".git")); err != nil {
			cmd := execCommand("git", "init")
			cmd.Dir = req.Cwd
			if out, err := cmd.CombinedOutput(); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": fmt.Sprintf("git init: %v — %s", err, out)})
				return
			}
		}
	}
	if req.Mode == "connect-remote" && req.Remote != "" {
		cmd := execCommand("git", "remote", "add", "origin", req.Remote)
		cmd.Dir = req.Cwd
		_, _ = cmd.CombinedOutput() // ignore "remote already exists"
	}
	s.rt.RecordEvent(runtime.Event{
		Kind: "git_setup", Actor: "operator",
		Body: fmt.Sprintf("git setup in %q (mode=%s, remote=%q)", req.Cwd, req.Mode, req.Remote),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func readGitBranchAt(cwd string) string {
	cmd := execCommand("git", "branch", "--show-current")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
func readGitRemoteAt(cwd string) string {
	cmd := execCommand("git", "config", "--get", "remote.origin.url")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// gitReposHandler — GET /api/v1/folders/git-repos
// Discovers git repos in ~/repos/, ~/projects/, ~/work/, ~/src/, ~/code/ and CWD.
// Returns up to 60 repos sorted by modification time (newest first).
// gitProvidersHandler — GET /api/v1/git-providers  (list)
//                        POST /api/v1/git-providers  (register / update)
func (s *Server) gitProvidersHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		providers, err := runtime.LoadGitProviders(s.rt.StateDir())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if providers == nil {
			providers = []*runtime.GitProvider{}
		}
		// Never return raw tokens to the UI — mask them.
		type safeProvider struct {
			ID           string                  `json:"id"`
			Kind         runtime.GitProviderKind `json:"kind"`
			RepoURL      string                  `json:"repo_url"`
			DisplayName  string                  `json:"display_name"`
			RegisteredAt string                  `json:"registered_at"`
			HasToken     bool                    `json:"has_token"`
		}
		var out []safeProvider
		for _, p := range providers {
			out = append(out, safeProvider{
				ID:           p.ID,
				Kind:         p.Kind,
				RepoURL:      p.RepoURL,
				DisplayName:  p.DisplayName,
				RegisteredAt: p.RegisteredAt.UTC().Format(time.RFC3339),
				HasToken:     p.Token != "",
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"providers": out})

	case http.MethodPost:
		var req struct {
			Kind        runtime.GitProviderKind `json:"kind"`
			RepoURL     string                  `json:"repo_url"`
			Token       string                  `json:"token"`
			DisplayName string                  `json:"display_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
			return
		}
		if req.Kind == "" || (req.Kind != runtime.GitProviderEmbedded && req.RepoURL == "") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "kind and repo_url are required"})
			return
		}
		id := string(req.Kind) + ":" + req.RepoURL
		if req.Kind == runtime.GitProviderEmbedded {
			id = "embedded"
		}
		name := req.DisplayName
		if name == "" {
			name = req.RepoURL
		}
		p := &runtime.GitProvider{
			ID:           id,
			Kind:         req.Kind,
			RepoURL:      req.RepoURL,
			Token:        req.Token,
			DisplayName:  name,
			RegisteredAt: time.Now().UTC(),
		}
		if err := runtime.UpsertGitProvider(s.rt.StateDir(), p); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		s.rt.RecordEvent(runtime.Event{
			Kind: "git_provider_registered", Actor: "operator",
			Body: fmt.Sprintf("registered git provider %q (%s)", name, req.Kind),
		})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// gitProviderByID — DELETE /api/v1/git-providers/{id}
func (s *Server) gitProviderByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/git-providers/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := runtime.DeleteGitProvider(s.rt.StateDir(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// claudeStatusHandler returns the operator's Claude login info — read from
// ~/.claude/.credentials.json — so the AgentDetails widget can surface
// "Login method: Claude Max account" etc. The OAuth access token + refresh
// token are NEVER returned; only the subscription type + rate-limit tier
// + token-expiry timestamp.
func (s *Server) claudeStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".claude", ".credentials.json")
	b, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"logged_in": false})
		return
	}
	var creds struct {
		ClaudeAiOauth struct {
			ExpiresAt        int64    `json:"expiresAt"`
			Scopes           []string `json:"scopes"`
			SubscriptionType string   `json:"subscriptionType"`
			RateLimitTier    string   `json:"rateLimitTier"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(b, &creds); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"logged_in": false, "error": err.Error()})
		return
	}
	loginMethod := "Claude account"
	switch creds.ClaudeAiOauth.SubscriptionType {
	case "max":
		loginMethod = "Claude Max account"
	case "pro":
		loginMethod = "Claude Pro account"
	case "team":
		loginMethod = "Claude Team account"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"logged_in":     true,
		"login_method":  loginMethod,
		"subscription":  creds.ClaudeAiOauth.SubscriptionType,
		"rate_tier":     creds.ClaudeAiOauth.RateLimitTier,
		"expires_at":    creds.ClaudeAiOauth.ExpiresAt,
		"scopes":        creds.ClaudeAiOauth.Scopes,
	})
}

// MemberOverrideReq lets the operator specialize a single template member
// at apply time — pick a specific resume UUID, swap cwd, or replace the
// per-role prompt without forking the YAML.
type MemberOverrideReq struct {
	ResumeUUID string `json:"resume_uuid,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
	Fresh      bool   `json:"fresh,omitempty"` // explicit opt-out from resume_strategy
}

// promptsHandler returns the default system prompt for a given role so
// the SpawnModal can pre-fill its textarea + the operator can tweak.
//   GET /api/v1/prompts/worker    → { role: "worker",   prompt: "..." }
//   GET /api/v1/prompts/shepherd  → { role: "shepherd", prompt: "..." }
func (s *Server) promptsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	role := strings.TrimPrefix(r.URL.Path, "/api/v1/prompts/")
	var body string
	switch role {
	case "shepherd":
		body = prompts.Shepherd
	default:
		body = prompts.Worker
	}
	writeJSON(w, http.StatusOK, map[string]any{"role": role, "prompt": body})
}

// ServeOn binds to addr + serves. Returns once listen is established;
// runs the accept loop on a goroutine. Returns an error from Listen.
// Use Stop() via the returned *http.Server.
func (s *Server) ServeOn(addr string) (*http.Server, error) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = srv.ListenAndServe()
	}()
	return srv, nil
}

// ---- handlers ----

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"ok":       true,
		"sessions": len(s.rt.List()),
		"ts":       time.Now().UTC(),
	}
	// Deployment profile (#129) — operators + dashboard read this to
	// know what spawner/auth/storage/tls is wired in.
	if s.Profile != nil {
		resp["profile"] = map[string]string{
			"name":     profileNameOrAuto(s.Profile.Name),
			"spawner":  s.Profile.Spawner,
			"auth":     s.Profile.AuthMode,
			"storage":  s.Profile.StorageType,
			"tls":      s.Profile.TLSMode,
			"oidc_iss": s.Profile.OIDCIssuer,
		}
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func profileNameOrAuto(n string) string {
	if n == "" {
		return "auto"
	}
	return n
}

// collectVCSTokenEnv assembles the set of credential env vars to inject
// into a freshly-spawned agent. Two sources are merged:
//
//  1. The git provider selected for this spawn (provider_id), if any —
//     its token becomes GITHUB_TOKEN / GITLAB_TOKEN / GITEA_TOKEN /
//     BITBUCKET_TOKEN based on the provider's kind. This is the "token
//     used to clone the repo also lets the agent push back" case (R1).
//  2. Every vault credential whose provider declares a DefaultEnv —
//     anthropic-api → ANTHROPIC_API_KEY, github-pat → GITHUB_TOKEN, etc.
//     Vault entries are the operator's "saved across all teams" tokens.
//
// Provider-supplied tokens (source 1) take precedence over vault tokens
// (source 2) for the same env-var name. The result is a slice of
// "KEY=VALUE" strings ready to feed into runtime.SpawnSpec.Env.
//
// R1 / #132.
func (s *Server) collectVCSTokenEnv(providerID string) []string {
	env := map[string]string{}

	// 2. Vault keys — set first so step 1 can overwrite if there's a conflict.
	if s.Vault != nil {
		if vEnv, err := s.Vault.EnvFor(nil); err == nil {
			for k, v := range vEnv {
				env[k] = v
			}
		}
	}

	// 1. Provider token by kind.
	if providerID != "" {
		if ps, err := runtime.LoadGitProviders(s.rt.StateDir()); err == nil {
			for _, p := range ps {
				if p.ID != providerID || p.Token == "" {
					continue
				}
				switch p.Kind {
				case runtime.GitProviderGitHub:
					env["GITHUB_TOKEN"] = p.Token
					env["GH_TOKEN"] = p.Token
				case runtime.GitProviderGitLab:
					env["GITLAB_TOKEN"] = p.Token
				case runtime.GitProviderGitea:
					env["GITEA_TOKEN"] = p.Token
				case runtime.GitProviderBitbucket:
					env["BITBUCKET_TOKEN"] = p.Token
				}
				break
			}
		}
	}

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// resolveProviderCwd maps a provider_id to a working directory.
// If provider_id is set, uses the provider's repo URL to derive a state-managed
// clone path. If cwd is also provided, it wins (explicit override).
func (s *Server) resolveProviderCwd(providerID, fallbackCwd string) (string, error) {
	if providerID == "" {
		if fallbackCwd == "" {
			fallbackCwd, _ = os.UserHomeDir()
		}
		return fallbackCwd, nil
	}
	providers, err := runtime.LoadGitProviders(s.rt.StateDir())
	if err != nil {
		return fallbackCwd, err
	}
	for _, p := range providers {
		if p.ID != providerID {
			continue
		}
		if p.Kind == runtime.GitProviderEmbedded {
			// Embedded Gitea — workspace under state dir.
			dir := filepath.Join(s.rt.StateDir(), "workspaces", "embedded")
			_ = os.MkdirAll(dir, 0o700)
			return dir, nil
		}
		// External provider — clone if needed, return clone path.
		dir := filepath.Join(s.rt.StateDir(), "workspaces", sanitizeID(p.ID))
		if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
			_ = os.MkdirAll(dir, 0o700)
			cloneURL := p.RepoURL
			// Inject token for HTTPS clones.
			if p.Token != "" && strings.HasPrefix(cloneURL, "https://") {
				cloneURL = strings.Replace(cloneURL, "https://", "https://oauth2:"+p.Token+"@", 1)
			}
			cmd := exec.Command("git", "clone", "--depth=1", cloneURL, dir)
			if out, err := cmd.CombinedOutput(); err != nil {
				return dir, fmt.Errorf("git clone failed: %w\n%s", err, out)
			}
		}
		return dir, nil
	}
	if fallbackCwd == "" {
		fallbackCwd, _ = os.UserHomeDir()
	}
	return fallbackCwd, fmt.Errorf("provider %q not registered", providerID)
}

func sanitizeID(id string) string {
	var b strings.Builder
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

func (s *Server) sessionsRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"sessions": s.rt.List()})
	case http.MethodPost:
		var req struct {
			Name, Agent, Team, Role, Cwd, SystemPrompt string
			ProviderID                                  string                `json:"provider_id"`
			AgentArgs                                   []string              `json:"agent_args"`
			ResumeUUID                                  string                `json:"resume_uuid"`
			UseDefaultPrompt                            bool                  `json:"use_default_prompt"`
			StatSheet                                   runtime.AgentStatSheet `json:"stat_sheet"`
			ClaudeTokenID                               string                `json:"claude_token_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		cwd, err := s.resolveProviderCwd(req.ProviderID, req.Cwd)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		role := runtime.Role(req.Role)
		if role == "" {
			role = runtime.RoleWorker
		}
		args := req.AgentArgs
		if req.ResumeUUID != "" {
			args = append(args, "--resume", req.ResumeUUID)
		}
		systemPrompt := req.SystemPrompt
		if req.UseDefaultPrompt && systemPrompt == "" {
			if role == runtime.RoleShepherd {
				systemPrompt = prompts.Shepherd
			} else {
				systemPrompt = prompts.Worker
			}
		}
		// R1 (#132) — inject VCS tokens into the agent environment so
		// `gh`, `glab`, `git push` etc. work inside the container without
		// re-entering credentials. Sources, in priority order:
		//   1. The selected provider's token (per-provider, set during
		//      git-provider registration in the wizard).
		//   2. Vault tokens matching standard provider env vars
		//      (GITHUB_TOKEN, GITLAB_TOKEN, GITEA_TOKEN, ANTHROPIC_API_KEY).
		//
		// IMPORTANT: when the agent will use a Claude OAuth token (the
		// usual flow — non-empty ClaudeTokenID OR any non-empty Claude
		// OAuth credential available), we MUST NOT inject ANTHROPIC_API_KEY
		// because claude-code refuses to start with both set ("Auth
		// conflict: Both a token (claude.ai) and an API key set"). The
		// API key path is reserved for explicit API-only configs (which
		// the agent would request via env later, not the auto flow).
		spawnEnv := s.collectVCSTokenEnv(req.ProviderID)
		spawnEnv = stripEnvKey(spawnEnv, "ANTHROPIC_API_KEY")
		info, sess, err := s.rt.Spawn(runtime.SpawnSpec{
			Name:          req.Name,
			AgentSlug:     req.Agent,
			Team:          req.Team,
			Role:          role,
			Cwd:           cwd,
			SystemPrompt:  systemPrompt,
			StatSheet:     req.StatSheet,
			AgentArgs:     args,
			ClaudeTokenID: req.ClaudeTokenID,
			Env:           spawnEnv,
		})
		if err == nil && req.Team != "" {
			_, _ = s.rt.JoinTeam(req.Name, req.Team, runtime.MembershipRole(req.Role), "")
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if sess != nil && (req.Agent == "" || req.Agent == "claude-code") {
			go autoDismissClaudeFirstRunPrompts(sess)
		}
		writeJSON(w, http.StatusCreated, info)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) sessionByName(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	parts := strings.SplitN(path, "/", 2)
	name := parts[0]
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}

	sess, info := s.rt.Get(name)
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such session: " + name})
		return
	}

	switch {
	case sub == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, info)
	case sub == "" && r.Method == http.MethodDelete:
		_ = s.rt.Stop(name)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case sub == "pause" && r.Method == http.MethodPost:
		var req struct{ Paused bool }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		_ = s.rt.Pause(name, req.Paused)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paused": req.Paused})
	case sub == "attach" && r.Method == http.MethodGet:
		s.handleAttach(w, r, sess, name)
	case sub == "stat-sheet" && r.Method == http.MethodPatch:
		var patch runtime.AgentStatSheet
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := s.rt.UpdateStatSheet(name, patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		_, info2 := s.rt.Get(name)
		writeJSON(w, http.StatusOK, info2)
	case sub == "rename" && r.Method == http.MethodPost:
		var req struct {
			NewName string `json:"new_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := s.rt.Rename(name, req.NewName); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": req.NewName})
	case sub == "restart" && r.Method == http.MethodPost:
		info2, err := s.rt.Restart(name)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, info2)
	case sub == "handoff" && r.Method == http.MethodPost:
		var req struct{ Target string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if req.Target == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "target required"})
			return
		}
		info, err := s.rt.Handoff(name, req.Target)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, info)
	case sub == "poke-prompt" && r.Method == http.MethodPost:
		var req struct{ Prompt string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if req.Prompt == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "prompt required"})
			return
		}
		if err := s.rt.PokePrompt(name, req.Prompt); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed for "+sub, http.StatusMethodNotAllowed)
	}
}

func (s *Server) inbox(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"inbox": s.rt.Inbox()})
}

// inboxByID handles POST /api/v1/inbox/{id}/read — flip a single entry to read.
func (s *Server) inboxByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/inbox/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[1] != "read" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ok := s.rt.MarkInboxRead(parts[0])
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such message: " + parts[0]})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// inboxReadAll handles POST /api/v1/inbox/read-all — flip all entries to read.
func (s *Server) inboxReadAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	n := s.rt.MarkAllInboxRead()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "marked": n})
}

// handleAttach upgrades to WebSocket + attaches the connection to the
// session's PTY: ring-buffer replay first, then live fan-out. Client
// sends binary frames → PTY stdin via session.Write.
func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request, sess *session.Session, name string) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	sub, replay, err := sess.Subscribe(256)
	if err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
		return
	}
	defer sess.Unsubscribe(sub)

	if len(replay) > 0 {
		_ = conn.WriteMessage(websocket.BinaryMessage, replay)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	// Outbound: PTY → WebSocket
	go func() {
		defer wg.Done()
		defer close(stop)
		for {
			select {
			case <-sub.Done:
				return
			case chunk, ok := <-sub.Ch:
				if !ok {
					return
				}
				if err := conn.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
					return
				}
			}
		}
	}()

	// Inbound: WebSocket → PTY stdin (+ resize control frames).
	// Binary frames are raw stdin bytes. Text frames carry a tiny JSON
	// control protocol; today only {"type":"resize","rows":N,"cols":N}
	// is honored — the client sends one on fit() so the PTY child gets
	// a SIGWINCH that matches xterm's actual dimensions.
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.TextMessage {
				var ctrl struct {
					Type       string `json:"type"`
					Rows, Cols uint16
				}
				if json.Unmarshal(msg, &ctrl) == nil && ctrl.Type == "resize" && ctrl.Rows > 0 && ctrl.Cols > 0 {
					_ = sess.Resize(ctrl.Rows, ctrl.Cols)
					continue
				}
				// Unknown text frame — fall through to PTY stdin so legacy
				// clients sending plain text still work.
			}
			if _, err := sess.Write(msg); err != nil {
				return
			}
		}
	}()

	wg.Wait()
	_ = name // reserved for future audit log
}

// latestClaudeSessionUUID returns the UUID of the most-recently-modified
// Claude session whose canonical cwd matches `cwd`, or "" if none. Used
// by templateApply's "resume_strategy=latest-in-cwd" path so a Council /
// Pair template can auto-resume each member's prior session.
func latestClaudeSessionUUID(cwd string) string {
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".claude", "projects")
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	type cand struct {
		uuid string
		mod  time.Time
	}
	var best cand
	for _, projDir := range entries {
		if !projDir.IsDir() {
			continue
		}
		if decodeClaudeProjectDir(projDir.Name()) != cwd {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, projDir.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(best.mod) {
				best.mod = info.ModTime()
				best.uuid = strings.TrimSuffix(f.Name(), ".jsonl")
			}
		}
	}
	return best.uuid
}

// claudeSessions enumerates Claude Code session files under
// ~/.claude/projects/<encoded-cwd>/<uuid>.jsonl so the web Spawn modal
// can offer a "resume which session?" picker for a chosen folder.
//
// Query: ?cwd=<absolute-path> filters to one project; omit to list all.
// Response: {sessions: [{uuid, cwd, modified, size_bytes, first_message}]}
//
// We read the cwd from each JSONL's first record carrying a top-level
// `cwd` field (typically the first `type:user` record). The legacy
// approach of decoding the directory name with "-"→"/" was broken for
// hyphenated repos (talent-mesh → talent/mesh) — fixed per reviewer
// finding on #78.
func (s *Server) claudeSessions(w http.ResponseWriter, r *http.Request) {
	filterCwd := r.URL.Query().Get("cwd")
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".claude", "projects")
	entries, err := os.ReadDir(root)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"sessions": []any{}})
		return
	}
	type cs struct {
		UUID         string `json:"uuid"`
		Cwd          string `json:"cwd"`
		Modified     string `json:"modified"`
		SizeBytes    int64  `json:"size_bytes"`
		FirstMessage string `json:"first_message"`
	}
	out := []cs{}
	for _, projDir := range entries {
		if !projDir.IsDir() {
			continue
		}
		// The directory name is the project's canonical cwd (Claude
		// persists sessions by where they were started). A session's
		// first-record cwd field may have drifted (resumed under a
		// different cwd, e.g. iogrid#477 was started in iogrid but its
		// first record says openova-private). Treat the directory as
		// authoritative for "which project this session belongs to".
		decodedDir := decodeClaudeProjectDir(projDir.Name())
		projPath := filepath.Join(root, projDir.Name())
		files, err := os.ReadDir(projPath)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			uuid := strings.TrimSuffix(f.Name(), ".jsonl")
			_, first := readSessionMeta(filepath.Join(projPath, f.Name()))
			// The directory-decoded cwd is what we filter and display by.
			cwd := decodedDir
			if filterCwd != "" && cwd != filterCwd {
				continue
			}
			out = append(out, cs{
				UUID:         uuid,
				Cwd:          cwd,
				Modified:     info.ModTime().UTC().Format(time.RFC3339),
				SizeBytes:    info.Size(),
				FirstMessage: first,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// recentFolders lists folders that have at least one Claude session,
// for the Spawn modal's folder autocomplete. Most-recently-active first.
// Reads cwd from JSONL records (same fix as claudeSessions above).
func (s *Server) recentFolders(w http.ResponseWriter, _ *http.Request) {
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".claude", "projects")
	entries, err := os.ReadDir(root)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"folders": []any{}})
		return
	}
	type fe struct {
		Path     string `json:"path"`
		Modified string `json:"modified"`
		Sessions int    `json:"sessions"`
	}
	byPath := map[string]*fe{}
	for _, d := range entries {
		if !d.IsDir() {
			continue
		}
		// Directory name → cwd (authoritative; see claudeSessions comment).
		cwd := decodeClaudeProjectDir(d.Name())
		if cwd == "" {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			modTime := info.ModTime().UTC().Format(time.RFC3339)
			if cur, ok := byPath[cwd]; ok {
				cur.Sessions++
				if modTime > cur.Modified {
					cur.Modified = modTime
				}
			} else {
				byPath[cwd] = &fe{Path: cwd, Modified: modTime, Sessions: 1}
			}
		}
	}
	out := make([]fe, 0, len(byPath))
	for _, v := range byPath {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	writeJSON(w, http.StatusOK, map[string]any{"folders": out})
}

// readSessionMeta scans a Claude session JSONL line-by-line and returns
// (cwd, first_user_message). Earlier versions read a fixed 16KB window
// which dropped sessions where the first user record is past 16KB
// (e.g. long multi-hour sessions like iogrid#477 "Apple Developer setup",
// 98MB, where many summary/queue-operation records precede the first
// user message). We now stream via bufio.Scanner with a large line cap.
// Fallback: derive cwd from the encoded directory name when the JSONL
// has no cwd field at all (very early Claude versions).
func readSessionMeta(path string) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024) // 4 MiB max line
	var cwd, first string
	// Cap how many lines we'll scan so a 100 MB file doesn't block the API.
	const maxLines = 5000
	scanned := 0
	for sc.Scan() && scanned < maxLines {
		scanned++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec struct {
			Type    string `json:"type"`
			Cwd     string `json:"cwd"`
			Message struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if cwd == "" && rec.Cwd != "" {
			cwd = rec.Cwd
		}
		if first == "" && (rec.Type == "user" || rec.Message.Role == "user") {
			switch c := rec.Message.Content.(type) {
			case string:
				first = truncate(c, 120)
			case []any:
				for _, item := range c {
					if m, ok := item.(map[string]any); ok {
						if t, _ := m["text"].(string); t != "" {
							first = truncate(t, 120)
							break
						}
					}
				}
			}
		}
		if cwd != "" && first != "" {
			break
		}
	}
	// Fallback: decode the parent directory name. This is lossy for
	// hyphenated repos (talent-mesh would become /talent/mesh) but it's
	// strictly better than dropping the session from the picker entirely.
	if cwd == "" {
		dir := filepath.Base(filepath.Dir(path))
		if strings.HasPrefix(dir, "-") {
			cwd = "/" + strings.ReplaceAll(strings.TrimPrefix(dir, "-"), "-", "/")
		}
	}
	return cwd, first
}

// ===== v0.6 endpoints: teams, memberships, reviews, workspaces =====

func (s *Server) teamsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"teams": s.rt.ListTeams()})
	case http.MethodPost:
		var req struct {
			Name, CanonPath, Topology string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if req.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "name required"})
			return
		}
		t, created := s.rt.CreateTeam(req.Name, req.CanonPath, runtime.Topology(req.Topology))
		status := http.StatusCreated
		if !created {
			status = http.StatusOK
		}
		writeJSON(w, status, map[string]any{"team": t, "created": created})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) teamByName(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/teams/")
	parts := strings.SplitN(path, "/", 2)
	name := parts[0]
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}
	if name == "" {
		http.NotFound(w, r)
		return
	}

	// Sub-resource: /api/v1/teams/{name}/resurrect
	if sub == "resurrect" && r.Method == http.MethodPost {
		s.resurrectTeamHandler(w, r, name)
		return
	}
	// Sub-resource: /api/v1/teams/{name}/canon — view + edit team CLAUDE.md.
	if sub == "canon" {
		var canonPath string
		for _, t := range s.rt.ListTeams() {
			if t.Name == name {
				canonPath = t.CanonPath
				break
			}
		}
		if canonPath == "" {
			// Lazily compute default location if team is in the byName
			// map but no canonPath is set (transitional / legacy teams).
			canonPath = filepath.Join(s.rt.StateDir(), "teams", name, "CLAUDE.md")
		}
		switch r.Method {
		case http.MethodGet:
			b, err := os.ReadFile(canonPath)
			if err != nil && !os.IsNotExist(err) {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"team": name, "path": canonPath, "body": string(b)})
		case http.MethodPut:
			var req struct{ Body string }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			if err := os.MkdirAll(filepath.Dir(canonPath), 0o700); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			if err := os.WriteFile(canonPath, []byte(req.Body), 0o600); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			s.rt.RecordEvent(runtime.Event{
				Kind: "canon_updated", Actor: "operator",
				Body: fmt.Sprintf("canon for team %q updated (%d bytes)", name, len(req.Body)),
				Meta: map[string]any{"team": name, "bytes": len(req.Body)},
			})
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	switch r.Method {
	case http.MethodDelete:
		if err := s.rt.DeleteTeam(name); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodPatch:
		var req struct {
			NewName  string `json:"new_name"`
			Topology string `json:"topology"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := s.rt.UpdateTeam(name, req.NewName, runtime.Topology(req.Topology)); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		// Find by name in ListTeams
		for _, t := range s.rt.ListTeams() {
			if t.Name == name {
				writeJSON(w, http.StatusOK, map[string]any{"team": t})
				return
			}
		}
		http.NotFound(w, r)
	}
}

func (s *Server) membershipsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		agent := r.URL.Query().Get("agent")
		team := r.URL.Query().Get("team")
		writeJSON(w, http.StatusOK, map[string]any{"memberships": s.rt.ListMemberships(agent, team)})
	case http.MethodPost:
		var req struct {
			Agent, Team, Role, BriefOverride string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		m, err := s.rt.JoinTeam(req.Agent, req.Team, runtime.MembershipRole(req.Role), req.BriefOverride)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"membership": m})
	case http.MethodDelete:
		agent := r.URL.Query().Get("agent")
		team := r.URL.Query().Get("team")
		ok := s.rt.LeaveTeam(agent, team)
		writeJSON(w, http.StatusOK, map[string]any{"removed": ok})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) reviewsByTarget(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimPrefix(r.URL.Path, "/api/v1/reviews/")
	if target == "" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": s.rt.ListReviews(target)})
}

// Workspaces: persist user's pane layouts so they're shared across operators.
// Stored as JSON files in <stateDir>/workspaces/<name>.json.
// Simple key-value: GET list / GET by name / PUT (save) / DELETE.
func (s *Server) workspacesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		dir := filepath.Join(s.rt.StateDir(), "workspaces")
		entries, _ := os.ReadDir(dir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				names = append(names, strings.TrimSuffix(e.Name(), ".json"))
			}
		}
		sort.Strings(names)
		writeJSON(w, http.StatusOK, map[string]any{"workspaces": names})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) workspaceByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/workspaces/")
	if name == "" || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	dir := filepath.Join(s.rt.StateDir(), "workspaces")
	_ = os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, name+".json")
	switch r.Method {
	case http.MethodGet:
		b, err := os.ReadFile(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	case http.MethodPut:
		b, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		// Validate it's JSON
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error()})
			return
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodDelete:
		_ = os.Remove(path)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func truncatePrompt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

// templatesHandler — list available TeamProfile templates from the
// catalog dir priority: bundled image catalog → operator custom → repo fallback (dev).
func (s *Server) catalogDirs() []string {
	return []string{
		"/app/catalog",
		"./catalog",
		filepath.Join(s.rt.StateDir(), "catalog"),
		filepath.Join(os.Getenv("HOME"), "repos", "chepherd", "catalog"),
	}
}

func (s *Server) templatesHandler(w http.ResponseWriter, r *http.Request) {
	dirs := s.catalogDirs()
	seen := map[string]bool{}
	var out []map[string]any
	for _, d := range dirs {
		ps, _ := catalog.LoadAll(d)
		for _, p := range ps {
			if seen[p.Name] {
				continue
			}
			seen[p.Name] = true
			memberSpecs := make([]map[string]any, 0, len(p.Members))
			for _, m := range p.Members {
				memberSpecs = append(memberSpecs, map[string]any{
					"name":          m.Name,
					"agent":         m.Agent,
					"role":          string(m.Role),
					"prompt_preview": truncatePrompt(m.Prompt + m.BriefOverride),
					"cwd":           m.Cwd,
				})
			}
			out = append(out, map[string]any{
				"name":         p.Name,
				"description":  p.Description,
				"topology":     p.Topology,
				"members":      len(p.Members),
				"member_specs": memberSpecs,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}

// templateApply — POST /api/v1/templates/{name}/apply {team, cwd}
//                  POST /api/v1/templates/{name}/fork {new_name}        — copy YAML to operator dir
//                  PUT  /api/v1/templates/{name}                         — overwrite YAML in operator dir
func (s *Server) templateApply(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/templates/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 1 && r.Method == http.MethodPut {
		// Save / publish operator-edited template YAML.
		name := parts[0]
		body, _ := io.ReadAll(r.Body)
		opDir := filepath.Join(s.rt.StateDir(), "catalog")
		_ = os.MkdirAll(opDir, 0o700)
		if err := os.WriteFile(filepath.Join(opDir, name+".yaml"), body, 0o600); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		s.rt.RecordEvent(runtime.Event{
			Kind: "template_published", Actor: "operator",
			Body: fmt.Sprintf("operator published template %q (%d bytes)", name, len(body)),
			Meta: map[string]any{"template": name, "bytes": len(body)},
		})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": filepath.Join(opDir, name+".yaml")})
		return
	}
	if len(parts) == 2 && parts[1] == "fork" && r.Method == http.MethodPost {
		// Copy a built-in template YAML to operator dir so it's editable.
		srcName := parts[0]
		var req struct {
			NewName string `json:"new_name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		newName := req.NewName
		if newName == "" {
			newName = srcName + "-fork"
		}
		var srcPath string
		for _, d := range s.catalogDirs() {
			candidate := filepath.Join(d, srcName+".yaml")
			if _, err := os.Stat(candidate); err == nil {
				srcPath = candidate
				break
			}
		}
		if srcPath == "" {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such template: " + srcName})
			return
		}
		b, err := os.ReadFile(srcPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		// Rewrite the leading `name:` field of the YAML (cheap regex-free
		// scan over the first non-comment line that starts with "name:")
		lines := strings.Split(string(b), "\n")
		for i, ln := range lines {
			trim := strings.TrimLeft(ln, " \t")
			if strings.HasPrefix(trim, "name:") {
				indent := ln[:len(ln)-len(trim)]
				lines[i] = indent + "name: " + newName
				break
			}
		}
		out := []byte(strings.Join(lines, "\n"))
		opDir := filepath.Join(s.rt.StateDir(), "catalog")
		_ = os.MkdirAll(opDir, 0o700)
		dstPath := filepath.Join(opDir, newName+".yaml")
		if err := os.WriteFile(dstPath, out, 0o600); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		s.rt.RecordEvent(runtime.Event{
			Kind: "template_forked", Actor: "operator",
			Body: fmt.Sprintf("template %q forked → %q", srcName, newName),
			Meta: map[string]any{"src": srcName, "new": newName},
		})
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "name": newName, "path": dstPath, "body": string(out)})
		return
	}
	if len(parts) != 2 || parts[1] != "apply" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	templateName := parts[0]
	var req struct {
		Team            string
		Cwd             string
		ProviderID      string                       `json:"provider_id"`
		Topology        string
		ResumeStrategy  string                       `json:"resume_strategy"` // "" | "fresh" | "latest-in-cwd"
		MemberOverrides map[string]MemberOverrideReq `json:"member_overrides"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	// Find the template
	var p *catalog.TeamProfile
	for _, d := range s.catalogDirs() {
		ps, _ := catalog.LoadAll(d)
		for _, t := range ps {
			if t.Name == templateName {
				p = t
				break
			}
		}
		if p != nil {
			break
		}
	}
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such template: " + templateName})
		return
	}
	team := req.Team
	if team == "" {
		team = p.Name
	}
	cwd, _ := s.resolveProviderCwd(req.ProviderID, req.Cwd)
	if cwd == "" {
		cwd = os.Getenv("HOME")
	}
	// Spawn each member; auto-join to team
	effectiveTopology := p.Topology
	if req.Topology != "" {
		effectiveTopology = runtime.Topology(req.Topology)
	}
	_, _ = s.rt.CreateTeam(team, "", effectiveTopology)
	type spawned struct {
		Name, Role string
		Err        string `json:",omitempty"`
	}
	var results []spawned
	for _, m := range p.Members {
		role := runtime.Role(m.Role)
		if role != runtime.RoleShepherd {
			role = runtime.RoleWorker
		}
		ov := req.MemberOverrides[m.Name]
		// Effective per-member cwd: override > YAML m.Cwd > apply-time cwd.
		memberCwd := ov.Cwd
		if memberCwd == "" {
			memberCwd = m.Cwd
		}
		if memberCwd == "" {
			memberCwd = cwd
		}
		// Effective per-member system prompt:
		//   override.Prompt > member.Prompt > member.BriefOverride > role-default
		sysPrompt := ov.Prompt
		if sysPrompt == "" {
			sysPrompt = m.Prompt
		}
		if sysPrompt == "" {
			sysPrompt = m.BriefOverride
		}
		if sysPrompt == "" {
			if role == runtime.RoleShepherd {
				sysPrompt = prompts.Shepherd
			} else {
				sysPrompt = prompts.Worker
			}
		}
		// Resume resolution chain:
		//   1. override.ResumeUUID (operator picked explicitly per-member)
		//   2. resume_strategy="latest-in-cwd" → newest .jsonl in memberCwd
		//   3. fresh (override.Fresh=true also forces fresh even if 2 would resolve)
		var agentArgs []string
		if m.Agent == "claude-code" && !ov.Fresh {
			resumeUUID := ov.ResumeUUID
			if resumeUUID == "" && req.ResumeStrategy == "latest-in-cwd" {
				resumeUUID = latestClaudeSessionUUID(memberCwd)
			}
			if resumeUUID != "" {
				agentArgs = append(agentArgs, "--resume", resumeUUID)
			}
		}
		// Auto-clear exited session with the same name so re-applying a
		// template after a prior run doesn't fail with "already in use".
		if _, existingInfo := s.rt.Get(m.Name); existingInfo != nil && existingInfo.Exited {
			_ = s.rt.Stop(m.Name)
		}
		_, newSess, err := s.rt.Spawn(runtime.SpawnSpec{
			Name:         m.Name,
			AgentSlug:    m.Agent,
			Team:         team,
			Role:         role,
			Cwd:          memberCwd,
			SystemPrompt: sysPrompt,
			StatSheet:    m.StatSheet,
			AgentArgs:    agentArgs,
		})
		res := spawned{Name: m.Name, Role: string(m.Role)}
		if err != nil {
			res.Err = err.Error()
		} else {
			_, _ = s.rt.JoinTeam(m.Name, team, m.Role, m.BriefOverride)
			// If the spawned member is a shepherd, kick off its watch
			// loop so it actually starts ticking (otherwise it'd sit at
			// the trust prompt indefinitely).
			if role == runtime.RoleShepherd && newSess != nil {
				s.rt.BootstrapShepherd(newSess, m.Name)
			}
		}
		results = append(results, res)
	}
	// Persist team apply for resurrect. Member→claude_uuid is "" at apply
	// time; the List() refresh fills it in as Claude writes its JSONL.
	memberRecs := make([]map[string]any, 0, len(p.Members))
	for _, m := range p.Members {
		memberRecs = append(memberRecs, map[string]any{
			"name":        m.Name,
			"agent":       m.Agent,
			"role":        string(m.Role),
			"cwd":         func() string { if m.Cwd != "" { return m.Cwd }; return cwd }(),
			"claude_uuid": "",
		})
	}
	teamDir := filepath.Join(s.rt.StateDir(), "teams", team)
	_ = os.MkdirAll(teamDir, 0o700)
	apply := map[string]any{
		"template":     p.Name,
		"team":         team,
		"cwd":          cwd,
		"topology":     string(effectiveTopology),
		"members":      memberRecs,
		"last_active":  time.Now().UTC().Format(time.RFC3339),
	}
	if b, err := json.MarshalIndent(apply, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(teamDir, "apply.json"), b, 0o600)
	}
	s.rt.RecordEvent(runtime.Event{
		Kind: "template_applied", Actor: "runtime",
		Body: fmt.Sprintf("template %q applied as team %q (%d members)", p.Name, team, len(p.Members)),
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"template": p.Name, "team": team, "members": results,
	})
}

// resurrectTeamHandler — POST /api/v1/teams/{name}/resurrect
// Reads <stateDir>/teams/<name>/apply.json and re-spawns every member
// using its last-known claude_uuid as --resume. Used by the wizard's
// Saved-teams card.
func (s *Server) resurrectTeamHandler(w http.ResponseWriter, r *http.Request, team string) {
	teamDir := filepath.Join(s.rt.StateDir(), "teams", team)
	b, err := os.ReadFile(filepath.Join(teamDir, "apply.json"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no saved state for team " + team})
		return
	}
	var rec struct {
		Template string         `json:"template"`
		Cwd      string         `json:"cwd"`
		Topology string         `json:"topology"`
		Members  []struct {
			Name, Agent, Role, Cwd, ClaudeUUID string `json:"-"`
		} `json:"members"`
	}
	// Use a flexible decode so JSON tags match the saved shape.
	var loose struct {
		Template string                   `json:"template"`
		Cwd      string                   `json:"cwd"`
		Topology string                   `json:"topology"`
		Members  []map[string]interface{} `json:"members"`
	}
	if err := json.Unmarshal(b, &loose); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	_ = rec
	// Ensure team exists with saved topology
	_, _ = s.rt.CreateTeam(team, "", runtime.Topology(loose.Topology))
	type res struct {
		Name string `json:"name"`
		Err  string `json:"err,omitempty"`
	}
	var results []res
	for _, m := range loose.Members {
		name, _ := m["name"].(string)
		agent, _ := m["agent"].(string)
		role, _ := m["role"].(string)
		mcwd, _ := m["cwd"].(string)
		uuid, _ := m["claude_uuid"].(string)
		if mcwd == "" {
			mcwd = loose.Cwd
		}
		var args []string
		if uuid != "" && agent == "claude-code" {
			args = append(args, "--resume", uuid)
		}
		role0 := runtime.Role(role)
		if role0 != runtime.RoleShepherd {
			role0 = runtime.RoleWorker
		}
		// Try to source a default prompt
		var sysPrompt string
		if role0 == runtime.RoleShepherd {
			sysPrompt = prompts.Shepherd
		} else {
			sysPrompt = prompts.Worker
		}
		_, newSess, err := s.rt.Spawn(runtime.SpawnSpec{
			Name: name, AgentSlug: agent, Team: team, Role: role0, Cwd: mcwd,
			SystemPrompt: sysPrompt, AgentArgs: args,
		})
		r := res{Name: name}
		if err != nil {
			r.Err = err.Error()
		} else {
			_, _ = s.rt.JoinTeam(name, team, runtime.MembershipRole(role), "")
			if role0 == runtime.RoleShepherd && newSess != nil {
				s.rt.BootstrapShepherd(newSess, name)
			}
		}
		results = append(results, r)
	}
	s.rt.RecordEvent(runtime.Event{
		Kind: "team_resurrected", Actor: "operator",
		Body: fmt.Sprintf("team %q resurrected from saved state (%d members)", team, len(results)),
	})
	writeJSON(w, http.StatusOK, map[string]any{"team": team, "members": results})
}

// eventsHandler returns the most recent N events (default 200).
func (s *Server) eventsHandler(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": s.rt.Events(limit)})
}

// eventsStream — server-sent events of every runtime event. Stays open
// for the dashboard's live events strip. Each line:
//
//	data: {"id":"...","at":"...","kind":"...","actor":"...","body":"...","meta":{...}}
func (s *Server) eventsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Replay recent events first so the client has context.
	for _, e := range s.rt.Events(50) {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", string(b))
	}
	flusher.Flush()

	ch, unsub := s.rt.SubscribeEvents()
	defer unsub()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", string(b))
			flusher.Flush()
		}
	}
}

// decodeClaudeProjectDir converts Claude's "-home-openova-repos-iogrid"
// directory name back to "/home/openova/repos/iogrid". Hyphens are
// ambiguous (talent-mesh could be "/talent/mesh" or "/talent-mesh") —
// we resolve by trying each interpretation and picking the one that
// exists on disk. Falls back to naive "-"→"/" replacement if no
// candidate is found.
func decodeClaudeProjectDir(dirName string) string {
	if !strings.HasPrefix(dirName, "-") {
		return ""
	}
	trimmed := strings.TrimPrefix(dirName, "-")
	// Recursive search: at each hyphen, try both "/" and literal "-" and
	// pick the longest existing prefix. Bounded to keep cost predictable.
	resolved := resolveClaudeDirName("/", trimmed, 0)
	if resolved != "" {
		return resolved
	}
	// Last-resort: naive decode.
	return "/" + strings.ReplaceAll(trimmed, "-", "/")
}

func resolveClaudeDirName(prefix, remaining string, depth int) string {
	if depth > 12 || remaining == "" {
		full := prefix + remaining
		if _, err := os.Stat(full); err == nil {
			return full
		}
		return ""
	}
	// Try splitting at the leftmost hyphen.
	idx := strings.IndexByte(remaining, '-')
	if idx < 0 {
		full := prefix + remaining
		if _, err := os.Stat(full); err == nil {
			return full
		}
		return ""
	}
	head := remaining[:idx]
	tail := remaining[idx+1:]
	// Prefer the slash interpretation when the prefix+head exists as a dir.
	if head != "" {
		slashCandidate := prefix + head
		if fi, err := os.Stat(slashCandidate); err == nil && fi.IsDir() {
			r := resolveClaudeDirName(slashCandidate+"/", tail, depth+1)
			if r != "" {
				return r
			}
		}
	}
	// Else try keeping the hyphen literal and recursing.
	return resolveClaudeDirName(prefix, head+"-"+tail, depth+1)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ─── Kanban proxy ────────────────────────────────────────────────────────────

// kanbanIssues proxies GET /api/v1/kanban?repo=<github-url>&state=open&per_page=100
// to the GitHub REST API. Uses GITHUB_TOKEN from env if available for auth.
func (s *Server) kanbanIssues(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	repoURL := r.URL.Query().Get("repo")
	if repoURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "repo required"})
		return
	}
	// Convert github.com/org/repo → api.github.com/repos/org/repo/issues
	apiURL, err := githubAPIURL(repoURL, "/issues")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
	apiURL += "?state=" + state + "&per_page=100"
	body, status, err := githubAPIGet(apiURL)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	var raw []any
	if err := json.Unmarshal(body, &raw); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "invalid JSON from GitHub"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": raw})
}

// kanbanMove updates the status label on a GitHub issue via the API.
// POST /api/v1/kanban/move  body: {repo, issue_number, status_label, remove_labels}
func (s *Server) kanbanMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Repo         string   `json:"repo"`
		IssueNumber  int      `json:"issue_number"`
		StatusLabel  string   `json:"status_label"`   // "" = backlog (no status label)
		RemoveLabels []string `json:"remove_labels"`  // existing status labels to strip
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	// First: remove old status labels one by one.
	for _, lbl := range req.RemoveLabels {
		apiURL, err := githubAPIURL(req.Repo, fmt.Sprintf("/issues/%d/labels/%s", req.IssueNumber, lbl))
		if err != nil {
			continue
		}
		_, _, _ = githubAPIDelete(apiURL)
	}
	// Then: add new status label (if not backlog).
	if req.StatusLabel != "" {
		apiURL, err := githubAPIURL(req.Repo, fmt.Sprintf("/issues/%d/labels", req.IssueNumber))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		body, _ := json.Marshal(map[string]any{"labels": []string{req.StatusLabel}})
		if _, _, err := githubAPIPost(apiURL, body); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// githubAPIURL converts a github.com/org/repo URL to an api.github.com path.
func githubAPIURL(repoURL, suffix string) (string, error) {
	repoURL = strings.TrimRight(repoURL, "/")
	repoURL = strings.TrimSuffix(repoURL, ".git")
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "git@github.com:"} {
		if strings.HasPrefix(repoURL, prefix) {
			slug := strings.TrimPrefix(repoURL, prefix)
			return "https://api.github.com/repos/" + slug + suffix, nil
		}
	}
	return "", fmt.Errorf("unsupported repo URL format: %s", repoURL)
}

func githubAPIGet(url string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode, nil
}

func githubAPIPost(url string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode, nil
}

func githubAPIDelete(url string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode, nil
}

// globalMDHandler returns the content of ~/.claude/CLAUDE.md (the user-global
// instruction file that Claude Code reads on every session). Read-only; used
// by the AgentSettings Prompt tab to show all three instruction sources.
func (s *Server) globalMDHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"body": ""})
		return
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"body": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"body": string(data)})
}

// ─── vault handlers ──────────────────────────────────────────────────────────

func (s *Server) vaultRoot(w http.ResponseWriter, r *http.Request) {
	if s.Vault == nil {
		http.Error(w, "vault not initialised", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.Vault.List())
	case http.MethodPost:
		var body struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Label    string `json:"label"`
			EnvVar   string `json:"env_var"`
			Value    string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if body.Provider == "" || body.Value == "" {
			http.Error(w, "provider and value required", http.StatusBadRequest)
			return
		}
		id, err := s.Vault.Set(body.ID, body.Provider, body.Label, body.EnvVar, body.Value)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": id})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) vaultByID(w http.ResponseWriter, r *http.Request) {
	if s.Vault == nil {
		http.Error(w, "vault not initialised", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/vault/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if err := s.Vault.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

func (s *Server) vaultProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	type providerResp struct {
		ID          string `json:"id"`
		Label       string `json:"label"`
		DefaultEnv  string `json:"default_env"`
		Description string `json:"description"`
	}
	out := make([]providerResp, 0, len(vault.KnownProviders))
	order := []string{"claude-oauth", "anthropic-api", "openrouter", "newapi", "github-pat", "gitlab-pat", "gitea", "custom"}
	for _, id := range order {
		pm := vault.KnownProviders[id]
		out = append(out, providerResp{ID: id, Label: pm.Label, DefaultEnv: pm.DefaultEnv, Description: pm.Description})
	}
	writeJSON(w, http.StatusOK, out)
}

// ─── Claude OAuth token handlers (R5 / #136) ────────────────────────────────
//
// The spawn wizard needs to: (a) list available Claude accounts, (b) accept a
// pasted credentials.json for the "manual login" path, (c) harvest the
// credentials that claude-code writes into a freshly-OAuth'd agent's home
// directory back into the vault so the next agent reuses them automatically.

func (s *Server) claudeTokensHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Vault == nil {
		writeJSON(w, http.StatusOK, map[string]any{"tokens": []any{}})
		return
	}
	type tokenView struct {
		ID        string    `json:"id"`
		Label     string    `json:"label"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	creds := s.Vault.ListByProvider("claude-oauth")
	out := make([]tokenView, 0, len(creds))
	for _, c := range creds {
		out = append(out, tokenView{ID: c.ID, Label: c.Label, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt})
	}
	// Also surface a synthetic "host" token if the host filesystem has
	// claude-code logged in — lets the wizard show "Use host login" as an
	// option even before the vault is seeded.
	if home, err := os.UserHomeDir(); err == nil {
		if _, statErr := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); statErr == nil {
			out = append(out, tokenView{ID: "host", Label: "Host login (~/.claude/.credentials.json)"})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// claudeTokensPaste accepts a raw credentials.json payload (the contents
// the operator copied from their own ~/.claude/.credentials.json) and
// stores it in the vault as a claude-oauth credential. Used when the
// operator is on a fresh chepherd install + has the JSON handy.
func (s *Server) claudeTokensPaste(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Vault == nil {
		http.Error(w, "vault not initialised", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Label string `json:"label"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.Value == "" {
		http.Error(w, "value required", http.StatusBadRequest)
		return
	}
	// Sanity-check it's the expected shape — must parse as JSON with a
	// "claudeAiOauth" or "anthropic" key.
	var probe map[string]any
	if err := json.Unmarshal([]byte(body.Value), &probe); err != nil {
		http.Error(w, "value is not valid JSON", http.StatusBadRequest)
		return
	}
	if _, hasOAuth := probe["claudeAiOauth"]; !hasOAuth {
		// Don't reject outright — future claude-code versions may rename
		// the field. Warn via a 200 with a "warning" key.
	}
	label := body.Label
	if label == "" {
		label = "pasted-" + time.Now().UTC().Format("2006-01-02-15:04")
	}
	id, err := s.Vault.Set("", "claude-oauth", label, "", body.Value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// claudeTokensHarvest reads the .claude/.credentials.json file inside an
// agent's home directory (which claude-code writes after a successful
// OAuth login from within the container) and stores it into the vault.
// Called by the spawn wizard after the operator completes the OAuth flow
// — closes the loop so subsequent agents reuse the captured token.
//
// Body: {"agent_name": "<session-name>", "label": "<optional label>"}
func (s *Server) claudeTokensHarvest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Vault == nil {
		http.Error(w, "vault not initialised", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		AgentName string `json:"agent_name"`
		Label     string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.AgentName == "" {
		http.Error(w, "agent_name required", http.StatusBadRequest)
		return
	}
	credPath := filepath.Join(s.rt.StateDir(), "agents", body.AgentName, "home", ".claude", ".credentials.json")
	data, err := os.ReadFile(credPath)
	if err != nil {
		http.Error(w, "no credentials at "+credPath+": "+err.Error(), http.StatusNotFound)
		return
	}
	label := body.Label
	if label == "" {
		label = "harvested-from-" + body.AgentName + "-" + time.Now().UTC().Format("2006-01-02-15:04")
	}
	id, err := s.Vault.Set("", "claude-oauth", label, "", string(data))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "label": label})
}

// ─── Claude OAuth login-capture flow (R5 redo / #136) ───────────────────────
//
// Spawns an ephemeral agent with NO claude credentials in the vault path.
// claude-code's first-run logic prints an OAuth URL; chepherd reads it
// off the agent's ring buffer and surfaces it to the operator as a
// clickable link. After the operator authenticates in their browser and
// pastes the code, chepherd injects the code into the agent's PTY stdin
// and harvests the credentials.json claude-code writes to ~/.claude/
// into the vault. The ephemeral agent is then terminated.

// claudeOAuthURLRegex captures the canonical claude.com/cai/oauth login
// URL claude-code prints (longest match wins — claude-code first prints
// a shorter claude.ai link then overwrites with the full one via \r).
var claudeOAuthURLRegex = regexp.MustCompile(`https://claude\.(?:ai|com)/[^\s"'<>\x1b\r\n]+`)

// ansiAndCRStrip is the same scrubber the terminal widget uses to
// reassemble PTY-wrapped URLs into one matchable string.
var ansiAndCRStrip = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)

func scanForClaudeOAuthURL(data []byte) string {
	// Strip ANSI escapes, then \r and \n, so wrapped URLs reassemble.
	clean := ansiAndCRStrip.ReplaceAll(data, nil)
	clean = []byte(strings.ReplaceAll(strings.ReplaceAll(string(clean), "\r", ""), "\n", ""))
	all := claudeOAuthURLRegex.FindAll(clean, -1)
	if len(all) == 0 {
		return ""
	}
	best := all[0]
	for _, m := range all[1:] {
		if len(m) > len(best) {
			best = m
		}
	}
	return string(best)
}

// claudeLoginBegin spawns an ephemeral agent with NO Claude credentials
// in its secrets dir. claude-code's first-run prompt prints the OAuth
// URL within seconds; the URL is captured via /login-url polling.
func (s *Server) claudeLoginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Vault == nil {
		http.Error(w, "vault not initialised", http.StatusServiceUnavailable)
		return
	}
	name := fmt.Sprintf("oauth-capture-%d", time.Now().UnixNano())
	// Pass an unresolvable ClaudeTokenID to force materializeAgentSecrets
	// to skip both vault + host fallback.
	// Use a small dedicated cwd to avoid mounting the operator's full
	// home dir + interfering with their .mcp.json symlink.
	cwd := filepath.Join(s.rt.StateDir(), "oauth-cwd")
	_ = os.MkdirAll(cwd, 0o755)
	_, sess, err := s.rt.Spawn(runtime.SpawnSpec{
		Name:          name,
		AgentSlug:     "claude-code",
		Team:          "default",
		Role:          runtime.RoleWorker,
		Cwd:           cwd,
		ClaudeTokenID: "__none__",
	})
	if err != nil {
		http.Error(w, "spawn: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// claude-code on first run shows a sequence of prompts before the
	// OAuth URL: theme picker → "use API key?" (if env has one) → intro
	// "Press Enter to continue" → "Bypass permissions" warning. Watch
	// the ring buffer for marker text and inject the right reply only
	// AFTER each marker appears — blind Enter-spam races the prompts.
	if sess != nil {
		go autoDismissClaudeFirstRunPrompts(sess)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"name": name})
}

// autoDismissClaudeFirstRunPrompts watches sess's ring buffer for the
// first-run prompts claude-code prints + injects the right reply per
// prompt so the OAuth URL surfaces without operator interaction.
// Returns once the canonical Claude OAuth URL is detected or after a
// timeout — the polling URL endpoint handles the rest.
func autoDismissClaudeFirstRunPrompts(sess *session.Session) {
	// Each step: marker substring → bytes to inject after it's seen.
	// Order matters; we only advance to step N after step N-1's marker
	// was seen (so a prompt that's already been dismissed doesn't re-fire).
	type step struct {
		marker string
		reply  []byte
		// If pause>0, sleep after injecting (lets the next prompt render).
		pause time.Duration
	}
	// claude-code's TUI renders each word via cursor-position escape
	// codes (e.g. "\x1b[9G text"), not real spaces, so stripping ANSI
	// from the ring buffer leaves words concatenated. Markers below are
	// the SPACE-LESS form ("ChoosethetextstylethatlooksbestwithyourTerminal"
	// etc.) — they must match the post-strip text.
	steps := []step{
		// 1. Theme picker. Default highlight "Dark mode ✔" — Enter accepts.
		{marker: "Choosethetextstyle", reply: []byte("\r"), pause: 1200 * time.Millisecond},
		// 2. "Select login method" — default option 1 is "Claude account
		//    with subscription". Enter accepts. (Only fires when there's
		//    no usable .credentials.json + .claude.json in agent home.)
		{marker: "Selectloginmethod", reply: []byte("\r"), pause: 1200 * time.Millisecond},
		// 3. Legacy/alternate API-key conflict prompt (safety net — only
		//    fires if ANTHROPIC_API_KEY somehow leaked into env).
		{marker: "DoyouwanttousethisAPIkey", reply: []byte("2\r"), pause: 1000 * time.Millisecond},
		// 4. "Do you trust the files in this folder?" — fires on every
		//    fresh container the first time it cd's into the workspace.
		//    Default is "Yes, I trust this folder" highlighted → Enter
		//    accepts. We mount the repo and let claude-code at it.
		{marker: "Yes,Itrustthisfolder", reply: []byte("\r"), pause: 1200 * time.Millisecond},
		// 5. MCP server approval — chepherd injects its own MCP server
		//    via .mcp.json into every workspace. Default is "Use this
		//    and all future MCP servers in this project". Enter accepts.
		{marker: "UsethisandallfutureMCPserversinthisproject", reply: []byte("\r"), pause: 1200 * time.Millisecond},
		// 6. Bypass-permissions warning (because we pass --dangerously-skip-permissions).
		//    Options are "1. No, exit" (default highlight) and "2. Yes, I accept".
		//    Special-case sentinel — handled in the loop below with split
		//    Down-arrow + 800ms wait + Enter so claude-code's Ink TUI
		//    has time to process the selection-change before the confirm.
		{marker: "BypassPermissionsmode", reply: []byte("__BYPASS_PROMPT__"), pause: 2000 * time.Millisecond},
		// 7. Intro "Press Enter to continue" (some claude-code versions
		//    show this between login-method pick and the welcome screen).
		{marker: "PressEntertocontinue", reply: []byte("\r"), pause: 1000 * time.Millisecond},
	}

	// Only look at the TAIL of the cleaned buffer — the cumulative buffer
	// contains every screen claude-code has ever drawn, including the
	// option labels of already-dismissed prompts. If we matched against
	// the whole buffer, e.g. "Yes,Itrustthisfolder" (the option label of
	// the trust prompt) would still appear minutes after it was
	// dismissed, and step 6 ("BypassPermissionsmode" → "2\r") could fire
	// during the trust screen with disastrous side effects (selecting
	// "No, exit" on the trust prompt, killing the container).
	const tailWindow = 1500
	fired := make([]bool, len(steps))
	deadline := time.Now().Add(60 * time.Second)
	idleTicks := 0
	for time.Now().Before(deadline) {
		sub, replay, err := sess.Subscribe(1)
		if err != nil {
			return
		}
		sess.Unsubscribe(sub)
		clean := ansiAndCRStrip.ReplaceAll(replay, nil)
		text := strings.ReplaceAll(strings.ReplaceAll(string(clean), "\r", ""), "\n", "")
		text = strings.ReplaceAll(text, " ", "") // TUI strips real spaces
		// Match only against the most recent window.
		tail := text
		if len(text) > tailWindow {
			tail = text[len(text)-tailWindow:]
		}

		// Fire AT MOST ONE step per iteration so we don't pile inputs
		// onto a screen that hasn't transitioned yet.
		fireIdx := -1
		for i, st := range steps {
			if fired[i] {
				continue
			}
			if strings.Contains(tail, st.marker) {
				fireIdx = i
				break
			}
		}
		if fireIdx >= 0 {
			reply := steps[fireIdx].reply
			if string(reply) == "__BYPASS_PROMPT__" {
				// Special-case: Bypass-permissions menu. Send Down,
				// wait for the highlight to move, then Enter.
				_, _ = sess.Inject([]byte("\x1b[B"))
				time.Sleep(800 * time.Millisecond)
				_, _ = sess.Inject([]byte("\r"))
			} else {
				_, _ = sess.Inject(reply)
			}
			fired[fireIdx] = true
			time.Sleep(steps[fireIdx].pause)
			idleTicks = 0
			continue
		}
		// Exit early once OAuth URL is in tail and no remaining steps
		// to fire — this is the OAuth-capture exit path.
		if strings.Contains(tail, "claude.com/cai/oauth") || strings.Contains(tail, "claude.ai/oauth") {
			return
		}
		idleTicks++
		// 8 idle ticks * 500ms = 4s with no marker hit + no OAuth URL
		// → agent has reached the welcome screen, our job is done.
		if idleTicks > 8 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// claudeLoginURL polls the ephemeral agent's ring buffer for the OAuth URL.
// Returns 202 with {} if not yet visible; 200 with {url} once seen.
func (s *Server) claudeLoginURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/claude-tokens/login-url/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	sess, _ := s.rt.Get(name)
	if sess == nil {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	sub, replay, err := sess.Subscribe(1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sess.Unsubscribe(sub)
	url := scanForClaudeOAuthURL(replay)
	if url == "" {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

// claudeLoginSubmit accepts the auth code the operator pasted, injects
// it into the ephemeral agent's PTY stdin, polls for the credentials
// file to appear, harvests it into the vault, and terminates the agent.
//
// Body: {"code": "...", "label": "optional vault label"}
func (s *Server) claudeLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/claude-tokens/login-submit/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	var body struct {
		Code  string `json:"code"`
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.Code == "" {
		http.Error(w, "code required", http.StatusBadRequest)
		return
	}
	sess, _ := s.rt.Get(name)
	if sess == nil {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	// Inject the auth code into the agent's stdin + Enter.
	_, _ = sess.Inject([]byte(body.Code))
	time.Sleep(150 * time.Millisecond)
	_, _ = sess.Inject([]byte("\r"))

	// Poll for the credentials. The agent home dir is bind-mounted with
	// :U into a UID-remapped namespace, so chepherd (UID 1000 on host)
	// can't read files claude-code (UID 1000 in container = host UID
	// ~100999) writes inside that namespace. Use `podman exec` so the
	// read runs INSIDE the container's namespace where the file is
	// readable. Fallback: try the host path in case the runtime mode is
	// bare-exec or :U remapping didn't kick in.
	credPathHost := filepath.Join(s.rt.StateDir(), "agents", name, "home", ".claude", ".credentials.json")
	containerName := "chepherd-agent-" + name
	deadline := time.Now().Add(40 * time.Second)
	var data []byte
	var lastErr string
	for time.Now().Before(deadline) {
		// First try `podman exec` — works regardless of UID remapping.
		out, err := exec.Command("podman", "exec", containerName, "cat", "/home/agent/.claude/.credentials.json").Output()
		if err == nil && len(out) > 0 && bytes.HasPrefix(bytes.TrimSpace(out), []byte("{")) {
			data = out
			break
		}
		if err != nil {
			lastErr = "podman exec: " + err.Error()
		}
		// Fallback: try the bind-mounted host path (works on BareExec).
		if b, err := os.ReadFile(credPathHost); err == nil && len(b) > 0 {
			data = b
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if data == nil {
		http.Error(w, "credentials never appeared — did the auth code work? "+lastErr, http.StatusGatewayTimeout)
		return
	}
	label := body.Label
	if label == "" {
		label = "oauth-captured-" + time.Now().UTC().Format("2006-01-02-15:04")
	}
	id, err := s.Vault.Set("", "claude-oauth", label, "", string(data))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Terminate the ephemeral agent — its job is done.
	_ = s.rt.Stop(name)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "label": label})
}

// claudeLoginCancel terminates an in-progress login-capture agent
// without saving anything. Idempotent.
func (s *Server) claudeLoginCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/claude-tokens/login-cancel/")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	_ = s.rt.Stop(name)
	writeJSON(w, http.StatusOK, map[string]string{"cancelled": name})
}

// stripEnvKey returns env with all KEY=... entries matching key removed.
// Used to keep ANTHROPIC_API_KEY out of OAuth-mode agent containers.
func stripEnvKey(env []string, key string) []string {
	prefix := key + "="
	out := env[:0]
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func logMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		_ = fmt.Sprintf // placeholder for future structured logging
		_ = start
	})
}
