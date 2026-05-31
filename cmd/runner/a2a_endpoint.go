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
	"github.com/chepherd/chepherd/internal/auth"
	"github.com/chepherd/chepherd/internal/runtime/knock"
	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
	"github.com/chepherd/chepherd/internal/runtime"
)

// a2aEndpoint is the runner's HTTP server hosting /a2a/<sid>/jsonrpc.
// Constructed by startA2AEndpoint; Close() stops the listener +
// closes the underlying store.
type a2aEndpoint struct {
	listener net.Listener
	server   *http.Server
	store    *sqlite.Store
}

// Handler returns the underlying http.Handler so callers (e.g. the
// F7.1 reverse-proxy tunnel client #585) can route inbound proxied
// requests through the same mux as the direct /a2a/<sid>/jsonrpc
// listener — single source of truth for A2A routing.
func (e *a2aEndpoint) Handler() http.Handler { return e.server.Handler }

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
func startA2AEndpoint(listenAddr, sid, name, baseURL, daemonURL, stateDir string, ptySession *session.Session, jwtCfg *auth.RunnerJWTMiddlewareConfig, auditEmitter runtime.AuditEmitter) (*a2aEndpoint, error) {
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

	// #465 Wave R4 — runner-local StreamBroker for SSE fan-out.
	// SendMessage handlers + tasks/resubscribe etc. consume this
	// broker; PumpPTYToBroker (spawned per Deliver call) publishes
	// to it as PTY chunks arrive from the agent process.
	broker := a2a.NewStreamBroker()

	// SendMessage uses a runner-local Deliverer.
	deliverer := newRunnerDeliverer(store, sid)
	// #465 Wave R4 — when ptySession is wired, the deliverer drives
	// the PTY directly (write → MarkSendNow → silence-finalize
	// completer flips state→completed). Otherwise R2's persist-only
	// fallback runs.
	if ptySession != nil {
		deliverer = deliverer.withPTY(ptySession, ptySession, broker)
	}
	if err := router.WireDeliverer(deliverer); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("a2a endpoint: wire deliverer: %w", err)
	}

	// The other 10 methods come from MethodBodies. AgentCardFn
	// returns a minimal card today — R3 ships the canonical
	// per-session card.
	// #464 Wave R3 — daemon JWKS URL surfaced in the Agent Card's
	// security scheme description so peers know where to fetch
	// signing keys. Empty daemonURL leaves the JWKS reference
	// relative (peers resolve against the daemon they discovered
	// the card through).
	//
	// #587 — daemonURL arrives as `ws://...` / `wss://...` because
	// the runner uses WebSocket transport to register with the
	// daemon. But JWKS is an HTTP resource — peers can't fetch it
	// via the ws:// scheme. Translate ws→http / wss→https so the
	// emitted JWKS URL is dereferenceable.
	daemonJWKSURL := ""
	if daemonURL != "" {
		daemonJWKSURL = joinBaseAndPath(httpFromWS(daemonURL), a2a.JWKSPath)
	}
	card := buildRunnerAgentCard(sid, name, baseURL, daemonJWKSURL)
	methodBodies := &a2a.MethodBodies{
		Store: store,
		AgentCardFn: func() a2a.AgentCard {
			return card
		},
		RunnerSID:   sid,
		SubscribeFn: nil, // streaming methods → -32004 until A2/A3 wire SSE in MethodBodies (A1 wired stream inline in jsonrpc.go)
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
	// Middleware order outer → inner: JWT → Audit → Router.
	// JWT runs FIRST so audit middleware reads the sub claim via
	// auth.SubjectFromRunnerContext. nil jwtCfg = dev mode; nil
	// emitter = no daemon URL. #486 T1 + #488 AU1 + #465 R4.
	jsonrpcHandler := http.Handler(router)
	jsonrpcHandler = auditMiddleware(auditEmitter, sid, jsonrpcHandler)
	if jwtCfg != nil {
		jsonrpcHandler = auth.JWTRunnerMiddleware(jwtCfg, jsonrpcHandler)
	}
	mux.Handle("/a2a/"+sid+"/jsonrpc", jsonrpcHandler)
	// #464 Wave R3 — per-session Agent Card mounts (unauthenticated).
	cardHandler := a2a.ServeAgentCard(&card)
	mux.Handle("/a2a/"+sid+a2a.AgentCardPath, cardHandler)
	mux.Handle("/a2a/"+sid+a2a.AgentCardAliasPath, cardHandler)
	// #465 Wave R4 — SSE stream endpoint mounted under the per-
	// session URL prefix so subscribers fetch chunks from the same
	// host they discovered the Agent Card on.
	mux.Handle("/a2a/"+sid+"/stream/", broker.Handler())
	// Healthz so callers (operator curl, R5 cutover smoke tests) can
	// confirm the endpoint is up without a JSON-RPC roundtrip.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	// #492 Wave F2 — WebRTC DataChannel transport for A2A JSON-RPC.
	// The runner mounts /webrtc/offer with an answerer factory that
	// attaches ServeJSONRPC to the new PC's DataChannel; inbound
	// envelopes route through the same A2A router that backs the
	// HTTP /jsonrpc endpoint (jsonrpcHandler above, minus the JWT
	// middleware because the cross-org JWT story rides T-series mTLS).
	// When the peer's AgentCard advertises x-chepherd-p2p.supported=
	// true, callers prefer DataChannel via webrtcrtc.JSONRPCClient.
	mountF2DataChannel(mux, jsonrpcHandler)

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

// Store returns the runner's task store so other surfaces
// (chepherd.get_task MCP tool — #473 Wave K2) can read tasks
// persisted by the A2A SendMessage path. nil when the endpoint
// wasn't started.
func (e *a2aEndpoint) Store() *sqlite.Store {
	if e == nil {
		return nil
	}
	return e.store
}

// buildRunnerAgentCard constructs the canonical per-session Agent Card
// per V0.9.2-ARCHITECTURE §5 #9 + §7 + §12.1. Replaces R2's
// minimalRunnerCard stub.
//
// Wire shape:
//   - protocolVersion = "1.0" (A2A v1.0)
//   - name            = "chepherd-runner-<sid>" (operator-visible
//                       chepherd-runner-X form; runner's
//                       --name flag value lifts into description if
//                       set so the spec-required `name` stays
//                       runner-instance-stable)
//   - url             = <baseURL>/a2a/<sid>/jsonrpc (the SendMessage
//                       endpoint a sibling POSTs to)
//   - version         = runnerSelfVersion
//   - capabilities    = {streaming=true (Wave A1 #511 SSE shipped),
//                       pushNotifications=false (Wave A3 lights it),
//                       extendedCard=false (Wave A5 — state-transition
//                       history)}
//   - defaultInputModes / defaultOutputModes = ["text/plain"]
//   - skills          = [] (runner-flavor-specific skills are
//                       advertised by the AGENT process the runner
//                       hosts, NOT by the runner itself; Wave A5+
//                       may template a chepherd-runner skill block)
//   - security        = [{httpAuth: ["chepherd-jwt"]}] — Bearer JWT
//                       issued by daemon's JWKS-published keys
//                       (#505 Wave T2)
//   - securitySchemes = {chepherd-jwt: HTTP Bearer JWT, with the
//                       daemon's JWKS URL surfaced in description so
//                       peers know where to fetch the public keys}
//   - x-chepherd-p2p  = placeholder (Wave F2/F3/F4 populates with
//                       WebRTC signaling endpoint + ICE servers +
//                       supported data channels)
//
// baseURL is the scheme://host[:port] siblings reach this runner on
// (the --a2a-base-url flag value). Empty leaves URLs relative.
//
// daemonJWKSURL is the absolute URL of the daemon's JWKS document
// (scheme://daemon-host/.well-known/jwks.json), populated from the
// runner's --daemon-url flag at startup. Surfaced in the
// securitySchemes.chepherd-jwt description so peers know where to
// fetch the public keys for JWT verification. Empty fine (the
// description falls back to the relative path).
func buildRunnerAgentCard(sid, runnerName, baseURL, daemonJWKSURL string) a2a.AgentCard {
	endpointURL := joinBaseAndPath(baseURL, "/a2a/"+sid+"/jsonrpc")
	description := "chepherd-runner v" + runnerSelfVersion + " hosting one A2A-protocol agent session"
	if runnerName != "" {
		description = description + " (operator handle: @" + runnerName + ")"
	}
	jwksRef := daemonJWKSURL
	if jwksRef == "" {
		jwksRef = a2a.JWKSPath // relative — peers resolve against daemon they discovered the card through
	}
	return a2a.AgentCard{
		ProtocolVersion: "1.0",
		Name:            "chepherd-runner-" + sid,
		Description:     description,
		URL:             endpointURL,
		Version:         runnerSelfVersion,
		Capabilities: a2a.AgentCapabilities{
			Streaming:         true,  // Wave A1 #511 SSE binding live
			PushNotifications: false, // Wave A3 lights this
			ExtendedCard:      false, // Wave A5 — state-transition history
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             []a2a.AgentSkill{},
		Security: []map[string][]string{
			{"chepherd-jwt": {}},
		},
		SecuritySchemes: map[string]a2a.SecurityScheme{
			"chepherd-jwt": {
				Type:         "http",
				Scheme:       "bearer",
				BearerFormat: "JWT",
				Description:  "Per-call JWT minted by chepherd-daemon (POST /api/v1/jwt/mint, Wave D2). Verify against daemon JWKS at " + jwksRef + " (Wave T2). ES256 signing.",
			},
		},
		XChepherdP2P: func() *a2a.ChepherdP2PExtension {
			// #488 Wave F1 — populate the x-chepherd-p2p extension's
			// signaling endpoint with this runner's reachable
			// /webrtc/offer URL so chepherd-aware peers can dial
			// the SDP exchange directly. Empty a2aBaseURL → empty
			// SignalingEndpoint (R1 scaffold mode).
			ext := a2a.DefaultExtension()
			ext.PopulateSignalingEndpoint(baseURL)
			return ext
		}(),
		XChepherdMethodAliases: a2a.MethodAliases(),
	}
}

// httpFromWS translates ws:// → http://, wss:// → https://. Used
// when surfacing a URL in an Agent Card field that peers will
// dereference via HTTP (e.g. JWKS, well-known doc). Idempotent;
// leaves non-ws schemes unchanged.
//
// Per #587: chepherd-runner registers with chepherd-daemon over
// WebSocket so daemonURL arrives as ws://chepherd:9090. The
// daemon's JWKS resource sits at the same host:port over HTTP
// — peers can't dereference ws://...:9090/.well-known/jwks.json
// (libcurl returns "Protocol \"ws\" not supported"). Translation
// keeps the Agent Card's JWKS reference dereferenceable.
func httpFromWS(u string) string {
	switch {
	case len(u) > 5 && u[:5] == "wss:/":
		return "https" + u[3:]
	case len(u) > 4 && u[:4] == "ws:/":
		return "http" + u[2:]
	default:
		return u
	}
}

// joinBaseAndPath cleanly composes base + path. Empty base → relative
// path. Trailing slash on base is stripped so we don't emit a double
// slash.
func joinBaseAndPath(base, path string) string {
	if base == "" {
		return path
	}
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base + path
}

// ─── runnerDeliverer (Wave R4) ────────────────────────────────────

// runnerDeliverer is the runner-side a2a.Deliverer. R4 evolution:
//
//   - R2 #463: persisted Task as "working" + returned immediately
//     (no agent process integration)
//   - R4 #465: when a PTY session is wired, writes msg → PTY stdin,
//     spawns PumpPTYToBroker goroutine, completer transitions Task
//     to "completed" + persists agent response when silence-finalize
//     fires
//
// When pty == nil (e.g. runner started without --agent flag, or
// agentcatalog.Lookup failed), the deliverer falls back to R2's
// persist-only path. Sibling peers can still SendMessage + GetTask;
// the response stays empty.
//
// IMPORTANT: this is NOT a workaround per principle 14. The PTY-on
// path is the architecturally-correct target shape; the persist-only
// fallback is honest about not having an agent to drive. Tests can
// exercise either path.
type runnerDeliverer struct {
	store     *sqlite.Store
	runnerSID string

	// pty + broker arrive in R4 — they're nil for the R2-style
	// persist-only path. Both non-nil ⇒ Deliver drives the PTY +
	// pumps to the broker.
	pty    runtime.SubscriberSource // *session.Session in production
	ptyW   ptyWriter                // *session.Session also satisfies this
	broker runtime.BrokerPublisher  // *a2a.StreamBroker in production

	// #549 — test seams. markFactory + markObserver are nil in
	// production (Deliver uses runtime.NewPumpSendMark + leaves the
	// mark anonymous). Tests inject:
	//   markFactory  = runtime.NewPumpSendMarkWithSilenceFire so the
	//                  spawned mark has the deterministic SilenceFire
	//                  channel
	//   markObserver = a callback receiving the created mark so the
	//                  test can call mark.MarkSilenceFire() at the
	//                  precise moment it wants silence-finalize to
	//                  fire (no wall-clock dependency)
	// Both nil in production = pre-#549 behavior.
	markFactory  func() *runtime.PumpSendMark
	markObserver func(*runtime.PumpSendMark)
}

// ptyWriter is the minimal write seam — *session.Session satisfies
// it. Lets unit tests inject a fake that records what was written
// without needing a real PTY.
type ptyWriter interface {
	Write(p []byte) (int, error)
}

func newRunnerDeliverer(store *sqlite.Store, runnerSID string) *runnerDeliverer {
	return &runnerDeliverer{store: store, runnerSID: runnerSID}
}

// withPTY returns a copy of d with pty + broker wired. Caller passes
// the runner's singleton session + broker.
func (d *runnerDeliverer) withPTY(pty runtime.SubscriberSource, ptyW ptyWriter, broker runtime.BrokerPublisher) *runnerDeliverer {
	out := *d
	out.pty = pty
	out.ptyW = ptyW
	out.broker = broker
	return &out
}

// Deliver implements a2a.Deliverer.
//
// Persists the input Message + the issued Task. When pty + broker
// are wired (R4 path):
//   - Spawns PumpPTYToBroker goroutine FIRST (subscribes the broker
//     before any writes so banner chrome doesn't get attributed to
//     this task — #387 P0 send-mark coordinates the boundary)
//   - Waits for pump's Subscribed signal
//   - Writes msg's user-text Parts → PTY stdin
//   - Signals MarkSendNow to the pump
//   - Returns the Task in state="working"; silence-finalize will
//     flip it to "completed" via the completer
func (d *runnerDeliverer) Deliver(ctx context.Context, msg a2a.Message) (*a2a.Task, error) {
	// #586 — per V0.9.2-ARCH §10 Pattern 1, the runner endpoint at
	// /a2a/<sid>/jsonrpc serves EXACTLY one session. SendMessage with
	// a ContextID that doesn't match the runner's sid is either:
	//   - a routing bug (caller hit the wrong runner)
	//   - a cross-session bleed (two clients confused which sid to
	//     address)
	// Pre-#586 the runner silently auto-created a task with the
	// mismatched ContextID — more permissive than the daemon's
	// equivalent surface (daemon returns -32603 on unknown contextId).
	// Strict-match brings runner + daemon behaviour into alignment
	// AND surfaces the routing error to the caller.
	//
	// Empty ContextID is allowed for compatibility (some clients
	// don't supply one; the path /a2a/<sid> already disambiguates).
	if msg.ContextID != "" && msg.ContextID != d.runnerSID {
		return nil, fmt.Errorf("runnerDeliverer: contextId %q does not match this runner's sid %q (each runner serves exactly one session per /a2a/<sid>)", msg.ContextID, d.runnerSID)
	}

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
		task.Status.State = a2a.TaskStateFailed
		return task, fmt.Errorf("runnerDeliverer: persist: %w", err)
	}

	// R4 PTY-driving path. pty + ptyW + broker must all be set;
	// otherwise fall back to R2's persist-only behavior.
	if d.pty != nil && d.ptyW != nil && d.broker != nil {
		// #549 — markFactory + markObserver test seams. Production
		// leaves them nil → standard NewPumpSendMark + no observer.
		var mark *runtime.PumpSendMark
		if d.markFactory != nil {
			mark = d.markFactory()
		} else {
			mark = runtime.NewPumpSendMark()
		}
		if d.markObserver != nil {
			d.markObserver(mark)
		}
		completer := d.completer()
		go runtime.PumpPTYToBroker(d.broker, d.pty, task, completer, mark)
		// Wait for the pump to subscribe so the byte stream from the
		// upcoming Write lands on the live channel (not just the
		// pre-subscribe ring snapshot). #387 P0.
		<-mark.Subscribed
		// #472 Wave K1 — write the knock marker + CR submit so
		// claude-code processes the marker as a user message turn.
		// The "no submit sequence" note was aspirational for a future
		// output-injection path; in the current PTY stdin path the
		// submit is required or the marker idles in the input box.
		from := auth.SubjectFromRunnerContext(ctx)
		if from == "" {
			from = "anonymous"
		}
		input := knock.FormatKnock(task.ID, from)
		if _, err := d.ptyW.Write([]byte(input)); err != nil {
			task.Status.State = a2a.TaskStateFailed
			return task, fmt.Errorf("runnerDeliverer: PTY write: %w", err)
		}
		// CR submits the knock marker to claude-code's TUI.
		if _, err := d.ptyW.Write([]byte("\r")); err != nil {
			task.Status.State = a2a.TaskStateFailed
			return task, fmt.Errorf("runnerDeliverer: PTY submit: %w", err)
		}
		mark.MarkSendNow()
	}
	return task, nil
}

// completer returns the callback PumpPTYToBroker invokes when
// silence-finalize fires (or sub.Done / chan close). It:
//   - strips ANSI chrome
//   - persists the agent's response as a Message{role:"agent"} into
//     the Task's history (TODO: requires Task.History column
//     evolution — for R4 we just flip Task state to "completed"
//     and store the response in the OutputBlob)
//   - flips Task state to "completed"
//
// Wave A5 (#485) will extend Task persistence with full history.
func (d *runnerDeliverer) completer() func(taskID, response string) {
	return func(taskID, response string) {
		clean := runtime.StripANSI(response)
		ctx := context.Background()
		rec, err := d.store.Tasks().Get(ctx, taskID)
		if err != nil || rec == nil {
			return
		}
		rec.State = string(a2a.TaskStateCompleted)
		rec.UpdatedAt = time.Now().UTC()
		// Extract contextId from the persisted input Message so the
		// completed-task envelope keeps it (consumers correlate per
		// context). persistence.Task doesn't carry contextID as a
		// column today (Wave A5 #485 may add it); decoding the
		// InputBlob is the source of truth.
		contextID := ""
		var inputMsg a2a.Message
		if err := json.Unmarshal(rec.InputBlob, &inputMsg); err == nil {
			contextID = inputMsg.ContextID
		}
		completedTask := &a2a.Task{
			ID:        taskID,
			ContextID: contextID,
			Kind:      "task",
			Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted},
			Artifacts: []a2a.Artifact{{
				ArtifactID: taskID + "-response",
				Parts:      []a2a.Part{{Kind: "text", Text: clean}},
			}},
		}
		rec.OutputBlob, _ = json.Marshal(completedTask)
		_ = d.store.Tasks().Save(ctx, rec)
	}
}

// extractMessageText walks msg.Parts and concatenates all text parts.
// Non-text parts (file, data) are ignored — R4 PTY input is line-mode
// only.
func extractMessageText(msg a2a.Message) string {
	var b []byte
	for _, p := range msg.Parts {
		if p.Kind == "text" {
			b = append(b, p.Text...)
		}
	}
	return string(b)
}
