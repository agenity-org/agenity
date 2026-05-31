// cmd/runner/agent_pump.go — chepherd-runner's child-process spawn +
// stdout/stderr → daemon audit-event pump. #504 Wave R1.
//
// SCOPE: simplest realisation of "runner hosts the agent + streams
// PTY output to daemon". For R1 we exec the configured flavor via
// agentcatalog.Lookup (resolves the binary + default argv), wire its
// stdout/stderr into a single bytes channel, and forward each chunk as
// a `kind: "pty_output"` audit notification. Real PTY ownership
// (creack/pty TTY allocation, ANSI-clean streaming, attach-WS sharing
// with dashboard) lands in Waves R3/R4 — R1 ships the daemon-side
// fan-out wiring that those Waves consume.
//
// Refs #504 Wave R1.
package main

import (
	"bufio"
	"io"
	"log"
	"os/exec"

	"github.com/chepherd/chepherd/internal/ptyhost/agentcatalog"
)

// runAgentAndPump exec's the agent flavor + streams its output to the
// daemon via dc.SendAudit. Blocks until the agent exits.
//
// Errors are logged (not fatal) — Wave R1 stays robust against an
// unknown agent slug or missing binary; the runner just doesn't pump.
// Subsequent Waves can hard-fail or restart depending on operator
// policy.
func runAgentAndPump(cfg *runnerConfig, dc *daemonClient) {
	agent, err := agentcatalog.Lookup(cfg.agentSlug)
	if err != nil {
		log.Printf("[chepherd-runner] agentcatalog.Lookup %q: %v — skipping agent spawn", cfg.agentSlug, err)
		return
	}
	argv := append([]string{}, agent.DefaultArgs...)
	if len(cfg.agentArgs) > 0 {
		argv = cfg.agentArgs
	}
	cmd := exec.Command(agent.Binary, argv...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[chepherd-runner] StdoutPipe: %v", err)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("[chepherd-runner] StderrPipe: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[chepherd-runner] start %s: %v", agent.Binary, err)
		return
	}
	log.Printf("[chepherd-runner] agent started: pid=%d binary=%s argv=%v", cmd.Process.Pid, agent.Binary, argv)
	if dc != nil {
		_ = dc.SendAudit("event", "[chepherd-runner] agent started")
	}

	go pumpStream("stdout", stdout, dc)
	go pumpStream("stderr", stderr, dc)

	if err := cmd.Wait(); err != nil {
		log.Printf("[chepherd-runner] agent exited: %v", err)
	} else {
		log.Printf("[chepherd-runner] agent exited cleanly")
	}
	if dc != nil {
		_ = dc.SendAudit("event", "[chepherd-runner] agent exited")
	}
}

// pumpStream reads lines from r and forwards each as a pty_output
// audit event. Line-by-line for human readability; raw-chunk
// streaming would lose word-wrap context the dashboard cares about.
func pumpStream(streamName string, r io.Reader, dc *daemonClient) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if dc != nil {
			_ = dc.SendAudit("pty_output", line)
		}
	}
}
