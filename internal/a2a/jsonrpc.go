package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// JSON-RPC 2.0 envelope per the A2A spec. v0.9.2 scaffold registers
// all 11 methods; bodies arrive in S5-S7.
//
// PascalCase method names match the A2A spec exactly (NOT the legacy
// snake_case from pre-v0.9.2 internal MCP tools, which retire in S2).
//
// Refs #208.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"` // always "2.0"
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
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

// Standard JSON-RPC 2.0 error codes used by the A2A scaffold.
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternalError  = -32603
)

// methodHandler is a single A2A method's handler. v0.9.2 scaffold
// returns ErrCodeInternalError for all 11; concrete behavior arrives
// in subsequent sub-branches.
type methodHandler func(req JSONRPCRequest) JSONRPCResponse

// Router dispatches an incoming JSON-RPC request to the registered
// method handler, or returns method-not-found.
type Router struct {
	handlers map[string]methodHandler
}

// NewRouter returns a Router with all 11 A2A methods registered to
// stub handlers that return JSON-RPC error code -32603 with body
// "scaffold: implementation pending in S5-S7 sub-branches".
//
// Method names use PascalCase per the A2A spec (NOT snake_case).
//
// Refs #208.
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
// implementation. Returns an error if the method name isn't one of
// the canonical 11.
func (r *Router) Register(method string, h methodHandler) error {
	if _, ok := r.handlers[method]; !ok {
		return errors.New("a2a: unknown method " + method)
	}
	r.handlers[method] = h
	return nil
}

// WireDeliverer binds the SendMessage method to a Deliverer.
// Decodes A2A SendMessageParams + invokes Deliverer.Deliver +
// returns the resulting Task wrapped in a SendMessageResult.
//
// After this call the SendMessage handler is no longer a stub. Other
// 10 methods stay scaffold until their own wire-up.
//
// Refs #208.
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
	h, ok := r.handlers[rpcReq.Method]
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

// A2AMethodNames returns the canonical 11 A2A method names in
// PascalCase per the A2A spec.
func A2AMethodNames() []string {
	return []string{
		"SendMessage",
		"SendStreamingMessage",
		"GetTask",
		"ListTasks",
		"CancelTask",
		"ResubscribeTask",
		"SetTaskPushNotificationConfig",
		"GetTaskPushNotificationConfig",
		"ListTaskPushNotificationConfigs",
		"DeleteTaskPushNotificationConfig",
		"GetAuthenticatedExtendedCard",
	}
}
