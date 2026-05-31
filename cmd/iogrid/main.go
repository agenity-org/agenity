// Package main is the iogrid HTTP API binary entrypoint (#500
// Wave H2). Wraps chepherd-runner --headless (Wave H1 #499) with
// an HTTP frontend so batch consumers can POST tasks, poll for
// completion, fetch the A2A Task envelope, and DELETE to cancel —
// all without forking the runner binary themselves.
//
// Wire shape (per V0.9.2-ARCHITECTURE.md §11 + §5 #49):
//
//	POST   /v1/runners               { ...A2A SendMessage params... }
//	                                   → 202 Accepted + {"id":"<runner-id>"}
//	GET    /v1/runners/{id}          → {"id","state","exit_code","created_at",
//	                                     "completed_at"}
//	                                   state ∈ {running, completed, failed, canceled}
//	GET    /v1/runners/{id}/result   → A2A v1.0 Task envelope JSON
//	                                   (404 until state is terminal)
//	DELETE /v1/runners/{id}          → 204 No Content + SIGTERM→SIGKILL
//	GET    /healthz                  → 200 ok
//
// Auth: when --auth-token is set, every /v1/* path requires
// `Authorization: Bearer <token>`. Empty disables (dev/test
// default). H3 #501 wires JWT validation against daemon JWKS;
// for H2 the static-token check is the minimum gate.
//
// Refs #500 V0.9.2-ARCHITECTURE.md §11 §5 #49.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "iogrid: %v\n", err)
		os.Exit(1)
	}
}

type config struct {
	listen        string
	runnerBin     string
	authToken     string
	taskTimeout   time.Duration
	stateDir      string
}

func run() error {
	cfg := config{}
	flag.StringVar(&cfg.listen, "listen", envOr("IOGRID_LISTEN", "127.0.0.1:8089"),
		"HTTP bind address (host:port).")
	flag.StringVar(&cfg.runnerBin, "runner-bin", envOr("IOGRID_RUNNER_BIN", "chepherd-runner"),
		"path to the chepherd-runner binary that --headless invocations exec.")
	flag.StringVar(&cfg.authToken, "auth-token", envOr("IOGRID_AUTH_TOKEN", ""),
		"shared-secret bearer token required on /v1/*. Empty disables auth (dev only).")
	flag.DurationVar(&cfg.taskTimeout, "task-timeout", 5*time.Minute,
		"wall-clock cap forwarded to each child runner's --task-timeout flag.")
	flag.StringVar(&cfg.stateDir, "state-dir", envOr("IOGRID_STATE_DIR", ""),
		"per-runner working dir prefix. Empty uses os.TempDir().")
	flag.Parse()

	if cfg.stateDir == "" {
		cfg.stateDir = os.TempDir()
	}
	if err := os.MkdirAll(cfg.stateDir, 0o700); err != nil {
		return fmt.Errorf("mkdir state-dir %q: %w", cfg.stateDir, err)
	}

	srv := newServer(&cfg)
	httpSrv := &http.Server{
		Addr:              cfg.listen,
		Handler:           srv.mux(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = httpSrv.ListenAndServe()
	}()
	fmt.Printf("✓ iogrid listening on http://%s (runner-bin=%s, auth=%v)\n",
		cfg.listen, cfg.runnerBin, cfg.authToken != "")

	// Graceful shutdown on SIGINT/SIGTERM — kill outstanding
	// children + close the listener.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	fmt.Println("iogrid: shutting down")
	srv.killAll()
	_ = httpSrv.Close()
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// runnerInfo holds the per-child-runner state.
type runnerInfo struct {
	ID          string    `json:"id"`
	State       string    `json:"state"`
	ExitCode    *int      `json:"exit_code,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`

	// Internal — not serialized.
	cmd        *exec.Cmd      `json:"-"`
	resultFile string         `json:"-"`
	workDir    string         `json:"-"`
	done       chan struct{}  `json:"-"`
	taskBody   []byte         `json:"-"`
}

const (
	stateRunning   = "running"
	stateCompleted = "completed"
	stateFailed    = "failed"
	stateCanceled  = "canceled"
)

type server struct {
	cfg     *config
	mu      sync.Mutex
	runners map[string]*runnerInfo
}

func newServer(cfg *config) *server {
	return &server{
		cfg:     cfg,
		runners: map[string]*runnerInfo{},
	}
}

func (s *server) mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/runners", s.requireAuth(s.handleRunnersRoot))
	mux.HandleFunc("/v1/runners/", s.requireAuth(s.handleRunnerByID))
	return mux
}

func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.authToken != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got == "" || got != s.cfg.authToken {
				writeJSONError(w, http.StatusUnauthorized, "authentication required")
				return
			}
		}
		next(w, r)
	}
}

func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"ok":true}`+"\n")
}

func (s *server) handleRunnersRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if len(body) == 0 {
		writeJSONError(w, http.StatusBadRequest, "task body empty")
		return
	}
	info, err := s.spawnRunner(body)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "spawn: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"id": info.ID})
}

// handleRunnerByID dispatches /v1/runners/{id} + /v1/runners/{id}/result.
func (s *server) handleRunnerByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/runners/")
	if rest == "" {
		writeJSONError(w, http.StatusBadRequest, "runner id required")
		return
	}
	id := rest
	suffix := ""
	if i := strings.Index(rest, "/"); i >= 0 {
		id = rest[:i]
		suffix = rest[i:]
	}
	switch suffix {
	case "":
		s.handleRunnerState(w, r, id)
	case "/result":
		s.handleRunnerResult(w, r, id)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown subpath: "+suffix)
	}
}

func (s *server) handleRunnerState(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		info, ok := s.runners[id]
		s.mu.Unlock()
		if !ok {
			writeJSONError(w, http.StatusNotFound, "runner not found")
			return
		}
		// Snapshot a serializable copy so the lock isn't held across encode.
		s.mu.Lock()
		snap := *info
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	case http.MethodDelete:
		s.cancelRunner(id)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "GET or DELETE")
	}
}

func (s *server) handleRunnerResult(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	s.mu.Lock()
	info, ok := s.runners[id]
	s.mu.Unlock()
	if !ok {
		writeJSONError(w, http.StatusNotFound, "runner not found")
		return
	}
	if info.State == stateRunning {
		writeJSONError(w, http.StatusConflict, "runner still running")
		return
	}
	body, err := os.ReadFile(info.resultFile)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "read result: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// spawnRunner forks chepherd-runner --headless + adds the result-
// pollable runnerInfo to the registry. Returns immediately;
// completion is observed asynchronously by trackRunner.
func (s *server) spawnRunner(taskBody []byte) (*runnerInfo, error) {
	id := uuid.NewString()
	workDir, err := os.MkdirTemp(s.cfg.stateDir, "iogrid-"+id+"-")
	if err != nil {
		return nil, fmt.Errorf("workdir: %w", err)
	}
	resultFile := filepath.Join(workDir, "result.json")
	cmd := exec.Command(s.cfg.runnerBin,
		"--headless",
		"--task-json", string(taskBody),
		"--result-file", resultFile,
		"--task-timeout", s.cfg.taskTimeout.String(),
	)
	// Capture child stderr into the workdir so failed runners
	// can be diagnosed; stdout is discarded because --result-file
	// owns the canonical task envelope.
	cmd.Stdout = io.Discard
	if errFile, err := os.Create(filepath.Join(workDir, "stderr.log")); err == nil {
		cmd.Stderr = errFile
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("start runner: %w", err)
	}
	info := &runnerInfo{
		ID:         id,
		State:      stateRunning,
		CreatedAt:  time.Now().UTC(),
		cmd:        cmd,
		resultFile: resultFile,
		workDir:    workDir,
		done:       make(chan struct{}),
		taskBody:   append([]byte(nil), taskBody...),
	}
	s.mu.Lock()
	s.runners[id] = info
	s.mu.Unlock()
	go s.trackRunner(info)
	return info, nil
}

// trackRunner waits for the child process + updates state +
// signals done. Called on a goroutine per spawn.
func (s *server) trackRunner(info *runnerInfo) {
	err := info.cmd.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	info.CompletedAt = time.Now().UTC()
	if info.State == stateCanceled {
		close(info.done)
		return
	}
	if err == nil {
		info.State = stateCompleted
		zero := 0
		info.ExitCode = &zero
	} else {
		info.State = stateFailed
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		info.ExitCode = &code
	}
	close(info.done)
}

// cancelRunner sends SIGTERM then SIGKILL after a grace window.
// State transitions to CANCELED if the process was still running.
func (s *server) cancelRunner(id string) {
	s.mu.Lock()
	info, ok := s.runners[id]
	if !ok || info.State != stateRunning {
		s.mu.Unlock()
		return
	}
	info.State = stateCanceled
	pid := -info.cmd.Process.Pid
	s.mu.Unlock()
	_ = syscall.Kill(pid, syscall.SIGTERM)
	select {
	case <-info.done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(pid, syscall.SIGKILL)
		<-info.done
	}
}

// killAll terminates every still-running child. Called on
// iogrid-server shutdown.
func (s *server) killAll() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.runners))
	for id, info := range s.runners {
		if info.State == stateRunning {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.cancelRunner(id)
	}
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
