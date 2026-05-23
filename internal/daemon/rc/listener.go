// Package rc wires the chepherd daemon to the chepherd-rc remote-control
// system. The Listener struct reads operator intent from
// ~/.config/chepherd/rc.toml, opens the configured transport, accepts
// incoming peer connections, and multiplexes the daemon's events
// (state snapshots, log lines, verdicts) to all currently-connected peers
// per protocol v1.
//
// One Listener instance per daemon process. Owned by the daemon's main
// loop and Run() is called in its own goroutine.
package rc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chepherd/chepherd/internal/daemon/rc/envelope"
	"github.com/chepherd/chepherd/internal/daemon/rc/signaling"
	"github.com/chepherd/chepherd/internal/daemon/rc/transport"
)

// Config — the parsed form of ~/.config/chepherd/rc.toml.
type Config struct {
	Enabled     bool
	RelayURL    string
	Mode        string // "privacy" | "relayed"
	SelfSignal  bool
	BastionID   string
	AuthToken   string
	STUNServers []string
}

// DefaultConfigPath returns ~/.config/chepherd/rc.toml.
func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "chepherd", "rc.toml")
}

// LoadConfig reads rc.toml if present. Returns Enabled=false when missing.
// The file is small + simple (key=value); we parse it manually to avoid
// pulling in a TOML library for one config file.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{Enabled: false}, nil
		}
		return nil, err
	}
	cfg := &Config{Enabled: true, Mode: "privacy", RelayURL: "https://rc.openova.io"}
	for _, line := range splitLines(string(b)) {
		k, v, ok := splitKV(line)
		if !ok {
			continue
		}
		switch k {
		case "relay":
			cfg.RelayURL = trimQuotes(v)
		case "mode":
			cfg.Mode = trimQuotes(v)
		case "self_signal":
			cfg.SelfSignal = (trimQuotes(v) == "true")
		case "bastion_id":
			cfg.BastionID = trimQuotes(v)
		case "auth_token":
			cfg.AuthToken = trimQuotes(v)
		}
	}
	if cfg.BastionID == "" {
		// Default to hostname.
		hn, _ := os.Hostname()
		cfg.BastionID = hn
	}
	return cfg, nil
}

// Listener owns the rc transport(s) + multiplexes events to connected peers.
type Listener struct {
	cfg     *Config
	factory transport.Factory

	mu          sync.Mutex
	peers       map[*peerSession]struct{}
	stateSnap   atomic.Pointer[envelope.StatePayload] // most recent state snapshot
	startedAt   time.Time
	totalPeers  atomic.Int64 // cumulative count across the listener's lifetime
	handler     CommandHandler
}

type peerSession struct {
	tr       transport.Transport
	sendSeq  envelope.SequenceCounter
	closed   chan struct{}
}

// New constructs a Listener from config. Returns (nil, nil) when rc is disabled.
func New(cfg *Config) (*Listener, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}
	if cfg.BastionID == "" {
		return nil, errors.New("rc: BastionID required (set bastion_id in rc.toml)")
	}

	l := &Listener{
		cfg:       cfg,
		peers:     map[*peerSession]struct{}{},
		startedAt: time.Now().UTC(),
	}

	switch cfg.Mode {
	case "privacy", "":
		l.factory = &transport.WebRTCFactory{
			STUNServers: cfg.STUNServers,
			Signaling:   signaling.New(cfg.RelayURL+"/v1/signaling", cfg.AuthToken, cfg.BastionID),
		}
	case "relayed":
		l.factory = &transport.WSFactory{
			RelayURL: cfg.RelayURL + "/v1/ws",
			Token:    cfg.AuthToken,
		}
	default:
		return nil, fmt.Errorf("rc: unknown mode %q (want privacy or relayed)", cfg.Mode)
	}
	return l, nil
}

// Run starts the accept loop. Blocks until ctx is cancelled.
func (l *Listener) Run(ctx context.Context) error {
	if l == nil {
		return nil // rc disabled
	}
	defer l.factory.Close()

	return l.factory.Listen(ctx, func(tr transport.Transport) {
		go l.handlePeer(ctx, tr)
	})
}

// PublishState pushes a new state snapshot to all connected peers.
// Called by the daemon's main tick loop whenever sessions change.
func (l *Listener) PublishState(snap *envelope.StatePayload) {
	if l == nil {
		return
	}
	l.stateSnap.Store(snap)
	l.fanOut(envelope.TypeState, snap)
}

// PublishLog pushes one log line to all connected peers.
func (l *Listener) PublishLog(session, level, text string) {
	if l == nil {
		return
	}
	l.fanOut(envelope.TypeLog, envelope.LogPayload{
		Session: session,
		Level:   level,
		Text:    text,
	})
}

// PublishVerdict pushes a fresh verdict to all peers.
func (l *Listener) PublishVerdict(p envelope.VerdictPayload) {
	if l == nil {
		return
	}
	l.fanOut(envelope.TypeVerdict, p)
}

// PeerCount reports connected peers — used by `chepherd rc status` + the
// TUI's "rc: connected · N clients" status line.
func (l *Listener) PeerCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.peers)
}

// TotalPeers reports the cumulative count of peers ever-connected over this
// Listener's lifetime — useful for ops counters.
func (l *Listener) TotalPeers() int64 {
	if l == nil {
		return 0
	}
	return l.totalPeers.Load()
}

// ─── peer session handler ───────────────────────────────────────────────

func (l *Listener) handlePeer(ctx context.Context, tr transport.Transport) {
	defer tr.Close()
	sess := &peerSession{
		tr:     tr,
		closed: make(chan struct{}),
	}
	l.mu.Lock()
	l.peers[sess] = struct{}{}
	l.mu.Unlock()
	l.totalPeers.Add(1)
	defer func() {
		l.mu.Lock()
		delete(l.peers, sess)
		l.mu.Unlock()
		close(sess.closed)
	}()

	// 1. On connect, push the latest state snapshot so the peer is current.
	if snap := l.stateSnap.Load(); snap != nil {
		l.sendTo(sess, envelope.TypeState, snap)
	}

	// 2. Start ping/pong heartbeat (protocol §4.ping).
	go l.heartbeatLoop(ctx, sess)

	// 3. Read peer commands until the connection closes.
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.closed:
			return
		default:
		}
		recvCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		frame, err := tr.Recv(recvCtx)
		cancel()
		if err != nil {
			return
		}
		if verr := envelope.ValidateFrame(frame); verr != nil {
			continue // drop malformed
		}
		env, err := envelope.Decode(frame)
		if err != nil {
			continue
		}
		l.handleCommand(ctx, sess, env)
	}
}

func (l *Listener) heartbeatLoop(ctx context.Context, sess *peerSession) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.closed:
			return
		case <-t.C:
			l.sendTo(sess, envelope.TypePing, envelope.PingPayload{})
		}
	}
}

// handleCommand dispatches one incoming envelope from a peer.
func (l *Listener) handleCommand(ctx context.Context, sess *peerSession, env *envelope.Envelope) {
	switch env.Type {
	case envelope.TypePing:
		l.sendTo(sess, envelope.TypePong, envelope.PongPayload{InReplyTo: env.Seq})
	case envelope.TypePong:
		// No-op for now; future: track liveness windows.
	case envelope.TypeCommand:
		var cmd envelope.CommandPayload
		if err := env.DecodePayload(&cmd); err != nil {
			l.sendTo(sess, envelope.TypeAck, envelope.AckPayload{
				InReplyTo: env.Seq,
				OK:        false,
				Error:     "malformed command payload",
			})
			return
		}
		l.executeCommand(ctx, sess, env.Seq, &cmd)
	default:
		// Unknown type — ignore per protocol §3 forward-compatibility.
	}
}

// CommandHandler is the daemon-supplied interface that performs the
// pause/unpause/inject/refresh actions on a real session. Allows the
// Listener to call into the daemon's existing primitives without a
// circular import. Wired in cmd/shadow.go via Listener.SetHandler.
type CommandHandler interface {
	Pause(sessionUUID string) error
	Unpause(sessionUUID string) error
	Refresh(sessionUUID string) error
	Inject(sessionUUID, message string) error
}

// SetHandler wires the CommandHandler. Safe to call before or after Run.
func (l *Listener) SetHandler(h CommandHandler) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.handler = h
	l.mu.Unlock()
}

// executeCommand runs the requested action against the configured handler.
func (l *Listener) executeCommand(ctx context.Context, sess *peerSession, replyTo uint64, cmd *envelope.CommandPayload) {
	l.mu.Lock()
	h := l.handler
	l.mu.Unlock()

	var err error
	var result string

	switch cmd.Action {
	case "pause":
		if h != nil {
			err = h.Pause(cmd.SessionUUID)
		}
		result = "paused"
	case "unpause":
		if h != nil {
			err = h.Unpause(cmd.SessionUUID)
		}
		result = "unpaused"
	case "refresh":
		if h != nil {
			err = h.Refresh(cmd.SessionUUID)
		}
		result = "refresh queued"
	case "inject":
		msg, _ := cmd.Args["message"].(string)
		if msg == "" {
			l.sendTo(sess, envelope.TypeAck, envelope.AckPayload{
				InReplyTo: replyTo, OK: false,
				Error: "inject requires args.message",
			})
			return
		}
		if h != nil {
			err = h.Inject(cmd.SessionUUID, msg)
		}
		result = "injected"
	case "tmux_attach_hint":
		// Informational; no daemon action.
		l.sendTo(sess, envelope.TypeAck, envelope.AckPayload{
			InReplyTo: replyTo, OK: true, Result: "noted",
		})
		return
	default:
		l.sendTo(sess, envelope.TypeAck, envelope.AckPayload{
			InReplyTo: replyTo, OK: false,
			Error: fmt.Sprintf("unknown action %q", cmd.Action),
		})
		return
	}

	if h == nil {
		l.sendTo(sess, envelope.TypeAck, envelope.AckPayload{
			InReplyTo: replyTo, OK: false,
			Error: "no command handler configured (daemon must SetHandler)",
		})
		return
	}
	if err != nil {
		l.sendTo(sess, envelope.TypeAck, envelope.AckPayload{
			InReplyTo: replyTo, OK: false,
			Error: err.Error(),
		})
		return
	}
	l.sendTo(sess, envelope.TypeAck, envelope.AckPayload{
		InReplyTo: replyTo, OK: true, Result: result,
	})
}

// fanOut sends the same payload to every connected peer.
func (l *Listener) fanOut(t envelope.Type, payload any) {
	l.mu.Lock()
	sessions := make([]*peerSession, 0, len(l.peers))
	for s := range l.peers {
		sessions = append(sessions, s)
	}
	l.mu.Unlock()
	for _, s := range sessions {
		l.sendTo(s, t, payload)
	}
}

// sendTo emits one envelope to one peer.
func (l *Listener) sendTo(sess *peerSession, t envelope.Type, payload any) {
	env, err := envelope.New(t, payload, sess.sendSeq.Atomic())
	if err != nil {
		return
	}
	frame, err := env.Marshal()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = sess.tr.Send(ctx, frame) // backpressure errors are tolerated — peer falls behind
}

// ─── tiny TOML-ish parser (deliberately not pulling in a library) ───────

func splitLines(s string) []string {
	var out []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func splitKV(line string) (k, v string, ok bool) {
	// Skip comments + empty.
	trimmed := trimLeft(line, " \t")
	if trimmed == "" || trimmed[0] == '#' {
		return "", "", false
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] == '=' {
			return trim(trimmed[:i]), trim(trimmed[i+1:]), true
		}
	}
	return "", "", false
}

func trimLeft(s, cutset string) string {
	for len(s) > 0 && contains(cutset, s[0]) {
		s = s[1:]
	}
	return s
}

func trim(s string) string {
	const ws = " \t\r\n"
	for len(s) > 0 && contains(ws, s[0]) {
		s = s[1:]
	}
	for len(s) > 0 && contains(ws, s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}

func contains(cs string, c byte) bool {
	for i := 0; i < len(cs); i++ {
		if cs[i] == c {
			return true
		}
	}
	return false
}

func trimQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
