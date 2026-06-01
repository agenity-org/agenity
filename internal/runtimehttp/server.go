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
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/federation"
	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/auth"
	"github.com/chepherd/chepherd/internal/canon"
	"github.com/chepherd/chepherd/internal/catalog"
	"github.com/chepherd/chepherd/internal/discovery"
	"github.com/chepherd/chepherd/internal/profile"
	"github.com/chepherd/chepherd/internal/prompts"
	"github.com/chepherd/chepherd/internal/ptyhost/agentcatalog"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
	"github.com/chepherd/chepherd/internal/roles"
	"github.com/chepherd/chepherd/internal/runtime"
	"github.com/chepherd/chepherd/internal/webrtcrtc"
	"github.com/chepherd/chepherd/internal/skills"
	"github.com/chepherd/chepherd/internal/templateregistry"
	"github.com/chepherd/chepherd/internal/vault"
)

// Server hosts chepherd runtime endpoints. Caller is responsible for
// listening on a port + calling http.Serve(listener, server.Handler()).
type Server struct {
	rt        *runtime.Runtime
	WebDir    string            // optional: serve Astro static build from this dir
	Vault     *vault.Vault      // optional: credential vault (nil = vault API returns 503)
	Auth      auth.AuthProvider // optional: auth provider (nil = no token validation, dev only)
	AuthToken string            // bearer token required on /api/v1/* (#139) — empty disables enforcement
	Profile   *profile.Profile  // optional: deployment profile, surfaced via /healthz (#129)

	// v0.9.2 (#208): A2A endpoints — GET /.well-known/agent-card.json and
	// POST /jsonrpc. Wired by cmd/run.go before ServeOn; nil disables
	// (legacy callers / unit tests). authMiddleware bypasses these paths
	// because A2A advertises its own securitySchemes on the Agent Card.
	A2ACard   *a2a.AgentCard
	A2ARouter *a2a.Router
	// v0.9.3 #225 row A2 — SSE broker for SendStreamingMessage +
	// ResubscribeTask. nil disables /a2a/stream/* + the streaming
	// JSON-RPC methods return -32004.
	StreamBroker *a2a.StreamBroker

	// v0.9.3 #225 row B2 — ES256 signing-key lifecycle. JWKSBody is
	// the marshalled JSON published at /.well-known/jwks.json; empty
	// disables the endpoint. ES256Priv is the in-memory handle the
	// runtime uses to sign outbound JWTs (B3 — peers verify via JWKS
	// lookup against this body).
	JWKSBody  []byte
	ES256Priv *ecdsa.PrivateKey

	// KeyStore is the v0.9.4 #505 Wave T2 daemon-owned multi-key
	// signing store with rotation + overlap window. When non-nil it
	// supersedes the legacy single-key JWKSBody + ES256Priv fields:
	// JWKS handler serves KeyStore.JWKS() dynamically, jwt mint signs
	// via KeyStore.Sign (per-key kid). Runners never carry their own
	// signing keys; they verify inbound JWTs by fetching this daemon's
	// JWKS. Legacy fields are retained as fallback for unit tests that
	// construct Server without persistence.
	KeyStore *auth.KeyStore

	// OrgID identifies this daemon's organization. Surfaced as the
	// iss claim in cross-org JWTs minted via /api/v1/federation/jwt
	// (#557 Wave F8.1). When empty, the cross-org mint endpoint
	// returns 503 ("not configured"). Production deploys set this
	// from cmd/run.go's --org-id flag / CHEPHERD_ORG_ID env.
	OrgID string

	// GrantCheck is the RBAC dispatch seam for POST /api/v1/jwt/mint
	// (#468 Wave D2). nil = default allow-all so the JWT pipeline is
	// exercisable end-to-end before the grant store lands. Wave D3
	// (#469) wires the persistence-backed check here without touching
	// the mint endpoint's wire shape.
	GrantCheck GrantCheckFn

	// GrantStore backs the §13 grant CRUD endpoints + the production
	// GrantCheck implementation (#469 Wave D3). nil disables the CRUD
	// surface (responds 503) — the JWT mint endpoint stays operational
	// in stub-allow-all mode in that case.
	GrantStore persistence.RBACGrantRepository

	// CrossOrgGrantCheck is the override seam for the cross-org federation
	// mint (#557 F8.1). When non-nil it is threaded into crossOrgGrantAdapter
	// as the explicit check function, taking precedence over GrantStore.
	// Use in tests or cmd/run.go to inject a store-backed allow/deny function
	// without touching the GrantStore field. nil + GrantStore nil → deny-all
	// (fail-closed, #639).
	CrossOrgGrantCheck func(callerOrg, scope string) error

	// AuditEventStore backs the §10-step-24 audit persistence +
	// GET /api/v1/audit/events query endpoint (#489 Wave AU2). nil
	// disables persistence — the AU1 stub-log path stays active so
	// the WS receiver doesn't fail open, but query API responds 503.
	AuditEventStore persistence.AuditEventRepository

	// DaemonOrgID is this daemon's org identifier. Stamped on every
	// audit event at ingest so cross-org events from federation peers
	// stay scoped to the receiver-daemon's org. Empty disables the
	// org-partition guard (dev / unit-test mode); production
	// deployments MUST set it.
	DaemonOrgID string

	// v0.9.3 #225 row C1 — federation orchestrator + cached agent-card
	// store. Federation is nil when --federation-registry-url is empty;
	// AgentCardStore is always set when persistence is wired (used by
	// the /api/v1/peers endpoint to enumerate cached peers).
	Federation     *federation.Federation
	AgentCardStore persistence.AgentCardRepository

	// FederationMTLS holds the daemon's federation leaf cert + pinned-
	// CA pool when --federation-mtls is enabled (#527 Wave T3.1).
	// cmd/run.go uses this to build the optional federation-facing
	// mTLS HTTP listener (`--federation-listen` flag) on a separate
	// port so cross-org peers terminate mTLS at the dedicated surface
	// while the dashboard listener stays plain TLS. nil = mTLS
	// disabled (dev/test default).
	FederationMTLS *federation.MTLSConfig
	// TaskStore is wired when persistence is enabled; surfaces the
	// A2A Task records for the dashboard's A2A Inbox tab (#225 row G2).
	TaskStore persistence.TaskRepository

	// SessionStore — #314 D4. Persisted-but-not-live session records
	// so the dashboard's reconciler sees what's available even when
	// the live runtime hasn't (yet) re-spawned them. Fixes the 5s 404
	// loop where the dashboard kept attaching to "full-stack".
	SessionStore persistence.SessionRepository

	// #194 — Skill Library (10 LEAN builtins + user-defined CRUD).
	skills *skills.Store

	// #194 — Role catalog (12 builtins + user-defined CRUD).
	roles *roles.Store

	// #198 — Operator Canon (Layer 1 of the 3-layer agent context).
	canon *canon.Store

	// #175 — Team Template Registry (6 visible + 3 hidden builtins +
	// user-defined CRUD). Templates compose Roles + Skills.
	templates *templateregistry.Store

	// #174 — Discovery Layer service. Initialised in New(); registers
	// the 4 builtin providers (GitHub, GitLab, Bitbucket, Gitea) so
	// SpawnWizard Stage-2 can auto-enumerate orgs + repos.
	discovery *discovery.Service

	upgrader websocket.Upgrader

	// #504 Wave R1 — registry of chepherd-runner processes that have
	// dialed in via /api/v1/runners/register. In-memory for R1; Wave
	// R5 cutover may persist via the agent registry. Lazily-init via
	// runnerReg() so the daemon doesn't carry the alloc when nothing
	// has registered yet.
	runnerRegMu    sync.Mutex
	runnerRegistry *runnerRegistry
}

// New constructs a Server bound to the runtime.
func New(rt *runtime.Runtime) *Server {
	// #174 — Discovery service with 4 builtin providers.
	disc := discovery.NewService()
	disc.RegisterProvider(discovery.NewGitHubProvider())
	disc.RegisterProvider(discovery.NewGitLabProvider())
	disc.RegisterProvider(discovery.NewBitbucketProvider())
	s := &Server{
		rt:        rt,
		discovery: disc,
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
	if rt != nil {
		// #194 — Skill Library.
		if sk, err := skills.NewStore(rt.StateDir()); err == nil {
			s.skills = sk
		}
		// #194 — Role catalog.
		if r, err := roles.NewStore(rt.StateDir()); err == nil {
			s.roles = r
		}
		// #198 — Operator Canon.
		if c, err := canon.NewStore(rt.StateDir()); err == nil {
			s.canon = c
		}
		// #175 — Team Template Registry.
		if tmpl, err := templateregistry.NewStore(rt.StateDir()); err == nil {
			s.templates = tmpl
		}
	}
	return s
}

// Handler returns the HTTP mux ready to be served.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/api/v1/sessions", s.sessionsRoot)
	mux.HandleFunc("/api/v1/sessions/", s.sessionByName)
	// #504 Wave R1 — chepherd-runner registration WS + read-side
	// list. The WS endpoint accepts a register frame, assigns a SID,
	// then receives audit notifications until the runner exits.
	// The list endpoint is the seam Wave D1 (#467) builds the
	// /api/v1/agents/ Agent Card directory atop.
	mux.HandleFunc("/api/v1/runners/register", s.handleRunnerRegister)
	mux.HandleFunc("/api/v1/runners", s.handleRunnersList)
	// #489 Wave AU2 — audit event query endpoint. Auth-gated by the
	// same Bearer middleware that protects other /api/v1/* routes;
	// per-org scoped at the repository layer.
	mux.HandleFunc("/api/v1/audit/events", s.handleAuditEventsQuery)
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
	// #172 — first-class Agent entity CRUD.
	mux.HandleFunc("/api/v1/agents", s.agentsEntity)
	mux.HandleFunc("/api/v1/agents/", s.agentEntityByID)
	// #468 Wave D2 — per-call JWT mint endpoint (§15.2). ES256-signed,
	// 60s expiry, RBAC-gated. The grant check is a no-op stub until
	// Wave D3 wires the real persistence-backed check.
	mux.HandleFunc("/api/v1/jwt/mint", s.jwtMint)
	// #557 Wave F8.1 — cross-org JWT mint endpoint (daemon-Y side).
	// Hub /v1/federation/auth (F8 #498) forwards here.
	s.mountCrossOrgFederationMint(mux)
	mux.HandleFunc("/api/v1/agent-types", s.agentsCatalog)
	// #469 Wave D3 — RBAC grant CRUD (§13). Operator-facing surface
	// the dashboard Federation tab consumes to create / list / revoke
	// peering grants. The production GrantCheck is wired to this same
	// store via cmd/run.go so JWT mint (#468) actually enforces grants.
	mux.HandleFunc("/api/v1/grants", s.grantsRoot)
	mux.HandleFunc("/api/v1/grants/", s.grantByID)
	// #194 — Skill Library
	mux.HandleFunc("/api/v1/skills", s.skillsRoot)
	mux.HandleFunc("/api/v1/skills/", s.skillByID)
	// #194 — Role catalog
	mux.HandleFunc("/api/v1/roles", s.rolesRoot)
	mux.HandleFunc("/api/v1/roles/", s.roleByID)
	// #198 — Operator Canon (Layer 1 of 3-layer agent context)
	mux.HandleFunc("/api/v1/canon", s.canonRoot)
	// v0.9.3 #225 row C1 — federated peer registry view.
	mux.HandleFunc("/api/v1/peers", s.peersList)
	mux.HandleFunc("/api/v1/tasks", s.tasksList)
	// #311 C5.1 — WebRTC signaling relay endpoints. /webrtc/offer
	// answers inbound SDP offers by spawning a chepherd-side
	// PeerConnection. /webrtc/ice trickles ICE candidates into the
	// most-recent open PeerConnection. The DataChannel 'a2a' then
	// flows A2A traffic P2P over DTLS, bypassing this relay.
	mux.Handle("/webrtc/offer", webrtcrtc.HandleOffer(func() (*webrtcrtc.PeerConnection, error) {
		return webrtcrtc.NewPeerConnectionForAnswerer(webrtcrtc.Config{})
	}))
	mux.HandleFunc("/webrtc/ice", s.webrtcICE)
	mux.HandleFunc("/api/v1/canon/history", s.canonHistory)
	mux.HandleFunc("/api/v1/canon/rollback", s.canonRollback)
	// #175 — Team Template Registry (Skill-composing templates)
	mux.HandleFunc("/api/v1/team-templates", s.teamTemplatesRoot)
	mux.HandleFunc("/api/v1/team-templates/", s.teamTemplateByID)
	// #174 — discovery layer (auto-enumerate orgs + repos from saved tokens)
	mux.HandleFunc("/api/v1/discovery/", s.discoveryRouter)

	// #650 — per-runner /a2a/<sid>/ agent card endpoint. The D1 directory
	// (/api/v1/agents/) advertises agent card URLs of this form. Runners
	// in sibling-container mode don't expose their own HTTP listener, so
	// the daemon serves the card directly from its runtime session index.
	// Only /.well-known/agent-card.json is supported; other sub-paths
	// under /a2a/<sid>/ return 404 JSON (not SPA HTML).
	mux.HandleFunc("/a2a/", s.a2aSessionCardHandler)

	// #466 Wave R5 — DAEMON DE-A2A CUTOVER. Per V0.9.2-ARCH §5 #3 +
	// §22, A2A goes through per-runner endpoints at /a2a/<sid>/jsonrpc
	// (Wave R2 #463). The daemon's legacy /jsonrpc + agent-card paths
	// return 410-Gone with structured operator-visible diagnostics:
	//   - Deprecation: true (RFC 9745)
	//   - Sunset header (RFC 8594) — Wave R5 merge date
	//   - Link: rel="successor-version" pointing at Wave D1 directory
	//   - JSON-RPC -32601 body so A2A clients see a parseable error
	//
	// JWKS (T2 #505) is unaffected — daemon owns the keystore + JWKS
	// publication; runners verify peer JWTs against this URL.
	mux.Handle("/jsonrpc", r5A2ACutoverHandler("/jsonrpc"))
	mux.Handle(a2a.AgentCardPath, r5A2ACutoverHandler(a2a.AgentCardPath))
	mux.Handle(a2a.AgentCardAliasPath, r5A2ACutoverHandler(a2a.AgentCardAliasPath))
	// JWKS path stays daemon-owned — peers verify JWTs against this
	// URL whether minted for daemon-side admin endpoints or per-call
	// A2A authorization issued by the same daemon.
	if s.KeyStore != nil {
		mux.HandleFunc(a2a.JWKSPath, s.jwksDynamic)
	} else if len(s.JWKSBody) > 0 {
		a2a.RegisterJWKS(mux, s.JWKSBody)
	}
	// #466 — back-compat fields A2ACard / A2ARouter / StreamBroker
	// stay on the Server struct so existing test fixtures that
	// populate them don't fail to compile. They're no longer
	// consumed in this mount block.
	_ = s.A2ACard
	_ = s.A2ARouter
	_ = s.StreamBroker

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

	// #297 — one-release-window 301 redirect for bookmark holders who
	// still type /v0.9.2/. The dashboard route moved to /v0.9.3/ with
	// the v0.9.3 ship; this redirect ships in v0.9.3 and gets removed
	// in v0.9.4. The /v0.9.1/ redirect from #223 is dropped per the
	// one-release-window rule (two versions back = bookmark forfeit).
	mux.HandleFunc("/v0.9.2/", func(w http.ResponseWriter, r *http.Request) {
		target := "/v0.9.3/" + strings.TrimPrefix(r.URL.Path, "/v0.9.2/")
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})

	// Static file serving — only active when --web-dir is set.
	// SPA fallback: any path not matching a real file returns index.html.
	if s.WebDir != "" {
		fs := http.FileServer(http.Dir(s.WebDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// #143 — unknown API paths return JSON 404, not the SPA's
			// index.html. The "API silently returns marketing HTML"
			// antipattern was a debugging nightmare.
			if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/api-v08/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"unknown API path","path":"` + r.URL.Path + `"}`))
				return
			}
			p := filepath.Join(s.WebDir, filepath.Clean("/"+r.URL.Path))
			if _, err := os.Stat(p); os.IsNotExist(err) {
				http.ServeFile(w, r, filepath.Join(s.WebDir, "index.html"))
				return
			}
			fs.ServeHTTP(w, r)
		})
	}

	return logMiddleware(s.authMiddleware(mux))
}

// authMiddleware (#139) enforces Bearer-token auth on every /api/v1/* and
// /api-v08/v1/* path. Paths that DON'T require auth:
//   - /healthz       (liveness probe, must not require creds)
//   - /v08/...       (the dashboard SPA itself — auth happens via the
//     token the operator pastes into the page; the bundle
//     itself is just static HTML/JS)
//   - /_astro/...    (static assets)
//   - everything else served from WebDir (logo, fonts, etc.)
//
// When s.AuthToken is empty, this middleware is a no-op — useful for
// dev mode (operator builds chepherd locally + runs without `auth`).
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.AuthToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		// Public surfaces.
		switch {
		case r.URL.Path == "/healthz":
		case strings.HasPrefix(r.URL.Path, "/api/v1/"),
			strings.HasPrefix(r.URL.Path, "/api-v08/v1/"):
			// Enforce — Bearer token required.
			got := ""
			if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
				got = strings.TrimPrefix(h, "Bearer ")
			}
			if got == "" {
				got = r.Header.Get("X-Chepherd-Token")
			}
			if got == "" {
				if c, _ := r.Cookie("chepherd_token"); c != nil {
					got = c.Value
				}
			}
			if got == "" {
				got = r.URL.Query().Get("token")
			}
			if got == "" {
				http.Error(w, "missing Bearer token", http.StatusUnauthorized)
				return
			}
			if !constantTimeEqualString(got, s.AuthToken) {
				http.Error(w, "invalid Bearer token", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func constantTimeEqualString(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
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
//
//	POST /api/v1/git-providers  (register / update)
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

		// Validate-before-save: call the provider's /user endpoint with
		// the token. Bad tokens (401/403) get rejected here so the
		// "stale provider" category never enters state. Embedded
		// skips validation (no remote API). Network errors are
		// considered transient and let the save proceed — the user can
		// retry discovery and we don't want to lose a working token
		// because of a momentary outage.
		if req.Kind != runtime.GitProviderEmbedded {
			if err := validateProviderToken(req.Kind, req.RepoURL, req.Token); err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"error":  "token rejected by " + string(req.Kind),
					"detail": err.Error(),
				})
				return
			}
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

// gitProviderByID — DELETE /api/v1/git-providers/{id} OR
// DELETE /api/v1/git-providers/?id=<composite-id>
//
// git-provider IDs are composite ("github:https://github.com/<org>/<repo>")
// and CANNOT be passed via path segment — Go's http.ServeMux issues a
// 301 redirect that collapses "//" → "/" before this handler runs,
// breaking the lookup. Composite IDs must use the query-param form.
// Opaque slug IDs ("embedded") still work as path segments. Same fix
// as the discovery handler (#200 walk).
func (s *Server) gitProviderByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Query-param form wins (composite IDs); fall back to path segment
	// for opaque slugs.
	id := r.URL.Query().Get("id")
	if id == "" {
		rawTail := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v1/git-providers/")
		id, _ = url.QueryUnescape(rawTail)
	}
	if id == "" {
		http.NotFound(w, r)
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
		"logged_in":    true,
		"login_method": loginMethod,
		"subscription": creds.ClaudeAiOauth.SubscriptionType,
		"rate_tier":    creds.ClaudeAiOauth.RateLimitTier,
		"expires_at":   creds.ClaudeAiOauth.ExpiresAt,
		"scopes":       creds.ClaudeAiOauth.Scopes,
	})
}

// MemberOverrideReq lets the operator specialize a single template member
// at apply time — pick a specific resume UUID, swap cwd, or replace the
// per-role prompt without forking the YAML.
type MemberOverrideReq struct {
	ResumeUUID    string `json:"resume_uuid,omitempty"`
	Cwd           string `json:"cwd,omitempty"`
	Prompt        string `json:"prompt,omitempty"`
	Fresh         bool   `json:"fresh,omitempty"`           // explicit opt-out from resume_strategy
	Agent         string `json:"agent,omitempty"`           // override agent CLI for this member (#164)
	ClaudeTokenID string `json:"claude_token_id,omitempty"` // override Claude OAuth credential (#164)
}

// promptsHandler returns the default system prompt for a given role so
// the SpawnModal can pre-fill its textarea + the operator can tweak.
//
//	GET /api/v1/prompts/worker       → { role: "worker",      prompt: "..." }
//	GET /api/v1/prompts/scrummaster  → { role: "scrummaster", prompt: "..." }
//	GET /api/v1/prompts/shepherd     → { role: "shepherd",    prompt: "..." }
//	                                   (legacy back-compat alias; same body)
//
// Refs #292.
func (s *Server) promptsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	role := strings.TrimPrefix(r.URL.Path, "/api/v1/prompts/")
	var body string
	switch role {
	case "scrummaster", "shepherd": // legacy alias for back-compat (#292)
		body = prompts.ScrumMaster
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
	// #383 P0 — surface the ACTUAL container runtime in use ("podman" |
	// "docker" | "bare"). The `profile.spawner` field below is
	// hardcoded to "podman-sidecar" by LocalRuntimeSpawner regardless
	// of whether the cr fell back to BareExec; that was misleading
	// operators into bisecting unrelated PRs when an env-level image
	// gap caused DetectRuntime to silently fallback to bare. The
	// `container_runtime` field is the source of truth; if it shows
	// "bare" in production, you've got a missing chepherd-agent image
	// (run `make agent-image`).
	if s.rt != nil {
		if cr := s.rt.ContainerRuntime(); cr != nil {
			resp["container_runtime"] = cr.Name()
		}
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
// cloneURL, when non-empty, is the specific repo URL the operator selected
// in the Stage 2 discovery tree (#651). It overrides the provider's generic
// base RepoURL (e.g. "https://github.com") so git clone targets the right repo.
func (s *Server) resolveProviderCwd(providerID, fallbackCwd, cloneURL string) (string, error) {
	if providerID == "" {
		if fallbackCwd == "" {
			fallbackCwd, _ = os.UserHomeDir()
		}
		// #594 — translate v0.9-wizard's hardcoded
		// '/home/chepherd/...' (container-only path) to the daemon's
		// actual host home when running host-direct. The wizard was
		// designed for the canonical scripts/start.sh topology where
		// chepherd runs in a container as user 'chepherd'; on
		// host-direct deploy /home/chepherd doesn't exist and spawn
		// fails with the misleading 'fork/exec /usr/bin/podman: no
		// such file' error (the real failure is os/exec chdir to
		// non-existent cwd before exec).
		if strings.HasPrefix(fallbackCwd, "/home/chepherd/") {
			if home, err := os.UserHomeDir(); err == nil && home != "/home/chepherd" {
				translated := home + strings.TrimPrefix(fallbackCwd, "/home/chepherd")
				if _, statErr := os.Stat(translated); statErr == nil {
					fallbackCwd = translated
				}
			}
		}
		// #649 — when running containerised and cwd defaulted to the
		// bare container home (/home/chepherd), toHostPath cannot
		// translate it (the prefix table only covers sub-paths). The
		// resulting mount -v /home/chepherd:/home/chepherd:rw hits a
		// root-owned dir on the host → OCI permission denied → agent
		// container stuck in Created. Fall back to /home/chepherd/repos
		// which IS in the prefix table and maps to CHEPHERD_HOST_REPOS_DIR.
		if fallbackCwd == "/home/chepherd" && os.Getenv("CHEPHERD_HOST_REPOS_DIR") != "" {
			if reposCwd := "/home/chepherd/repos"; func() bool { _, e := os.Stat(reposCwd); return e == nil }() {
				fallbackCwd = reposCwd
			}
		}
		// #594 Fix 2 — pre-validate cwd exists so we surface
		// actionable errors instead of the kernel-level ENOENT from
		// os/exec.
		if _, err := os.Stat(fallbackCwd); err != nil {
			return fallbackCwd, fmt.Errorf("spawn cwd does not exist: %s (check wizard Stage 2 Repo selection or chepherd state-dir layout)", fallbackCwd)
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
			// Embedded Gitea (#137) — ensure the sidecar container is
			// running, then clone the workspace repo into the state dir.
			repoName := p.DisplayName
			if repoName == "" {
				repoName = "workspace"
			}
			info, err := runtime.EnsureEmbeddedGitea(s.rt.StateDir(), repoName)
			if err != nil {
				return fallbackCwd, fmt.Errorf("embedded gitea: %w", err)
			}
			dir := filepath.Join(s.rt.StateDir(), "workspaces", "embedded-"+sanitizeID(repoName))
			if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
				_ = os.MkdirAll(dir, 0o755)
				cmd := exec.Command("git", "clone", "--depth=1", info.CloneURLForRepo(repoName), dir)
				if out, err := cmd.CombinedOutput(); err != nil {
					return dir, fmt.Errorf("embedded git clone: %w (%s)", err, strings.TrimSpace(string(out)))
				}
			}
			return dir, nil
		}
		// External provider — clone if needed, return clone path.
		// #138 fix: never embed the PAT in the clone URL (would persist
		// in .git/config). Use GIT_ASKPASS via a short-lived helper
		// script that prints the token, then git clones with the bare
		// HTTPS URL → .git/config has NO credentials, only the URL.
		//
		// #651: use the caller-supplied cloneURL (specific repo selected
		// by the operator in Stage 2 discovery tree) if non-empty;
		// otherwise fall back to the provider's generic RepoURL (which is
		// the provider homepage, e.g. "https://github.com", and is NOT a
		// valid git remote).
		effectiveCloneURL := cloneURL
		if effectiveCloneURL == "" {
			effectiveCloneURL = p.RepoURL
		}
		// Derive workspace dir from the effective URL so each repo gets
		// its own directory (provider ID is non-unique across repos from
		// the same provider token).
		dirKey := effectiveCloneURL
		if dirKey == "" {
			dirKey = p.ID
		}
		dir := filepath.Join(s.rt.StateDir(), "workspaces", sanitizeID(dirKey))
		if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
			_ = os.MkdirAll(dir, 0o700)
			cloneURL := effectiveCloneURL
			env := os.Environ()
			cleanupAsk := func() {}
			if p.Token != "" && strings.HasPrefix(cloneURL, "https://") {
				// Write helper script to a tempfile, env GIT_ASKPASS
				// points at it. Git uses the script for HTTP auth + the
				// token never lands on disk in .git/config.
				askScript, cleanup, err := writeAskpassHelper(p.Token)
				if err != nil {
					return dir, fmt.Errorf("askpass setup: %w", err)
				}
				cleanupAsk = cleanup
				env = append(env, "GIT_ASKPASS="+askScript, "GIT_TERMINAL_PROMPT=0")
				// Use a username placeholder; provider treats the password
				// (token) as the auth material.
				cloneURL = strings.Replace(cloneURL, "https://", "https://oauth2@", 1)
			}
			cmd := exec.Command("git", "clone", "--depth=1", cloneURL, dir)
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			cleanupAsk()
			if err != nil {
				return dir, fmt.Errorf("git clone failed: %w\n%s", err, out)
			}
			// Belt-and-braces: strip any URL credentials the clone might
			// have written to .git/config (older git versions store the
			// full URL even with askpass). Re-write origin to the bare URL.
			_ = exec.Command("git", "-C", dir, "remote", "set-url", "origin", effectiveCloneURL).Run()
		}
		return dir, nil
	}
	if fallbackCwd == "" {
		fallbackCwd, _ = os.UserHomeDir()
	}
	return fallbackCwd, fmt.Errorf("provider %q not registered", providerID)
}

// writeAskpassHelper creates a chmod-0700 shell script that prints token,
// returns its path + a cleanup func. Used as GIT_ASKPASS so the token
// never lands in .git/config (#138).
func writeAskpassHelper(token string) (string, func(), error) {
	f, err := os.CreateTemp("", "chepherd-askpass-*.sh")
	if err != nil {
		return "", func() {}, err
	}
	// Use printf %s to avoid shell-injection regardless of token content.
	if _, err := fmt.Fprintf(f, "#!/bin/sh\nprintf '%%s' %q\n", token); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", func() {}, err
	}
	if err := os.Chmod(f.Name(), 0o700); err != nil {
		os.Remove(f.Name())
		return "", func() {}, err
	}
	return f.Name(), func() { os.Remove(f.Name()) }, nil
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


// listSessionsMerged combines live runtime sessions with persisted
// session records from SessionStore so the dashboard sees the full
// available-session set. Each entry includes a `live` boolean —
// live==true means runtime has it in memory (attachable); live==false
// means it was persisted previously but isn't currently spawned (the
// dashboard should display "resumable", not auto-attach). #314 D4.
func (s *Server) listSessionsMerged(ctx context.Context) []map[string]any {
	var (
		out       []map[string]any
		liveNames = map[string]struct{}{}
	)
	if s.rt != nil {
		live := s.rt.List()
		out = make([]map[string]any, 0, len(live))
		for _, info := range live {
			liveNames[info.Name] = struct{}{}
			out = append(out, infoToMap(info, true))
		}
	}
	if s.SessionStore == nil {
		return out
	}
	ids, err := s.SessionStore.List(ctx)
	if err != nil {
		return out
	}
	for _, id := range ids {
		if _, alreadyLive := liveNames[id]; alreadyLive {
			continue
		}
		state, err := s.SessionStore.Get(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, map[string]any{
			"name":  id,
			"id":    id,
			"live":  false,
			"state": state,
		})
	}
	return out
}

// infoToMap returns a JSON-friendly representation of the full
// SessionInfo struct + a `live` boolean. Uses json.Marshal of the
// SessionInfo so every json-tagged field on the struct (pid, started,
// container_runtime, system_prompt, stat_sheet, agent_home_dir,
// github_url, branch, etc.) flows through to the dashboard.
//
// #356 P0: an earlier flattened-handcoded version of this helper
// stripped pid + started + container_runtime, causing the dashboard's
// WidgetAgentRuntime to display 'pid: —' and 'started: —' even when
// the spawn succeeded.
func infoToMap(info *runtime.SessionInfo, live bool) map[string]any {
	body, _ := json.Marshal(info)
	out := map[string]any{}
	_ = json.Unmarshal(body, &out)
	out["live"] = live
	return out
}

func (s *Server) sessionsRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"sessions": s.listSessionsMerged(r.Context())})
	case http.MethodPost:
		var req struct {
			Name, Agent, Team, Role, Cwd, SystemPrompt string
			ProviderID                                 string                 `json:"provider_id"`
			// CloneURL is the specific repo URL selected in Stage 2 (#651).
			CloneURL     string                 `json:"clone_url"`
			AgentArgs    []string               `json:"agent_args"`
			ResumeUUID   string                 `json:"resume_uuid"`
			UseDefaultPrompt bool               `json:"use_default_prompt"`
			StatSheet    runtime.AgentStatSheet `json:"stat_sheet"`
			ClaudeTokenID string                `json:"claude_token_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		cwd, err := s.resolveProviderCwd(req.ProviderID, req.Cwd, req.CloneURL)
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
				systemPrompt = prompts.ScrumMaster
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

// cleanupOrphans implements POST /api/v1/sessions/_cleanup-orphans
// (#393 P1, deferred from #377). Walks the SessionStore, deletes
// every row whose name is NOT in the live runtime registry, returns
// {deleted: N, kept: M}. Idempotent: running twice on the same state
// yields deleted=0 the second time.
//
// The operator unblock for #393 was a curl loop I gave them
// (workaround documented in #393 body). This endpoint is the
// programmatic equivalent + the backend for the dashboard's
// "Clean up orphans" header button.
//
// Live sessions (rt.Get returns non-nil) are PRESERVED unconditionally —
// the rt-Get gate guards against accidentally killing a session that
// happens to also have a persisted row (the canonical case is "every
// live session HAS a row").
func (s *Server) cleanupOrphans(w http.ResponseWriter, r *http.Request) {
	if s.SessionStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"deleted": 0, "kept": 0, "note": "no session store wired"})
		return
	}
	ctx := r.Context()
	ids, err := s.SessionStore.List(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "SessionStore.List: " + err.Error()})
		return
	}
	var deleted, kept int
	var deletedNames []string
	for _, name := range ids {
		var live bool
		if s.rt != nil {
			sess, _ := s.rt.Get(name)
			live = sess != nil
		}
		if live {
			kept++
			continue
		}
		if err := s.SessionStore.Delete(ctx, name); err != nil {
			continue // soft-fail per-row; report the others
		}
		deleted++
		deletedNames = append(deletedNames, name)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":       deleted,
		"kept":          kept,
		"deleted_names": deletedNames,
	})
}

func (s *Server) sessionByName(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	parts := strings.SplitN(path, "/", 2)
	name := parts[0]
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}
	// #393 P1 — bulk orphan cleanup. Walks the SessionStore, deletes
	// every row whose name has no live runtime entry, returns the
	// deleted count. Operator-driven via dashboard's "Clean up
	// orphans" header button; previously they had to click DELETE
	// N times or run a curl loop (#393 body documents the workaround
	// I gave them). Deferred bonus from #377.
	if name == "_cleanup-orphans" && sub == "" && r.Method == http.MethodPost {
		s.cleanupOrphans(w, r)
		return
	}

	var (
		sess *session.Session
		info *runtime.SessionInfo
	)
	if s.rt != nil {
		sess, info = s.rt.Get(name)
	}
	if sess == nil {
		// #357 P0: distinguish persisted-but-not-live (410 Gone, can
		// resume) from never-existed (404, definitive). The dashboard
		// uses the distinction to stop the 5s WebSocket attach loop on
		// stale-cache sessions + transition to a Resume UI.
		//
		// #377 P0: DELETE on a persisted-but-not-live row must clean
		// the store (idempotent garbage-collection), not return 410.
		// Pre-fix this branch short-circuited for ALL methods → the
		// DELETE handler at the switch below was unreachable for
		// orphan rows → operator's dashboard X button returned 410
		// and the row stayed forever. Operator accumulated 58 orphan
		// rows from chepherd restarts. Method-gate the cleanup:
		// DELETE → remove row + 200; everything else → 410.
		if s.SessionStore != nil {
			if state, err := s.SessionStore.Get(r.Context(), name); err == nil && len(state) > 0 {
				if sub == "" && r.Method == http.MethodDelete {
					_ = s.SessionStore.Delete(r.Context(), name)
					writeJSON(w, http.StatusOK, map[string]any{
						"ok":      true,
						"cleaned": true,
						"wasLive": false,
					})
					return
				}
				writeJSON(w, http.StatusGone, map[string]any{
					"error":     "session persisted but not running",
					"canResume": true,
					"name":      name,
					"live":      false,
					"state":     state,
				})
				return
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such session: " + name})
		return
	}

	switch {
	case sub == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, info)
	case sub == "agent-card" && r.Method == http.MethodGet:
		// #404 P0.1 — per-session AgentCard for peer-awareness. A spawned
		// agent can call chepherd.get_peer_card(peerName) which fetches
		// this endpoint to discover the peer's role, capabilities,
		// skills, current state, scorecard. Sibling of the chepherd-
		// instance-level /.well-known/agent-card.json (a2a discovery)
		// but scoped to ONE session inside the team.
		writeJSON(w, http.StatusOK, runtime.BuildPeerAgentCard(info))
	case sub == "peer-status" && r.Method == http.MethodGet:
		// #404 P0.2 — live activity status. Pulls runtime.sessionActivity
		// counters (total bytes, 5-minute window, idle seconds) + a
		// ring-buffer tail excerpt so peer agents answer "what is X
		// doing right now" without polling each other. Companion to
		// /agent-card (#404 P0.1, capabilities surface).
		writeJSON(w, http.StatusOK, s.rt.BuildPeerStatus(name))
	case sub == "" && r.Method == http.MethodDelete:
		_ = s.rt.Stop(name)
		// #377 P0 TRIGGER layer: also delete the persistence row so the
		// next chepherd restart doesn't auto-resume a stopped container
		// and re-create the orphan. Without this, every live DELETE
		// leaves a future orphan; that's how the operator accumulated
		// 58 of them. The Get-then-Delete branch above handles existing
		// orphans (CONTAINMENT); this prevents new ones (TRIGGER).
		if s.SessionStore != nil {
			_ = s.SessionStore.Delete(r.Context(), name)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleaned": true, "wasLive": true})
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
					"name":           m.Name,
					"agent":          m.Agent,
					"role":           string(m.Role),
					"prompt_preview": truncatePrompt(m.Prompt + m.BriefOverride),
					"cwd":            m.Cwd,
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
//
//	POST /api/v1/templates/{name}/fork {new_name}        — copy YAML to operator dir
//	PUT  /api/v1/templates/{name}                         — overwrite YAML in operator dir
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
		ProviderID      string `json:"provider_id"`
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
	cwd, _ := s.resolveProviderCwd(req.ProviderID, req.Cwd, "")
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
				sysPrompt = prompts.ScrumMaster
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
		// Guard against duplicate spawns: if a session with this name is
		// already live and healthy, skip re-spawning it — the operator hit
		// Launch twice or refreshed the page. If it exited, clear the record
		// so the spawn below doesn't fail with "already in use". (#647)
		if _, existingInfo := s.rt.Get(m.Name); existingInfo != nil {
			if !existingInfo.Exited {
				results = append(results, spawned{Name: m.Name, Role: string(m.Role)})
				continue
			}
			_ = s.rt.Stop(m.Name)
		}
		_, newSess, err := s.rt.Spawn(runtime.SpawnSpec{
			Name:          m.Name,
			AgentSlug:     firstNonEmpty(ov.Agent, m.Agent),
			Team:          team,
			Role:          role,
			Cwd:           memberCwd,
			SystemPrompt:  sysPrompt,
			StatSheet:     m.StatSheet,
			AgentArgs:     agentArgs,
			ClaudeTokenID: ov.ClaudeTokenID,
		})
		res := spawned{Name: m.Name, Role: string(m.Role)}
		if err != nil {
			res.Err = err.Error()
		} else {
			_, _ = s.rt.JoinTeam(m.Name, team, m.Role, m.BriefOverride)
			// Fire the first-run-prompts auto-dismisser for every
			// claude-code member — without this, template-spawned
			// agents sit on the "Yes, I trust this folder" prompt
			// forever (the per-session POST handler fires this for
			// solo spawns, but templateApply was missing it).
			agentSlug := firstNonEmpty(ov.Agent, m.Agent)
			if newSess != nil && (agentSlug == "" || agentSlug == "claude-code") {
				go autoDismissClaudeFirstRunPrompts(newSess)
			}
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
			"name":  m.Name,
			"agent": m.Agent,
			"role":  string(m.Role),
			"cwd": func() string {
				if m.Cwd != "" {
					return m.Cwd
				}
				return cwd
			}(),
			"claude_uuid": "",
		})
	}
	teamDir := filepath.Join(s.rt.StateDir(), "teams", team)
	_ = os.MkdirAll(teamDir, 0o700)
	apply := map[string]any{
		"template":    p.Name,
		"team":        team,
		"cwd":         cwd,
		"topology":    string(effectiveTopology),
		"members":     memberRecs,
		"last_active": time.Now().UTC().Format(time.RFC3339),
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
		Template string `json:"template"`
		Cwd      string `json:"cwd"`
		Topology string `json:"topology"`
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
			sysPrompt = prompts.ScrumMaster
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
		StatusLabel  string   `json:"status_label"`  // "" = backlog (no status label)
		RemoveLabels []string `json:"remove_labels"` // existing status labels to strip
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

// agentsCatalog surfaces the compiled-in + override agent registry so the
// spawn wizard can let operators pick an agent type (#127 R5 redo).
// Returns a slim view: slug + label + description + which credential
// types it expects, hiding internal Binary / DefaultArgs / RequiredEnv.
func (s *Server) agentsCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	type entry struct {
		Slug         string   `json:"slug"`
		Label        string   `json:"label"`
		Description  string   `json:"description"`
		RequiresVCS  bool     `json:"requires_vcs"`  // GitHub etc. tokens useful
		RequiresAuth string   `json:"requires_auth"` // "claude-oauth" | "openai-api" | "" (none)
		RequiredEnv  []string `json:"required_env,omitempty"`
	}
	labels := map[string]struct {
		Label, Description, Auth string
	}{
		"claude-code":     {"Claude Code", "Anthropic's official CLI — needs a Claude.ai subscription (Pro/Max/Team)", "claude-oauth"},
		"qwen-code":       {"Qwen Code", "Alibaba's open coding agent — needs an OpenAI-compatible endpoint key", "openai-api"},
		"aider":           {"Aider", "Open-source pair programmer — needs an OpenAI key", "openai-api"},
		"cursor-agent":    {"Cursor Agent", "Cursor's headless agent — needs the LLM gateway", ""},
		"little-coder":    {"Little Coder", "Minimal local agent — needs an OpenAI-compatible endpoint", "openai-api"},
		"opencode":        {"OpenCode", "Community-built coding agent — needs an OpenAI-compatible endpoint", "openai-api"},
		"sovereign-shell": {"Raw Shell", "Raw shell with no agent — useful as a rescue session", ""},
	}
	out := make([]entry, 0, len(agentcatalog.Builtin))
	for _, a := range agentcatalog.Builtin {
		meta := labels[a.Slug]
		out = append(out, entry{
			Slug:         a.Slug,
			Label:        firstNonEmpty(meta.Label, a.Slug),
			Description:  firstNonEmpty(meta.Description, a.Notes),
			RequiresAuth: meta.Auth,
			RequiresVCS:  meta.Auth == "claude-oauth" || meta.Auth == "openai-api",
			RequiredEnv:  a.RequiredEnv,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
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
	// #600 — claude-code prints TUI helper text "Paste code here if
	// prompted" immediately after the OAuth URL using ANSI cursor
	// positioning (no \n or \r separator). After ansiAndCRStrip
	// removes the cursor escape, the helper text concatenates
	// directly onto the URL (no whitespace, no stop char the regex
	// catches), corrupting the final query param (typically state=).
	// Result: PKCE flow validation fails when chepherd echoes back
	// 'state=<random>Paste...' vs stored '<random>'.
	return sanitizeOAuthCallbackURL(string(best))
}

// sanitizeOAuthCallbackURL parses the captured URL and trims each
// query param value at any TUI-helper-text suffix (e.g. claude-code's
// "Paste code here if prompted" string that got ANSI-cursor-glued onto
// the URL's tail end). Conservative: if URL parse fails, return input
// unchanged.
//
// Per #600.
func sanitizeOAuthCallbackURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	// Only sanitize base64url PKCE params (state, code_challenge).
	// Never touch scope, client_id, redirect_uri etc. — they are
	// human-readable strings that legitimately exceed 64 chars and
	// contain no TUI helper-text suffix. (#613)
	pkceParams := map[string]bool{"state": true, "code_challenge": true}
	q := u.Query()
	changed := false
	for k, vs := range q {
		if !pkceParams[k] {
			continue
		}
		for i, v := range vs {
			cleaned := trimTUIHelperSuffix(v)
			if cleaned != v {
				vs[i] = cleaned
				changed = true
			}
		}
		q[k] = vs
	}
	if !changed {
		return raw
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// trimTUIHelperSuffix removes a trailing TUI helper text suffix
// that's been concatenated onto a base64url value via ANSI cursor
// positioning. First tries known helper-text strings, then falls
// back to length-cap (PKCE state from 32 random bytes ≈ 43
// base64url chars; cap at 64 for headroom). For over-cap values,
// truncates at first CamelCase boundary (uppercase followed by 3+
// lowercase) — strong signal of human-readable text glued onto
// the base64url payload.
//
// Per #600.
func trimTUIHelperSuffix(v string) string {
	knownHelperSuffixes := []string{
		"Pastecodehereifprompted",
		"Pastethecodehere",
		"Paste", // very conservative — drops any trailing "Paste..." run
	}
	for _, suffix := range knownHelperSuffixes {
		if idx := strings.Index(v, suffix); idx > 0 {
			return v[:idx]
		}
	}
	// CamelCase boundary defense: scan from offset 32 (covers PKCE
	// 32-byte entropy ≈ 43 base64url chars) for an uppercase letter
	// followed by 3+ lowercase — a strong CamelCase signal of
	// human-readable TUI text glued onto the base64url payload.
	// Runs regardless of total length (so it catches concat
	// patterns even on shorter-than-cap values).
	if len(v) > 32+4 {
		for i := 32; i < len(v)-4; i++ {
			if v[i] >= 'A' && v[i] <= 'Z' &&
				v[i+1] >= 'a' && v[i+1] <= 'z' &&
				v[i+2] >= 'a' && v[i+2] <= 'z' &&
				v[i+3] >= 'a' && v[i+3] <= 'z' {
				return v[:i]
			}
		}
	}
	// Length-cap fallback (PKCE state from 32 random bytes ≈ 43
	// base64url chars; cap at 64 for any provider extension).
	const maxBase64URLValueLen = 64
	if len(v) > maxBase64URLValueLen {
		return v[:maxBase64URLValueLen]
	}
	return v
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

// claudeAutoDismissStep is one prompt-matcher entry. Extracted to
// package-level so the regression test (p0_411_*_test.go) can feed
// canned fixtures through the matcher without spinning up a PTY.
//
// #411 P0.
type claudeAutoDismissStep struct {
	marker string
	reply  []byte
	pause  time.Duration
}

// claudeAutoDismissSteps is the full canonical sequence chepherd's
// auto-dismiss watcher walks through. Order matters; we advance
// strictly forward (no step fires twice; step N+1 only after step N).
//
// Package-level so p0_411 test can assert the MCP-approval step's
// shape directly. #411 P0.
var claudeAutoDismissSteps = []claudeAutoDismissStep{
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
	//    via .mcp.json into every workspace.
	//
	//    #411 P0 — pre-fix matched option 2's label + bare "\r" reply
	//    selected option 1 ("Use this MCP server") which isn't
	//    permanent. Worse, in operator's repro the function exited on
	//    its 4s idle-tick timeout BEFORE the MCP prompt rendered
	//    (chepherd-net DNS + WS handshake → 5-8s on first boot). Agent
	//    wedged forever.
	//
	//    Fix: marker = HEADING text (fires as soon as prompt starts
	//    rendering), reply = "2\r" (deterministic option 2 — permanent
	//    across future spawns regardless of TUI default-highlight
	//    position).
	{marker: "NewMCPserverfoundinthisproject", reply: []byte("2\r"), pause: 2000 * time.Millisecond},
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

// claudeAutoDismissNormalize returns the form the matcher compares
// against — same scrubbing the live loop applies. Extracted for the
// #411 regression test so fixtures + the matcher use the same
// normalization rules.
func claudeAutoDismissNormalize(raw []byte) string {
	clean := ansiAndCRStrip.ReplaceAll(raw, nil)
	text := strings.ReplaceAll(strings.ReplaceAll(string(clean), "\r", ""), "\n", "")
	return strings.ReplaceAll(text, " ", "")
}

// claudeAutoDismissFirstUnfired returns the index of the first step
// whose marker is present in `tail` AND hasn't already fired, or -1
// if none match.
func claudeAutoDismissFirstUnfired(tail string, fired []bool) int {
	for i, st := range claudeAutoDismissSteps {
		if fired[i] {
			continue
		}
		if strings.Contains(tail, st.marker) {
			return i
		}
	}
	return -1
}

// autoDismissClaudeFirstRunPrompts watches sess's ring buffer for the
// first-run prompts claude-code prints + injects the right reply per
// prompt so the OAuth URL surfaces without operator interaction.
// Returns once the canonical Claude OAuth URL is detected or after a
// timeout — the polling URL endpoint handles the rest.
func autoDismissClaudeFirstRunPrompts(sess *session.Session) {
	// #411 P0 — steps moved to package-level claudeAutoDismissSteps so
	// the regression test in p0_411_*_test.go can exercise the matcher
	// without spinning up a real PTY.
	// claude-code's TUI renders each word via cursor-position escape
	// codes (e.g. "\x1b[9G text"), not real spaces, so stripping ANSI
	// from the ring buffer leaves words concatenated. Markers below are
	// the SPACE-LESS form ("ChoosethetextstylethatlooksbestwithyourTerminal"
	// etc.) — they must match the post-strip text.
	steps := claudeAutoDismissSteps

	// Only look at the TAIL of the cleaned buffer — the cumulative buffer
	// contains every screen claude-code has ever drawn, including the
	// option labels of already-dismissed prompts. If we matched against
	// the whole buffer, e.g. "Yes,Itrustthisfolder" (the option label of
	// the trust prompt) would still appear minutes after it was
	// dismissed, and step 6 ("BypassPermissionsmode" → "2\r") could fire
	// during the trust screen with disastrous side effects (selecting
	// "No, exit" on the trust prompt, killing the container).
	const tailWindow = 2048
	// #411 P0 — bumped deadline (60s → 180s) and idle tolerance (8 ticks
	// → 24 ticks = 12s) so the MCP-server-approval prompt has time to
	// render after trust-folder dismiss. MCP server init involves DNS
	// resolution on chepherd-net + WS handshake to ws://chepherd:9090,
	// which can take 5-8s on first boot. Pre-#411 the 4s idle window
	// fired BEFORE the prompt rendered and the function returned,
	// leaving the agent wedged forever.
	fired := make([]bool, len(steps))
	deadline := time.Now().Add(180 * time.Second)
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
				fmt.Fprintf(os.Stderr, "[chepherd-auto-dismiss] step %d (bypass): sent Down+Enter\n", fireIdx)
			} else {
				_, _ = sess.Inject(reply)
				fmt.Fprintf(os.Stderr, "[chepherd-auto-dismiss] step %d marker %q matched → sent %q\n",
					fireIdx, steps[fireIdx].marker, string(reply))
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
		// #411 P0 — 24 idle ticks * 500ms = 12s with no marker hit + no
		// OAuth URL → agent has reached steady state. Pre-#411 the
		// tolerance was 8 ticks (4s) which was too aggressive: MCP
		// server init (DNS + WS handshake to ws://chepherd:9090) can
		// take 5-8s on first boot, so the function exited BEFORE the
		// MCP approval prompt rendered + the agent wedged.
		if idleTicks > 24 {
			fmt.Fprintf(os.Stderr, "[chepherd-auto-dismiss] reached steady state (24 idle ticks); exiting\n")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "[chepherd-auto-dismiss] DEADLINE reached (180s) — agent may be wedged on an unmatched prompt\n")
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
	containerName := agentContainerName(s.rt.InstanceUUID(), name)
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

// agentContainerName returns the podman/docker container name for an agent
// session. When uuid is non-empty (#270 instance-UUID scoping) the name
// is "chepherd-agent-<uuid>-<sessionName>"; otherwise "chepherd-agent-<sessionName>".
// Extracted from claudeLoginSubmit so it can be unit-tested independently.
func agentContainerName(uuid, sessionName string) string {
	prefix := "chepherd-agent-"
	if uuid != "" {
		prefix += uuid + "-"
	}
	return prefix + sessionName
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

// validateProviderToken makes a single GET against the provider's
// `/user` endpoint (each provider has one) with the operator-supplied
// token. Returns nil on 2xx, error otherwise. Used by the
// POST /api/v1/git-providers handler to reject bad tokens BEFORE they
// land in state — eliminates the "stale provider" UX category by
// preventing it from existing.
//
// Network errors (DNS, timeout, connection refused) are NOT treated as
// validation failures — the operator may be behind a flaky network at
// registration time and we don't want to lose a real working token to
// transient noise. Only HTTP-level rejection (401, 403, 4xx) counts.
func validateProviderToken(kind runtime.GitProviderKind, repoURL, token string) error {
	type probe struct {
		url      string
		authHdr  string
		tokenFmt string // "Bearer %s" or "token %s"
	}
	apiBase := func(host string) string {
		// Strip path/trailing slash from instance URL.
		host = strings.TrimRight(host, "/")
		return host
	}
	var p probe
	switch kind {
	case "github":
		p = probe{url: "https://api.github.com/user", authHdr: "Authorization", tokenFmt: "Bearer %s"}
		// GHES self-hosted instances use <instance>/api/v3/user.
		if host := apiBase(repoURL); host != "" && host != "https://github.com" {
			p.url = host + "/api/v3/user"
		}
	case "gitlab":
		host := apiBase(repoURL)
		if host == "" {
			host = "https://gitlab.com"
		}
		p = probe{url: host + "/api/v4/user", authHdr: "Authorization", tokenFmt: "Bearer %s"}
	case "bitbucket":
		p = probe{url: "https://api.bitbucket.org/2.0/user", authHdr: "Authorization", tokenFmt: "Bearer %s"}
	case "gitea":
		host := apiBase(repoURL)
		if host == "" {
			return fmt.Errorf("gitea requires instance URL")
		}
		p = probe{url: host + "/api/v1/user", authHdr: "Authorization", tokenFmt: "token %s"}
	default:
		// Unknown kind — let it through; the save layer will error out
		// on schema validation.
		return nil
	}

	req, err := http.NewRequest(http.MethodGet, p.url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set(p.authHdr, fmt.Sprintf(p.tokenFmt, token))
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Network error — not a validation failure. Let the save
		// proceed; the operator can retry discovery later.
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// 401/403 = token rejected; surface a readable message.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	bodyStr := strings.TrimSpace(string(body))
	if bodyStr == "" {
		bodyStr = "HTTP " + resp.Status
	}
	return fmt.Errorf("%s on %s — %s", resp.Status, p.url, bodyStr)
}

// authProviderValidator adapts auth.AuthProvider to a2a.TokenValidator.
// #225 row B1 — bridges the runtimehttp Server's existing auth provider
// (HS256 dashboard JWT today; ES256 / OAuth2 / mTLS layered later) to
// the A2A AuthMiddleware's minimal seam without internal/a2a importing
// internal/auth (cycle-breaker).
type authProviderValidator struct {
	provider auth.AuthProvider
}

func (v *authProviderValidator) Validate(ctx context.Context, token string) (string, error) {
	id, err := v.provider.Validate(ctx, token)
	if err != nil {
		return "", err
	}
	if id == nil {
		return "", fmt.Errorf("auth: validator returned nil identity")
	}
	return id.Subject, nil
}

// peersList implements GET /api/v1/peers — returns the cached peer
// agent-cards persisted by the federation orchestrator (#225 row C1).
// Always returns an array (possibly empty); never 503 when federation
// is disabled — operator sees an empty list + UI can prompt them to
// configure --federation-registry-url.
func (s *Server) peersList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.AgentCardStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"peers": []any{}})
		return
	}
	cards, err := s.AgentCardStore.List(r.Context(), persistence.AgentCardListOpts{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	type peerView struct {
		SID      string          `json:"sid"`
		Name     string          `json:"name"`
		Card     json.RawMessage `json:"card"`
		SyncedAt time.Time       `json:"syncedAt"`
	}
	out := make([]peerView, 0, len(cards))
	for _, c := range cards {
		out = append(out, peerView{
			SID:      c.SID,
			Name:     c.Name,
			Card:     json.RawMessage(c.Body),
			SyncedAt: c.SyncedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": out})
}


// webrtcICE accepts ICE candidates trickled from the offering peer.
// v0.9.3 scaffold: parses the candidate + returns 200; full plumbing
// requires session-keyed PeerConnection routing (#311 C5.2 follow-up
// — track open peer connections by session-id from the offer's SDP).
//
// Refs #311 (C5.1) #311 (C5).
func (s *Server) webrtcICE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Parse the body just to validate it's a candidate envelope; the
	// candidate trickle is best-effort + the negotiation is driven by
	// the answerer's own ICE gathering at this layer.
	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()
	if len(body) == 0 {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// tasksList implements GET /api/v1/tasks — returns recent A2A tasks
// from the TaskRepository for the dashboard's A2A Inbox tab (#225
// row G2). Returns the most recent 50 tasks. Always an array
// (possibly empty); never 503 when no TaskStore is wired.
//
// Refs #225 row G2.
func (s *Server) tasksList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.TaskStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"tasks": []any{}})
		return
	}
	tasks, err := s.TaskStore.List(r.Context(), persistence.TaskListOpts{Limit: 50})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	type taskView struct {
		ID        string    `json:"id"`
		RunnerSID string    `json:"runnerSID"`
		State     string    `json:"state"`
		Method    string    `json:"method"`
		CreatedAt time.Time `json:"createdAt"`
		UpdatedAt time.Time `json:"updatedAt"`
	}
	out := make([]taskView, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, taskView{
			ID:        task.ID,
			RunnerSID: task.RunnerSID,
			State:     task.State,
			Method:    task.Method,
			CreatedAt: task.CreatedAt,
			UpdatedAt: task.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out})
}
