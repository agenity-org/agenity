// cmd/runner/agent_pump.go — Wave R4 #465 PTY-backed agent spawn
// inside the runner process. Replaces R1's exec-with-StdoutPipe
// path so the runner is the canonical PTY-owner per
// V0.9.2-ARCHITECTURE §5 #3 + §22.
//
// One PTY, two consumers:
//   - audit fan-out to daemon (R1 contract via daemonClient.SendAudit)
//   - StreamBroker fan-out to A2A SSE consumers (R4 contract via
//     a2a.StreamBroker, wired by cmd/runner/a2a_endpoint.go)
//
// Refs #465 #504.
package main

import (
	"log"

	"github.com/chepherd/chepherd/internal/ptyhost/agentcatalog"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
)

// spawnAgentSession allocates a PTY-backed session.Session for the
// configured agent flavor. Returns nil + nil if no agent is
// configured — that path stays valid for the scaffold / e2e modes
// where the runner is exercised without a real agent.
//
// Caller closes the returned session on shutdown.
func spawnAgentSession(cfg *runnerConfig) (*session.Session, error) {
	if cfg.agentSlug == "" {
		return nil, nil
	}
	agent, err := agentcatalog.Lookup(cfg.agentSlug)
	if err != nil {
		log.Printf("[chepherd-runner] agentcatalog.Lookup %q: %v — skipping agent spawn", cfg.agentSlug, err)
		return nil, nil
	}
	argv := append([]string{agent.Binary}, agent.DefaultArgs...)
	if len(cfg.agentArgs) > 0 {
		argv = append([]string{agent.Binary}, cfg.agentArgs...)
	}
	id := cfg.sid
	if id == "" {
		id = "runner-pty"
	}
	sess, err := session.New(id, session.Spec{
		Command: argv,
		Rows:    24,
		Cols:    80,
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[chepherd-runner] agent PTY-session started: sid=%s binary=%s argv=%v", id, agent.Binary, argv)
	return sess, nil
}

// pumpSessionToAudit forwards each chunk read from the PTY session
// to the daemon as a `pty_output` audit event. R1's contract — kept
// alongside R4's broker fan-out so the daemon's audit log doesn't
// regress.
//
// Caller subscribes to sess.Subscribe(N) and passes the resulting
// Subscriber. nil sub disables the pump. nil dc disables the fan-out
// (back-compat for runner started without --daemon-url).
func pumpSessionToAudit(sess *session.Session, dc *daemonClient) {
	if sess == nil || dc == nil {
		return
	}
	sub, _, err := sess.Subscribe(64)
	if err != nil {
		log.Printf("[chepherd-runner] pumpSessionToAudit subscribe: %v", err)
		return
	}
	go func() {
		defer sess.Unsubscribe(sub)
		for {
			select {
			case chunk, ok := <-sub.Ch:
				if !ok {
					return
				}
				_ = dc.SendAudit("pty_output", string(chunk))
			case <-sub.Done:
				return
			}
		}
	}()
}
