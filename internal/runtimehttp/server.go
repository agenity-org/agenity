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
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

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
