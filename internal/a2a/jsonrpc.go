package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// JSON-RPC 2.0 envelope per the A2A v1.0 spec.
//
// Method names use A2A v1.0 spec §9.1 PascalCase wire shape
// ("SendMessage", "GetTask", "CreateTaskPushNotificationConfig",
// "GetExtendedAgentCard", ...) matching the canonical a2a-python SDK
// (a2aproject/a2a-python, src/a2a/client/transports/jsonrpc.py).
//
// A backward-compatibility alias map (see MethodAliases) also accepts
// the slash + camelCase form ("message/send", "tasks/get", ...) used
// by the stale a2a-js reference SDK (last touched 2026-02-11) and by
// the chepherd builds that shipped between #291 (2026-05-30) and #568.
// Inbound requests using either form route to the same handlers; the
// Agent Card publishes the dual acceptance via the
// x-chepherd-method-aliases extension.
//
// Refs #208 #291 #561 #568.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"` // always "2.0"
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`

	// AuthSubject is the validated bearer subject extracted from the
	// inbound HTTP request's Authorization header. Set by
	// Router.ServeHTTP via SubjectFromContext(req.Context()) — i.e.
	// populated only when AuthMiddleware authenticated the request.
	// Empty for unauthenticated callers OR for direct (non-HTTP)
	// router invocations. Method bodies that auth-gate themselves
	// (#483 Wave A4's agent/getAuthenticatedExtendedCard) read this
	// field; bodies that don't auth-gate ignore it.
	//
	// json:"-" so the field is never marshaled into a JSON-RPC
	// envelope on the wire — it's purely a transport-attached
	// annotation.
	AuthSubject string `json:"-"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"` // always "2.0"
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC 2.0 error codes.
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternalError  = -32603
)

// A2A-specific JSON-RPC error codes per A2A v1.0 spec §5.4 mapping
// table. Use these named constants — magic numbers in handler bodies
// were a #576 audit finding. Refs #561 #576.
const (
	ErrCodeTaskNotFound                 = -32001 // §5.4 TaskNotFoundError
	ErrCodeTaskNotCancelable            = -32002 // §5.4 TaskNotCancelableError
	ErrCodePushNotificationNotSupported = -32003 // §5.4 PushNotificationNotSupportedError
	ErrCodeUnsupportedOperation         = -32004 // §5.4 UnsupportedOperationError
	ErrCodeContentTypeNotSupported      = -32005 // §5.4 ContentTypeNotSupportedError
	ErrCodeInvalidAgentResponse         = -32006 // §5.4 InvalidAgentResponseError
	ErrCodeExtendedCardNotConfigured    = -32007 // §5.4 ExtendedAgentCardNotConfiguredError
	ErrCodeExtensionSupportRequired     = -32008 // §5.4 ExtensionSupportRequiredError
	ErrCodeVersionNotSupported          = -32009 // §5.4 VersionNotSupportedError

	// chepherd-extension codes in the JSON-RPC server-reserved range
	// outside the A2A-claimed §5.4 region (-32001..-32009). Used for
	// chepherd-specific transport-layer errors that don't have a
	// direct A2A spec mapping. Reserved range: -32011..-32099.
	ErrCodeAuthRequired = -32011 // chepherd transport: missing/invalid Bearer JWT
)

// methodHandler is a single A2A method's handler. v0.9.2 scaffold
// returns ErrCodeInternalError for all 11; concrete behavior arrives
// in subsequent sub-branches.
type methodHandler func(req JSONRPCRequest) JSONRPCResponse

// Router dispatches an incoming JSON-RPC request to the registered
// method handler, or returns method-not-found.
type Router struct {
	handlers map[string]methodHandler

	// StreamingHandler, when non-nil, is invoked instead of the
	// JSON-RPC methodHandler when an inbound /jsonrpc POST satisfies
	// ALL of: method == "message/stream" AND Accept header includes
	// "text/event-stream". The handler is responsible for ALL
	// response writing (SSE headers, frames, terminal close). When
	// nil, message/stream falls through to the standard two-call
	// pattern (returns {task, streamId} JSON; client connects to
	// /a2a/stream/<streamId> separately).
	//
	// Refs #480 Wave A1.
	StreamingHandler StreamingHandlerFn
}

// NewRouter returns a Router with all 11 A2A methods registered to
// stub handlers that return JSON-RPC error code -32603 with body
// "scaffold: implementation pending in S5-S7 sub-branches".
//
// Method names use A2A v1.0 §9.1 PascalCase; inbound slash-camelCase
// is accepted via MethodAliases (see the package-level doc).
//
// Refs #208 #291 #568.
func NewRouter() *Router {
	r := &Router{handlers: map[string]methodHandler{}}
	for _, name := range A2AMethodNames() {
		name := name
		r.handlers[name] = func(req JSONRPCRequest) JSONRPCResponse {
			return JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &JSONRPCError{
					Code:    ErrCodeInternalError,
					Message: "scaffold: " + name + " implementation arrives in a later sub-branch",
				},
			}
		}
	}
	return r
}

// Register replaces the stub handler for `method` with a real
// implementation. The canonical wire form per A2A v1.0 §9.1 is
// PascalCase ("SendMessage", "GetTask", ...); slash-camelCase aliases
// ("message/send", "tasks/get", ...) are accepted here for source
// compatibility and resolve to the same handler slot.
// Returns an error if the method name isn't one of the canonical 11
// (or one of their aliases).
func (r *Router) Register(method string, h methodHandler) error {
	canonical := canonicalizeMethod(method)
	if _, ok := r.handlers[canonical]; !ok {
		return errors.New("a2a: unknown method " + method)
	}
	r.handlers[canonical] = h
	return nil
}

// WireDeliverer binds the SendMessage method to a Deliverer.
// Decodes A2A SendMessageParams + invokes Deliverer.Deliver +
// returns the resulting Task wrapped in a SendMessageResult.
//
// After this call the SendMessage handler is no longer a stub.
// Other 10 methods stay scaffold until their own wire-up.
//
// Refs #208 #291 #568.
func (r *Router) WireDeliverer(deliverer Deliverer) error {
	return r.Register("SendMessage", makeSendMessageHandler(deliverer))
}

func makeSendMessageHandler(deliverer Deliverer) methodHandler {
	return func(req JSONRPCRequest) JSONRPCResponse {
		var params SendMessageParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errorResp(req.ID, ErrCodeInvalidParams,
				"decode SendMessageParams: "+err.Error())
		}
		if params.Message.ContextID == "" {
			return errorResp(req.ID, ErrCodeInvalidParams,
				"message.contextId is required (resolves to chepherd session ID for interactive mode); taskId is optional (auto-generated if missing)")
		}
		task, err := deliverer.Deliver(context.Background(), params.Message)
		if err != nil {
			return errorResp(req.ID, ErrCodeInternalError,
				"deliver: "+err.Error())
		}
		return JSONRPCResponse{
			JSONRPC: "2.0", ID: req.ID,
			Result: SendMessageResult{Task: task},
		}
	}
}

func errorResp(id json.RawMessage, code int, message string) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: "2.0", ID: id,
		Error: &JSONRPCError{Code: code, Message: message},
	}
}

// ServeHTTP makes Router an http.Handler for the JSON-RPC POST path
// (typically /jsonrpc on a runner; routes registered in routes.go).
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, nil, ErrCodeInvalidRequest, "method must be POST")
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		writeError(w, nil, ErrCodeParseError, "read body: "+err.Error())
		return
	}
	var rpcReq JSONRPCRequest
	if err := json.Unmarshal(body, &rpcReq); err != nil {
		writeError(w, nil, ErrCodeParseError, "parse JSON: "+err.Error())
		return
	}
	if rpcReq.JSONRPC != "2.0" {
		writeError(w, rpcReq.ID, ErrCodeInvalidRequest, "jsonrpc must be \"2.0\"")
		return
	}
	// #483 Wave A4 — attach the authenticated subject (if any) for
	// method bodies that auth-gate themselves. Empty when no
	// AuthMiddleware ran on this request (dev mode / TokenValidator
	// nil at registration).
	rpcReq.AuthSubject = SubjectFromContext(req.Context())
	// #568 — Canonicalize the wire method to the spec PascalCase form
	// before dispatch. Accepts both PascalCase (per spec §9.1) and the
	// slash-camelCase legacy form (a2a-js stale SDK; pre-#568 chepherd
	// clients). The canonical name is what the streaming branch + the
	// handler-lookup branch see.
	canonicalMethod := canonicalizeMethod(rpcReq.Method)
	rpcReq.Method = canonicalMethod
	// #569 — SendStreamingMessage + SubscribeToTask MUST return
	// Content-Type: text/event-stream per A2A v1.0 §9.4.2 + §9.4.6,
	// directly from the POST response. No two-call streamId pattern
	// + no Accept-header opt-in — the spec is unconditional on the
	// response Content-Type for these methods. When the runner has
	// wired a StreamingHandler, route streaming methods to SSE
	// unconditionally; pre-#569 chepherd's Accept-header gate was a
	// spec violation that returned application/json + {streamId}
	// instead. When StreamingHandler is nil (e.g., chepherd-runner
	// headless scaffold without broker), fall through to the JSON
	// two-call legacy path so the runner still has a working response
	// — the spec-conformant path requires the StreamingHandler wiring
	// at the runtime.
	if r.StreamingHandler != nil &&
		(canonicalMethod == "SendStreamingMessage" || canonicalMethod == "SubscribeToTask") {
		r.StreamingHandler(w, req, rpcReq)
		return
	}
	h, ok := r.handlers[canonicalMethod]
	if !ok {
		writeError(w, rpcReq.ID, ErrCodeMethodNotFound, "unknown method: "+rpcReq.Method)
		return
	}
	resp := h(rpcReq)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &JSONRPCError{Code: code, Message: message},
	})
}

// A2AMethodNames returns the canonical 11 A2A method names in their
// A2A v1.0 spec §9.1 + §5.3 wire shape (PascalCase, matching the
// gRPC RPC names per the proto in a2aproject/A2A@main).
//
// The corresponding canonical spec-compliant SDK is
// a2aproject/a2a-python (client.transports.jsonrpc emits these
// exact strings). The stale a2a-js reference SDK (last touched
// 2026-02-11) still emits slash-camelCase; chepherd accepts both
// forms via MethodAliases() to preserve interop with both ecosystem
// sides. See #561 for the ecosystem-split RCA.
//
// Refs #208 #291 #561 #568.
func A2AMethodNames() []string {
	return []string{
		"SendMessage",
		"SendStreamingMessage",
		"GetTask",
		"ListTasks",
		"CancelTask",
		"SubscribeToTask",
		"CreateTaskPushNotificationConfig",
		"GetTaskPushNotificationConfig",
		"ListTaskPushNotificationConfigs",
		"DeleteTaskPushNotificationConfig",
		"GetExtendedAgentCard",
	}
}

// MethodAliases returns the slash-camelCase → PascalCase translation
// table for backward compatibility with the a2a-js SDK + pre-#568
// chepherd clients. Inbound method names matching a key here are
// rewritten to the value before handler dispatch.
//
// The Agent Card publishes this map verbatim via the
// x-chepherd-method-aliases extension so peers can discover the dual
// acceptance.
//
// Refs #568.
func MethodAliases() map[string]string {
	return map[string]string{
		"message/send":                            "SendMessage",
		"message/stream":                          "SendStreamingMessage",
		"tasks/get":                               "GetTask",
		"tasks/list":                              "ListTasks",
		"tasks/cancel":                            "CancelTask",
		"tasks/resubscribe":                       "SubscribeToTask",
		"tasks/pushNotificationConfig/set":        "CreateTaskPushNotificationConfig",
		"tasks/pushNotificationConfig/get":        "GetTaskPushNotificationConfig",
		"tasks/pushNotificationConfig/list":       "ListTaskPushNotificationConfigs",
		"tasks/pushNotificationConfig/delete":     "DeleteTaskPushNotificationConfig",
		"agent/getAuthenticatedExtendedCard":      "GetExtendedAgentCard",
	}
}

// canonicalizeMethod returns the spec PascalCase form of `m`.
// If `m` is already a canonical name (or unknown), returns `m` as-is.
// If `m` is a slash-camelCase alias, returns its PascalCase target.
func canonicalizeMethod(m string) string {
	if canonical, ok := MethodAliases()[m]; ok {
		return canonical
	}
	return m
}
