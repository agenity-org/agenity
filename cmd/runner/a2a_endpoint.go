// cmd/runner/a2a_endpoint.go — #463 Wave R2.
//
// Per V0.9.2-ARCHITECTURE §5 #16 + §10 Pattern 1, chepherd-runner
// exposes its OWN A2A endpoint at /a2a/{sid}/jsonrpc — distinct from
// (and eventually replacing) the daemon's /jsonrpc.
//
// R2 scope: HTTP listener bound to --a2a-listen, mounting an
// a2a.Router at /a2a/<sid>/jsonrpc with:
//   - message/send       (this PR — stub Deliverer persists Task)
//   - tasks/get          (this PR — MethodBodies, persistence-backed)
//   - tasks/list         (this PR — MethodBodies)
//   - tasks/cancel       (this PR — MethodBodies)
//   - tasks/resubscribe  (this PR — MethodBodies, no SSE → -32004)
//   - message/stream     (this PR — MethodBodies, no SSE → -32004)
//   - tasks/pushNotificationConfig/{set,get,list,delete}  (this PR —
//                          MethodBodies, persistence-backed)
//   - agent/getAuthenticatedExtendedCard (this PR — MethodBodies)
//
// Out of scope (separate Waves):
//   - Per-session Agent Card at /.well-known/agent-card.json (R3 #464)
//   - PTY ownership move (R4 #465) — Deliverer just persists tasks as
//     "submitted" today; R4 makes the runner OWN the agent process so
//     Deliver actually drives stdin + the silence-finalize completer
//   - Daemon retiring its in-process A2A/MCP/Deliverer (R5 #466 cutover)
//
// Refs #463 #208 #225 V0.9.2-ARCHITECTURE §5 #16 §10 Pattern 1.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

// a2aEndpoint is the runner's HTTP server hosting /a2a/<sid>/jsonrpc.
// Constructed by startA2AEndpoint; Close() stops the listener +
// closes the underlying store.
type a2aEndpoint struct {
	listener net.Listener
	server   *http.Server
	store    *sqlite.Store
}

// startA2AEndpoint spins up the runner's per-session A2A endpoint.
// Returns the bound listener address (useful when --a2a-listen is
// "host:0" — OS picks the port).
//
// sid is the chepherd session ID this runner manages; the endpoint
// mounts at /a2a/<sid>/jsonrpc. baseURL is the externally-reachable
// scheme://host[:port] siblings reach this runner on (for Agent
// Card templating in R3 — R2 doesn't ship the Agent Card yet).
// stateDir is where the runner's task-store SQLite file lives.
//
// Caller must Close() the returned endpoint at shutdown.
func startA2AEndpoint(listenAddr, sid, baseURL, stateDir string) (*a2aEndpoint, error) {
	if sid == "" {
		return nil, fmt.Errorf("a2a endpoint: --sid is required (no scaffold mode for A2A — the URL path /a2a/<sid> depends on it)")
	}
	dbPath := filepath.Join(stateDir, "runner-tasks.sqlite")
	store, err := sqlite.NewStore(context.Background(), dbPath)
	if err != nil {
		return nil, fmt.Errorf("a2a endpoint: open task store: %w", err)
	}

	// Build the router + wire all 11 methods.
	router := a2a.NewRouter()

	// SendMessage uses a runner-local Deliverer that just persists
	// the Task as "submitted". Wave R4 replaces this with a PTY-
	// owning Deliverer that drives the agent process.
	deliverer := newRunnerDeliverer(store, sid)
	if err := router.WireDeliverer(deliverer); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("a2a endpoint: wire deliverer: %w", err)
	}

	// The other 10 methods come from MethodBodies. AgentCardFn
	// returns a minimal card today — R3 ships the canonical
	// per-session card.
	methodBodies := &a2a.MethodBodies{
		Store: store,
		AgentCardFn: func() a2a.AgentCard {
			return minimalRunnerCard(sid, baseURL)
		},
		RunnerSID:   sid,
		SubscribeFn: nil, // streaming methods → -32004 until R3+ wires SSE
	}
	if err := methodBodies.Register(router); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("a2a endpoint: register method bodies: %w", err)
	}

	// Mount at /a2a/<sid>/jsonrpc — exact path (no path-param
	// routing) since each runner manages exactly one session. The
	// daemon's directory tells siblings which sid lives at which
	// runner address.
	mux := http.NewServeMux()
	mux.Handle("/a2a/"+sid+"/jsonrpc", router)
	// Healthz so callers (operator curl, R5 cutover smoke tests) can
	// confirm the endpoint is up without a JSON-RPC roundtrip.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("a2a endpoint: listen %s: %w", listenAddr, err)
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	log.Printf("[chepherd-runner] A2A endpoint listening on %s (/a2a/%s/jsonrpc)", ln.Addr().String(), sid)
	return &a2aEndpoint{listener: ln, server: srv, store: store}, nil
}

// Close stops the HTTP server + closes the task store.
func (e *a2aEndpoint) Close() {
	if e == nil {
		return
	}
	if e.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = e.server.Shutdown(ctx)
	}
	if e.store != nil {
		_ = e.store.Close()
	}
}

// Addr returns the bound listener address (useful when --a2a-listen
// was "host:0" — OS picks port).
func (e *a2aEndpoint) Addr() string {
	if e == nil || e.listener == nil {
		return ""
	}
	return e.listener.Addr().String()
}

// minimalRunnerCard is the placeholder Agent Card returned today by
// agent/getAuthenticatedExtendedCard. R3 (#464) ships the canonical
// per-session Agent Card; this is a stub so the JSON-RPC method
// doesn't 5xx in the meantime.
func minimalRunnerCard(sid, baseURL string) a2a.AgentCard {
	url := baseURL
	if url == "" {
		url = "/a2a/" + sid + "/jsonrpc"
	} else {
		// strip trailing slash so we don't emit double slash
		for len(url) > 0 && url[len(url)-1] == '/' {
			url = url[:len(url)-1]
		}
		url += "/a2a/" + sid + "/jsonrpc"
	}
	return a2a.AgentCard{
		ProtocolVersion: "1.0",
		Name:            "chepherd-runner-" + sid,
		URL:             url,
		Version:         runnerSelfVersion,
		Capabilities: a2a.AgentCapabilities{
			Streaming:         false, // R3+ wires SSE
			PushNotifications: true,
			ExtendedCard:      false,
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             []a2a.AgentSkill{},
	}
}

// ─── runnerDeliverer ──────────────────────────────────────────────

// runnerDeliverer is the stub a2a.Deliverer the runner uses for
// message/send in R2. It persists the Task as "submitted" + returns
// the working task immediately. Wave R4 (#465) replaces this with
// a PTY-owning Deliverer that actually drives the agent process +
// completes the Task via the silence-finalize completer.
//
// IMPORTANT: this is not a workaround — it's the architecturally-
// correct shape for R2 scope. The runner OWNS the task lifecycle now
// (via its sqlite store); R4 just adds the PTY transport leg. Sibling
// peers can SendMessage + GetTask today; the message body sits as
// "submitted" until R4 lights the agent-process leg.
type runnerDeliverer struct {
	store     *sqlite.Store
	runnerSID string
}

func newRunnerDeliverer(store *sqlite.Store, runnerSID string) *runnerDeliverer {
	return &runnerDeliverer{store: store, runnerSID: runnerSID}
}

// Deliver implements a2a.Deliverer. Persists the input Message + the
// issued Task, returns the Task with state="working".
func (d *runnerDeliverer) Deliver(ctx context.Context, msg a2a.Message) (*a2a.Task, error) {
	taskID := msg.TaskID
	if taskID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			id = uuid.New()
		}
		taskID = id.String()
	}
	task := &a2a.Task{
		ID:        taskID,
		ContextID: msg.ContextID,
		Kind:      "task",
		Status:    a2a.TaskStatus{State: a2a.TaskStateWorking},
	}
	// Persist the input Message + outbound Task so subsequent
	// tasks/get returns the envelope. Failure persists the task as
	// failed instead so siblings see the error rather than a 5xx.
	inputBlob, _ := json.Marshal(msg)
	outputBlob, _ := json.Marshal(task)
	now := time.Now().UTC()
	rec := &persistence.Task{
		ID:         task.ID,
		RunnerSID:  d.runnerSID,
		State:      string(a2a.TaskStateWorking),
		Method:     "message/send",
		InputBlob:  inputBlob,
		OutputBlob: outputBlob,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := d.store.Tasks().Save(ctx, rec); err != nil {
		// Persistence failed — return a failed-state Task envelope
		// so the caller sees a structured error rather than HTTP 5xx.
		task.Status.State = a2a.TaskStateFailed
		return task, fmt.Errorf("runnerDeliverer: persist: %w", err)
	}
	return task, nil
}
