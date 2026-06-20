// internal/mcpserver/send_to_operator_test.go — regression for the
// operator-reply drop (operator-reported 2026-06-20).
//
// Bug: an operator message reaches an agent with from="operator". The agent
// replies via chepherd.send_to_session("operator", body) per the knock-pattern
// briefing. But "operator" is the human, not an agent PTY session, so
// s.rt.Get("operator") returned nil and the shim answered "no such session:
// operator" — the reply was silently dropped and never reached the Talk
// transcript. The operator messaged all 5 agents and saw zero replies.
//
// Fix: send_to_session addressed to "operator"/"human" routes into the
// HumanInbox (the same sink alert_human uses), which collectTranscriptRows
// surfaces in the Talk transcript + dashboard inbox.
package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/chepherd/chepherd/internal/runtime"
)

func newServerWithRuntime(t *testing.T) (*Server, *runtime.Runtime) {
	t.Helper()
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	return New(rt), rt
}

func sendToSession(s *Server, caller, name, body string) rpcResp {
	s.lastCaller = caller
	args, _ := json.Marshal(map[string]string{"name": name, "body": body})
	return s.toolCallDirect(nil, "send_to_session", json.RawMessage(args))
}

// Core regression: send_to_session→operator must succeed AND land in the
// HumanInbox attributed to the calling agent.
func TestSendToSession_Operator_RoutesToInbox(t *testing.T) {
	s, rt := newServerWithRuntime(t)

	resp := sendToSession(s, "qa", "operator", "I am fine, thanks!")
	if resp.Error != nil {
		t.Fatalf("send_to_session→operator returned error %d: %s (want success — must NOT be 'no such session')",
			resp.Error.Code, resp.Error.Message)
	}

	inbox := rt.Inbox()
	var found bool
	for _, e := range inbox {
		if e.From == "qa" && e.Body == "I am fine, thanks!" {
			found = true
		}
	}
	if !found {
		t.Fatalf("operator reply not in HumanInbox; inbox=%+v", inbox)
	}
}

// "human" is the alternate handle for the operator — same routing.
func TestSendToSession_Human_RoutesToInbox(t *testing.T) {
	s, rt := newServerWithRuntime(t)
	if resp := sendToSession(s, "architect", "human", "model is gemini-2.5-flash"); resp.Error != nil {
		t.Fatalf("send_to_session→human errored: %s", resp.Error.Message)
	}
	if len(rt.Inbox()) != 1 {
		t.Fatalf("want 1 inbox entry, got %d", len(rt.Inbox()))
	}
}

// Guard: a genuinely unknown agent session still errors "no such session"
// (the operator route must not swallow real misrouted sends).
func TestSendToSession_UnknownAgent_StillErrors(t *testing.T) {
	s, _ := newServerWithRuntime(t)
	resp := sendToSession(s, "qa", "ghost-agent", "hi")
	if resp.Error == nil || resp.Error.Code != -32000 {
		t.Fatalf("unknown agent: want -32000 'no such session', got %+v", resp.Error)
	}
}

// Casing / whitespace robustness: the server lowercases+trims the recipient
// (server.go: strings.ToLower(strings.TrimSpace(a.Name))), so an LLM that
// echoes the from= handle with stray casing/padding ("Operator", " operator ",
// "HUMAN", "  Human") still routes to the HumanInbox rather than hitting the
// "no such session" dead path. These document + lock that handling — if the
// code ever stops normalizing, they go RED instead of silently regressing to
// the original drop bug. (Verified against server.go: it DOES normalize, so
// these are GREEN — confirming behavior, not papering over a gap.)
func TestSendToSession_OperatorHandle_CasingAndWhitespace_RoutesToInbox(t *testing.T) {
	cases := []struct {
		name      string
		recipient string
	}{
		{"capitalized Operator", "Operator"},
		{"padded operator", " operator "},
		{"uppercase HUMAN", "HUMAN"},
		{"padded+capitalized Human", "  Human  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rt := newServerWithRuntime(t)
			body := "reply for " + tc.name
			resp := sendToSession(s, "qa", tc.recipient, body)
			if resp.Error != nil {
				t.Fatalf("send_to_session→%q returned error %d: %s (casing/whitespace must still route to operator inbox, not 'no such session')",
					tc.recipient, resp.Error.Code, resp.Error.Message)
			}
			var found bool
			for _, e := range rt.Inbox() {
				if e.From == "qa" && e.Body == body {
					found = true
				}
			}
			if !found {
				t.Fatalf("recipient %q: reply not in HumanInbox; inbox=%+v", tc.recipient, rt.Inbox())
			}
		})
	}
}
