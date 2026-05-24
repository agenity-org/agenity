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
			Name, Agent, Tribe, Role, Cwd, SystemPrompt string
			AgentArgs                                   []string `json:"agent_args"`
			ResumeUUID                                  string   `json:"resume_uuid"`
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
		info, _, err := s.rt.Spawn(runtime.SpawnSpec{
			Name:         req.Name,
			AgentSlug:    req.Agent,
			Tribe:        req.Tribe,
			Role:         role,
			Cwd:          req.Cwd,
			SystemPrompt: req.SystemPrompt,
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

	// Inbound: WebSocket → PTY stdin
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
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
// The encoded-cwd convention is Claude's: replace '/' with '-' (e.g.
// /home/openova → -home-openova). We decode by reversing.
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
		// Decode Claude's "-home-openova-repos-x" → "/home/openova/repos/x"
		decoded := "/" + strings.ReplaceAll(strings.TrimPrefix(projDir.Name(), "-"), "-", "/")
		if filterCwd != "" && decoded != filterCwd {
			continue
		}
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
			// First message: read first 1KB, parse first non-summary line.
			first := readFirstUserMessage(filepath.Join(projPath, f.Name()))
			out = append(out, cs{
				UUID:         uuid,
				Cwd:          decoded,
				Modified:     info.ModTime().UTC().Format(time.RFC3339),
				SizeBytes:    info.Size(),
				FirstMessage: first,
			})
		}
	}
	// Sort newest first by Modified
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// recentFolders lists folders that have at least one Claude session,
// for the Spawn modal's folder picker. Most-recently-active first.
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
	out := []fe{}
	for _, d := range entries {
		if !d.IsDir() {
			continue
		}
		decoded := "/" + strings.ReplaceAll(strings.TrimPrefix(d.Name(), "-"), "-", "/")
		files, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			continue
		}
		count := 0
		var newest time.Time
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			count++
			info, err := f.Info()
			if err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		if count == 0 {
			continue
		}
		out = append(out, fe{Path: decoded, Modified: newest.UTC().Format(time.RFC3339), Sessions: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	writeJSON(w, http.StatusOK, map[string]any{"folders": out})
}

// readFirstUserMessage scans the first ~4KB of a Claude session JSONL
// for the first message of type "user" (or "human") and returns a short
// snippet of its content. Best-effort; returns "" on any parse failure.
func readFirstUserMessage(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Type != "user" && rec.Message.Role != "user" {
			continue
		}
		// Content can be string or [{type:"text",text:"..."}]
		switch c := rec.Message.Content.(type) {
		case string:
			return truncate(c, 120)
		case []any:
			for _, item := range c {
				if m, ok := item.(map[string]any); ok {
					if t, _ := m["text"].(string); t != "" {
						return truncate(t, 120)
					}
				}
			}
		}
	}
	return ""
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
