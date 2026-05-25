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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chepherd/chepherd/internal/catalog"
	"github.com/chepherd/chepherd/internal/prompts"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
	"github.com/chepherd/chepherd/internal/runtime"
)

// Server hosts chepherd runtime endpoints. Caller is responsible for
// listening on a port + calling http.Serve(listener, server.Handler()).
type Server struct {
	rt *runtime.Runtime

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

	return logMiddleware(mux)
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
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"sessions": len(s.rt.List()),
		"ts":       time.Now().UTC(),
	})
}

func (s *Server) sessionsRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"sessions": s.rt.List()})
	case http.MethodPost:
		var req struct {
			Name, Agent, Team, Role, Cwd, SystemPrompt string
			AgentArgs                                   []string `json:"agent_args"`
			ResumeUUID                                  string   `json:"resume_uuid"`
			UseDefaultPrompt                            bool     `json:"use_default_prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		info, _, err := s.rt.Spawn(runtime.SpawnSpec{
			Name:         req.Name,
			AgentSlug:    req.Agent,
			Team:         req.Team,
			Role:         role,
			Cwd:          req.Cwd,
			SystemPrompt: systemPrompt,
			AgentArgs:    args,
		})
		if err == nil && req.Team != "" {
			// v0.6: also record the membership in the unified model.
			// Best-effort; failure here doesn't block the spawn since
			// v0.5 SessionInfo.Team is still the source of truth in
			// transitional code.
			_, _ = s.rt.JoinTeam(req.Name, req.Team, runtime.MembershipRole(req.Role), "")
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
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
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/teams/")
	if name == "" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		// Not yet implemented — leaving as 405 for now
		http.Error(w, "team deletion not implemented", http.StatusMethodNotAllowed)
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

// templatesHandler — list available TeamProfile templates from the
// catalog dir (./catalog at the repo root in dev; ~/.local/state/chepherd-v06/catalog
// in production installs).
func (s *Server) templatesHandler(w http.ResponseWriter, r *http.Request) {
	dirs := []string{
		"./catalog",
		filepath.Join(os.Getenv("HOME"), ".local/state/chepherd-v06/catalog"),
		filepath.Join(s.rt.StateDir(), "..", "..", "..", "repos", "chepherd", "catalog"), // fallback to repo
	}
	seen := map[string]bool{}
	var out []map[string]any
	for _, d := range dirs {
		ps, _ := catalog.LoadAll(d)
		for _, p := range ps {
			if seen[p.Name] {
				continue
			}
			seen[p.Name] = true
			out = append(out, map[string]any{
				"name":        p.Name,
				"description": p.Description,
				"topology":    p.Topology,
				"members":     len(p.Members),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}

// templateApply — POST /api/v1/templates/{name}/apply {team, cwd}
func (s *Server) templateApply(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/templates/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[1] != "apply" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	templateName := parts[0]
	var req struct{ Team, Cwd string }
	_ = json.NewDecoder(r.Body).Decode(&req)
	// Find the template
	dirs := []string{
		"./catalog",
		filepath.Join(os.Getenv("HOME"), ".local/state/chepherd-v06/catalog"),
	}
	var p *catalog.TeamProfile
	for _, d := range dirs {
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
	cwd := req.Cwd
	if cwd == "" {
		cwd = os.Getenv("HOME")
	}
	// Spawn each member; auto-join to team
	_, _ = s.rt.CreateTeam(team, "", p.Topology)
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
		_, newSess, err := s.rt.Spawn(runtime.SpawnSpec{
			Name:      m.Name,
			AgentSlug: m.Agent,
			Team:      team,
			Role:      role,
			Cwd:       cwd,
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
	s.rt.RecordEvent(runtime.Event{
		Kind: "template_applied", Actor: "runtime",
		Body: fmt.Sprintf("template %q applied as team %q (%d members)", p.Name, team, len(p.Members)),
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"template": p.Name, "team": team, "members": results,
	})
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

func logMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		_ = fmt.Sprintf // placeholder for future structured logging
		_ = start
	})
}
