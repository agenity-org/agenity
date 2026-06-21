// cmd/runner/audit_middleware.go — #488 Wave AU1 inbound-audit
// HTTP middleware on the runner's /a2a/<sid>/jsonrpc endpoint.
//
// Per V0.9.2-ARCH §10 Pattern 1 step 24: every successful inbound
// A2A call MUST emit an audit.received event. AU1 emits POST-
// handler-completion so latency_ms is meaningful + status reflects
// whether the handler 200'd or 4xx/5xx'd.
//
// Hook order on the runner's mux:
//   /jsonrpc ←  audit middleware  ←  JWT middleware (T1 #486)  ←  router
//
// JWT middleware runs FIRST so the audit middleware can read the
// JWT sub claim out of context (caller attribution). Failed-auth
// 401s emit NO audit event — the caller never identified.
//
// Refs #488 V0.9.2-ARCH §10 #5 #8.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/agenity-org/agenity/internal/auth"
	"github.com/agenity-org/agenity/internal/runtime"
)

// auditMiddleware wraps next with an audit.received emission on the
// success/error path. emitter receives the event; nil disables the
// middleware (passthrough, back-compat for tests).
func auditMiddleware(emitter runtime.AuditEmitter, runnerSID string, next http.Handler) http.Handler {
	if emitter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Peek at the inbound body to extract method + JWT-derived
		// caller — needed for the event before invoking the handler.
		// Bounded read so a multi-MB body doesn't blow memory; the
		// A2A spec caps frames much smaller.
		var bodyBytes []byte
		if r.Body != nil {
			bodyBytes, _ = io.ReadAll(io.LimitReader(r.Body, 256*1024))
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}
		method, taskID := peekJSONRPCMethodAndTaskID(bodyBytes)
		// T1 #486 sets the JWT sub claim under RunnerSubjectKey when
		// JWTRunnerMiddleware ran before us. Empty when JWT is off
		// (dev mode) — that's fine; AU2 dashboard correlates by JTI
		// + Timestamp + Callee in that case.
		caller := auth.SubjectFromRunnerContext(r.Context())

		// Capture status code via a response interceptor.
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// Fire-and-forget — DO NOT block the response on emission.
		go func() {
			ev := runtime.NewAuditEvent(
				runtime.AuditEventTypeReceived,
				method,
				caller,
				runnerSID,
			)
			ev.TaskID = taskID
			ev.LatencyMS = time.Since(start).Milliseconds()
			if rec.status >= 400 {
				ev.Status = "error"
				ev.Error = http.StatusText(rec.status)
			}
			emitter.EmitAuditEvent(ev)
		}()
	})
}

// statusRecorder captures the status code on its way out so the
// audit event can record success vs error.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.wrote = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.wrote = true
		// Implicit 200 (handler didn't call WriteHeader before
		// Write). Default status is already 200; no flip needed.
	}
	return s.ResponseWriter.Write(b)
}

// peekJSONRPCMethodAndTaskID extracts the JSON-RPC method + (if
// present) the task id from a request body. Best-effort — returns
// empty strings when the body isn't valid JSON-RPC, which is fine
// for audit attribution (the event still records latency + status).
func peekJSONRPCMethodAndTaskID(body []byte) (method, taskID string) {
	if len(body) == 0 {
		return "", ""
	}
	var rpc struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return "", ""
	}
	method = rpc.Method
	if len(rpc.Params) == 0 {
		return method, ""
	}
	// Try to extract a taskId from common A2A param shapes.
	var p1 struct {
		TaskID string `json:"taskId"`
	}
	if json.Unmarshal(rpc.Params, &p1) == nil && p1.TaskID != "" {
		return method, p1.TaskID
	}
	var p2 struct {
		Message struct {
			TaskID string `json:"taskId"`
		} `json:"message"`
	}
	if json.Unmarshal(rpc.Params, &p2) == nil && p2.Message.TaskID != "" {
		return method, p2.Message.TaskID
	}
	return method, ""
}
