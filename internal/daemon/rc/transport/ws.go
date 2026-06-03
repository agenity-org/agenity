package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// WSTransport implements Transport over a single WebSocket connection
// (one peer). Used in the WS-relay mode (data plane visible to relay)
// when WebRTC P2P establishment fails or the user opted in.
//
// Per protocol v1 §1: connection auths via Bearer token in the WS
// handshake; subprotocol negotiated as `chepherd.v1.ws`.
type WSTransport struct {
	conn        *websocket.Conn
	mode        Mode
	peerID      string
	sendBuffer  chan []byte
	closeOnce   sync.Once
	closed      chan struct{}

	// observability
	framesSent     atomic.Uint64
	framesReceived atomic.Uint64
	bytesSent      atomic.Uint64
	bytesReceived  atomic.Uint64
	lastActivity   atomic.Int64
	reconnects     atomic.Int64
}

// SendBufferSize per protocol §7 backpressure budget — when this fills,
// Send returns ErrBackpressure and the caller must apply drop-oldest.
const SendBufferSize = 256

// NewWSTransport wraps an already-handshaken websocket.Conn into a Transport.
// The handshake itself happens in WSFactory.Dial/Listen.
func NewWSTransport(conn *websocket.Conn, peerID string) *WSTransport {
	t := &WSTransport{
		conn:       conn,
		mode:       ModeWS,
		peerID:     peerID,
		sendBuffer: make(chan []byte, SendBufferSize),
		closed:     make(chan struct{}),
	}
	go t.writeLoop()
	return t
}

// Mode returns ModeWS.
func (t *WSTransport) Mode() Mode { return t.mode }

// Send pushes one frame into the send queue. Non-blocking; returns
// ErrBackpressure when the queue is full.
func (t *WSTransport) Send(ctx context.Context, frame []byte) error {
	select {
	case <-t.closed:
		return ErrClosed
	default:
	}
	select {
	case t.sendBuffer <- frame:
		return nil
	default:
		return ErrBackpressure
	}
}

// writeLoop drains sendBuffer + writes each frame as a single WS text message.
// Exits when closed is signalled OR the conn errors fatally.
func (t *WSTransport) writeLoop() {
	for {
		select {
		case <-t.closed:
			return
		case frame, ok := <-t.sendBuffer:
			if !ok {
				return
			}
			// #688 — bump the send counters BEFORE the write so stats are
			// causally consistent: an echoed receive can never be observed
			// while its own send's counter still reads 0. With the bump
			// AFTER the write, the peer could echo + a Stats() reader run
			// while this goroutine sat descheduled between Write returning
			// and the Add (CI flake: FramesSent:0 despite FramesReceived:1
			// — a state that misleads production dashboards the same way).
			// On write error the transport closes immediately, so the
			// one-frame overcount on the final failed write is moot.
			t.framesSent.Add(1)
			t.bytesSent.Add(uint64(len(frame)))
			t.lastActivity.Store(time.Now().UnixNano())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := t.conn.Write(ctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				_ = t.Close()
				return
			}
		}
	}
}

// Recv blocks until the next text frame arrives, or ctx/conn closes.
func (t *WSTransport) Recv(ctx context.Context) ([]byte, error) {
	select {
	case <-t.closed:
		return nil, ErrClosed
	default:
	}
	msgType, frame, err := t.conn.Read(ctx)
	if err != nil {
		if isClosed(err) {
			_ = t.Close()
			return nil, ErrClosed
		}
		return nil, fmt.Errorf("ws read: %w", err)
	}
	if msgType != websocket.MessageText {
		return nil, fmt.Errorf("ws: unexpected non-text frame %v", msgType)
	}
	t.framesReceived.Add(1)
	t.bytesReceived.Add(uint64(len(frame)))
	t.lastActivity.Store(time.Now().UnixNano())
	return frame, nil
}

// Close terminates the WS connection + signals the writeLoop to exit.
// Safe to call multiple times.
func (t *WSTransport) Close() error {
	var closeErr error
	t.closeOnce.Do(func() {
		close(t.closed)
		closeErr = t.conn.Close(websocket.StatusNormalClosure, "chepherd: bye")
	})
	return closeErr
}

// Stats snapshot.
func (t *WSTransport) Stats() Stats {
	return Stats{
		Mode:            t.mode,
		Connected:       !t.isClosedNow(),
		PeerID:          t.peerID,
		FramesSent:      t.framesSent.Load(),
		FramesReceived:  t.framesReceived.Load(),
		BytesSent:       t.bytesSent.Load(),
		BytesReceived:   t.bytesReceived.Load(),
		LastActivity:    t.lastActivity.Load(),
		SendBufferDepth: len(t.sendBuffer),
		Reconnects:      int(t.reconnects.Load()),
	}
}

func (t *WSTransport) isClosedNow() bool {
	select {
	case <-t.closed:
		return true
	default:
		return false
	}
}

func isClosed(err error) bool {
	if err == nil {
		return false
	}
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return false
}

// ─── WSFactory ──────────────────────────────────────────────────────────

// WSFactory builds WSTransports by dialing the relay's WSS endpoint with a
// Bearer auth token.
//
// Wire (chepherd-relay@ace425c):
//   URL          wss://relay.chepherd.org/v1/signaling/ws
//                   ?role=client|daemon
//                   &bastion_id=<id>
//                   [&client_id=<id>]
//   Subprotocol  chepherd-rc-v1
//   Auth         Authorization: Bearer <jwt> (via the relay's middleware)
type WSFactory struct {
	// RelayURL — base WSS endpoint on the relay. The factory appends
	// the role + bastion_id query string automatically.
	RelayURL string
	// Token — OAuth2 bearer (user token for client-side, daemon token
	// for daemon-side).
	Token string
	// BastionID — the room key on the relay. Required for both sides.
	BastionID string
}

// Mode reports ModeWS.
func (f *WSFactory) Mode() Mode { return ModeWS }

// Dial opens a WSS connection as role=client + returns a Transport.
// peerID is the target bastion_id (overrides f.BastionID for the dial).
func (f *WSFactory) Dial(ctx context.Context, peerID string) (Transport, error) {
	target := peerID
	if target == "" {
		target = f.BastionID
	}
	url := withWSQuery(f.RelayURL, "client", target, "")
	conn, err := f.dialWS(ctx, url)
	if err != nil {
		return nil, err
	}
	return NewWSTransport(conn, target), nil
}

// Listen — daemon side: opens ONE long-lived WS connection as role=daemon
// to the relay. Every frame the relay forwards from any connected client
// appears on this single Transport (the wsrelay broadcasts daemon→clients
// and fans clients→daemon in v0.2). onPeer is called exactly once with the
// Transport; subsequent client connections are multiplexed onto it. When
// the WS closes for any reason, Listen returns and the caller may retry.
func (f *WSFactory) Listen(ctx context.Context, onPeer func(Transport)) error {
	if f.BastionID == "" {
		return errors.New("ws: BastionID required for Listen")
	}
	url := withWSQuery(f.RelayURL, "daemon", f.BastionID, "")
	conn, err := f.dialWS(ctx, url)
	if err != nil {
		return err
	}
	t := NewWSTransport(conn, "relay")
	onPeer(t)
	// Block until the ctx is done OR the Transport closes.
	<-ctx.Done()
	_ = t.Close()
	return ctx.Err()
}

// dialWS performs the actual websocket.Dial with the canonical subprotocol
// and auth header. Errors mapped to ErrUnauthorized for 401 responses.
func (f *WSFactory) dialWS(ctx context.Context, url string) (*websocket.Conn, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+f.Token)
	opts := &websocket.DialOptions{
		HTTPHeader:   header,
		Subprotocols: []string{"chepherd-rc-v1"},
	}
	conn, resp, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("ws dial %s: %w", url, err)
	}
	if conn.Subprotocol() != "chepherd-rc-v1" {
		conn.Close(websocket.StatusProtocolError, "subprotocol mismatch")
		return nil, fmt.Errorf("ws: relay refused subprotocol chepherd-rc-v1 (got %q)", conn.Subprotocol())
	}
	conn.SetReadLimit(int64(2 * 256 * 1024)) // 2x frame limit headroom
	return conn, nil
}

func withWSQuery(base, role, bastionID, clientID string) string {
	u := base
	if !strings.Contains(u, "?") {
		u += "?"
	} else {
		u += "&"
	}
	u += "role=" + role + "&bastion_id=" + bastionID
	if clientID != "" {
		u += "&client_id=" + clientID
	}
	return u
}

// Close releases factory-level resources (none for WS).
func (f *WSFactory) Close() error { return nil }
