package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := t.conn.Write(ctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				_ = t.Close()
				return
			}
			t.framesSent.Add(1)
			t.bytesSent.Add(uint64(len(frame)))
			t.lastActivity.Store(time.Now().UnixNano())
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
type WSFactory struct {
	RelayURL string // e.g. "wss://rc.openova.io/v1/ws"
	Token    string // OAuth2 bearer
}

// Mode reports ModeWS.
func (f *WSFactory) Mode() Mode { return ModeWS }

// Dial opens a WSS connection to the relay + returns a Transport.
func (f *WSFactory) Dial(ctx context.Context, peerID string) (Transport, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+f.Token)
	opts := &websocket.DialOptions{
		HTTPHeader:   header,
		Subprotocols: []string{"chepherd.v1.ws"},
	}
	conn, resp, err := websocket.Dial(ctx, f.RelayURL, opts)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("ws dial %s: %w", f.RelayURL, err)
	}
	if conn.Subprotocol() != "chepherd.v1.ws" {
		conn.Close(websocket.StatusProtocolError, "subprotocol mismatch")
		return nil, fmt.Errorf("ws: relay refused subprotocol chepherd.v1.ws")
	}
	conn.SetReadLimit(int64(2 * 256 * 1024)) // 2x frame limit headroom
	return NewWSTransport(conn, peerID), nil
}

// Listen — server-side accept loop (not used by client-side daemons that
// only Dial). Future: the chepherd-relay service implements this side.
func (f *WSFactory) Listen(ctx context.Context, onPeer func(Transport)) error {
	return errors.New("ws: server-side Listen not implemented in client SDK; " +
		"chepherd-relay implements this side")
}

// Close releases factory-level resources (none for WS).
func (f *WSFactory) Close() error { return nil }
