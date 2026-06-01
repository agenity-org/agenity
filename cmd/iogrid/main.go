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
	"context"
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

	// #503 Wave H5 — start the AUTH_REQUIRED timeout sweeper.
	authCtx, cancelAuth := context.WithCancel(context.Background())
	go srv.runAuthTimeoutLoop(authCtx)

	// Graceful shutdown on SIGINT/SIGTERM — kill outstanding
	// children + close the listener.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	fmt.Println("iogrid: shutting down")
	cancelAuth()
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
	stateRunning      = "running"
	stateCompleted    = "completed"
	stateFailed       = "failed"
	stateCanceled     = "canceled"
	stateAuthRequired = "auth-required" // #503 Wave H5 — agent emitted OAuth challenge; awaiting credentials inject.
)

// headlessAuthRequiredExitCode is the runner exit code that signals
// "task exited cleanly but ended in AUTH_REQUIRED state" — iogrid
// translates this into the auth-required runner state per #503.
const headlessAuthRequiredExitCode = 4

type server struct {
	cfg     *config
	mu      sync.Mutex
	runners map[string]*runnerInfo
	recipes *recipeStore // #502 Wave H4 — task-recipe catalog
}

func newServer(cfg *config) *server {
	return &server{
		cfg:     cfg,
		runners: map[string]*runnerInfo{},
		recipes: newRecipeStore(),
	}
}

func (s *server) mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/runners", s.requireAuth(s.handleRunnersRoot))
	mux.HandleFunc("/v1/runners/", s.requireAuth(s.handleRunnersDispatch))
	// #502 Wave H4 — recipe surface.
	mux.HandleFunc("/api/v1/recipes", s.requireAuth(s.handleRecipesRoot))
	mux.HandleFunc("/api/v1/recipes/", s.requireAuth(s.handleRecipeByName))
	// Public virtual Agent Card per A2A discovery convention.
	mux.HandleFunc("/a2a/recipe/", s.handleVirtualAgentCard)
	return mux
}

// authRequiredTimeout caps how long a runner can sit in AUTH_REQUIRED
// before iogrid transitions it to FAILED("oauth-timeout"). Operator
// has this much wall-clock time to complete the out-of-band OAuth
// flow + POST credentials/inject. Per #503 Wave H5 / §15.3 dispatch.
const authRequiredTimeout = 10 * time.Minute

// handleRunnersDispatch routes /v1/runners/{id}, /v1/runners/{id}/result,
// AND /v1/runners/recipe/{name} (which H4 added). The recipe path is
// dispatched here to keep the mux pattern count minimal.
func (s *server) handleRunnersDispatch(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/runners/")
	if strings.HasPrefix(rest, "recipe/") {
		s.handleRecipeExecution(w, r)
		return
	}
	s.handleRunnerByID(w, r)
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
	case "/credentials/inject":
		s.handleCredentialsInject(w, r, id)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown subpath: "+suffix)
	}
}

// handleCredentialsInject resumes an AUTH_REQUIRED runner by
// re-spawning a fresh task with the supplied credentials merged
// into the original task body. The original runner ID stays
// attached to the new headless invocation (state transitions
// AUTH_REQUIRED → RUNNING → COMPLETED|FAILED|AUTH_REQUIRED again).
//
// Request body: {"credentials":[{"provider":"<slug>","key":"<token>"}]}
//
// Per §15.3 / #503 Wave H5: this is the resume seam — the operator
// completes the OAuth flow out-of-band (browser), then hands the
// resulting token to iogrid via this endpoint. Headless mode has
// no in-process resume (the agent terminated after AUTH_REQUIRED
// emission); iogrid spawns a NEW chepherd-runner child with the
// same task body + new credentials, and the new child finds the
// MCP server authenticated this time.
func (s *server) handleCredentialsInject(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	s.mu.Lock()
	info, ok := s.runners[id]
	s.mu.Unlock()
	if !ok {
		writeJSONError(w, http.StatusNotFound, "runner not found")
		return
	}
	if info.State != stateAuthRequired {
		writeJSONError(w, http.StatusConflict,
			"runner state="+info.State+", inject only valid in auth-required")
		return
	}
	injectBody, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var inject struct {
		Credentials []map[string]string `json:"credentials"`
	}
	if err := json.Unmarshal(injectBody, &inject); err != nil {
		writeJSONError(w, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	if len(inject.Credentials) == 0 {
		writeJSONError(w, http.StatusBadRequest, "credentials array required")
		return
	}
	// Merge credentials into the original task body. The headless
	// runner already understands the {credentials:[...]} field
	// (Wave H3 substrate).
	var orig map[string]any
	if err := json.Unmarshal(info.taskBody, &orig); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "decode original task: "+err.Error())
		return
	}
	merged := make([]any, 0, len(inject.Credentials))
	for _, c := range inject.Credentials {
		merged = append(merged, c)
	}
	orig["credentials"] = merged
	resumedBody, _ := json.Marshal(orig)

	// Spawn a fresh runner under a NEW id. The original id stays in
	// the registry pinned at auth-required so the result envelope
	// remains addressable; the new runner is the resumption.
	newInfo, err := s.spawnRunner(resumedBody)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "respawn: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"resumed_from": id,
		"id":           newInfo.ID,
	})
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
	// #503 Wave H5 — auth-required, completed, and failed states all
	// return the on-disk result file; the envelope's Status.State +
	// Status.Details is what differentiates them on the wire.
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
	// #501 Wave H3 — strip credentials from the task body before
	// forwarding to the runner. The runner gets the task via
	// --task-json (visible in /proc/<runner-pid>/cmdline); the
	// credentials NEVER appear there. They flow via
	// --credentials-file pointing at a 0600 file the runner reads
	// + deletes.
	taskBodyForRunner, credsFile, credSummary, cerr := extractCredentialsFromTaskBody(taskBody, workDir)
	if cerr != nil {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("credentials: %w", cerr)
	}
	args := []string{
		"--headless",
		"--task-json", string(taskBodyForRunner),
		"--result-file", resultFile,
		"--task-timeout", s.cfg.taskTimeout.String(),
	}
	if credsFile != "" {
		args = append(args, "--credentials-file", credsFile)
		fmt.Printf("iogrid: injecting credentials for task %s: %s\n", id, credSummary)
	}
	cmd := exec.Command(s.cfg.runnerBin, args...)
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
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		info.ExitCode = &code
		// #503 Wave H5 — exit 4 from --headless means the agent
		// emitted an OAuth challenge; the result file carries the
		// auth-required Task envelope. Map to AUTH_REQUIRED so
		// pollers + the inject endpoint can resume the task.
		if code == headlessAuthRequiredExitCode {
			info.State = stateAuthRequired
		} else {
			info.State = stateFailed
		}
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

// authTimeoutSweep transitions AUTH_REQUIRED runners that have sat
// past authRequiredTimeout to FAILED with reason oauth-timeout.
// Single-shot; the caller (server.runAuthTimeoutLoop) ticks.
//
// Refs #503 Wave H5 / §15.3.
func (s *server) authTimeoutSweep() {
	cutoff := time.Now().Add(-authRequiredTimeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, info := range s.runners {
		if info.State != stateAuthRequired {
			continue
		}
		if info.CompletedAt.IsZero() || info.CompletedAt.After(cutoff) {
			continue
		}
		info.State = stateFailed
		// Overwrite the result file's Status.State so consumers
		// fetching /result see the timeout terminal state rather
		// than the stale auth-required envelope.
		_ = rewriteResultStateToFailed(info.resultFile, "oauth-timeout")
	}
}

// runAuthTimeoutLoop is the long-lived ticker that calls
// authTimeoutSweep every 30s. Returns when ctx is canceled.
func (s *server) runAuthTimeoutLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.authTimeoutSweep()
		}
	}
}

// rewriteResultStateToFailed mutates the on-disk Task envelope so
// Status.State = "failed" + Status.Message carries `reason`. Used
// by the auth-required timeout sweep to keep the on-disk result
// in sync with the in-memory state transition.
func rewriteResultStateToFailed(path, reason string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	status, _ := envelope["status"].(map[string]any)
	if status == nil {
		status = map[string]any{}
	}
	status["state"] = "TASK_STATE_FAILED"
	status["message"] = map[string]any{
		"role": "agent",
		"kind": "message",
		"parts": []map[string]any{
			{"kind": "text", "text": "AUTH_REQUIRED timed out: " + reason},
		},
	}
	envelope["status"] = status
	out, _ := json.MarshalIndent(envelope, "", "  ")
	out = append(out, '\n')
	return os.WriteFile(path, out, 0o600)
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

// extractCredentialsFromTaskBody pulls a top-level "credentials"
// field out of the iogrid POST body and writes it to a 0600 file
// in workDir for the runner to read via --credentials-file
// (#501 Wave H3). The redacted body (without credentials) is what
// the runner sees via --task-json — keys never reach
// /proc/<runner-pid>/cmdline.
//
// Returns the redacted task body, the credentials file path (empty
// when no credentials supplied), and a provider-list summary for
// audit logging. The summary NEVER includes key values.
func extractCredentialsFromTaskBody(body []byte, workDir string) ([]byte, string, string, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		// Body isn't a JSON object — forward as-is (the bare-prompt
		// convenience shape the runner accepts).
		return body, "", "", nil
	}
	credsRaw, ok := envelope["credentials"]
	if !ok || len(credsRaw) == 0 || string(credsRaw) == "null" {
		return body, "", "", nil
	}
	// Validate the credentials shape before writing.
	var creds []map[string]any
	if err := json.Unmarshal(credsRaw, &creds); err != nil {
		return nil, "", "", fmt.Errorf("decode credentials: %w", err)
	}
	if len(creds) == 0 {
		// Empty list — strip the field, forward without creds.
		delete(envelope, "credentials")
		out, _ := json.Marshal(envelope)
		return out, "", "", nil
	}
	// Build the summary (provider names only).
	providers := make([]string, 0, len(creds))
	for _, c := range creds {
		if p, ok := c["provider"].(string); ok {
			providers = append(providers, p)
		}
	}
	credsFile := filepath.Join(workDir, "credentials.json")
	if err := os.WriteFile(credsFile, credsRaw, 0o600); err != nil {
		return nil, "", "", fmt.Errorf("write credentials file: %w", err)
	}
	// Strip credentials from the body before forwarding.
	delete(envelope, "credentials")
	redacted, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", "", fmt.Errorf("re-marshal task body: %w", err)
	}
	summary := "providers=[" + strings.Join(providers, ",") + "]"
	return redacted, credsFile, summary, nil
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
