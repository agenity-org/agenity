// internal/webrtcrtc/pcstore.go — #492 Wave F2 per-session PeerConnection
// cache. F1 #488 ships fresh-per-request answerer PeerConnections via
// HandleOffer; F2 introduces a durable PCStore so multiple SendMessage
// calls to the same peer reuse the open DTLS+DataChannel link instead
// of re-negotiating from scratch.
//
// Architecture per V0.9.2-ARCHITECTURE.md §S5 + §20:
//
//   Caller-side (this PCStore lives here):
//     - GetOrDial(peerURL) returns a connected, OPEN-DataChannel PC.
//     - On first call: NewPeerConnection (offering role) + dial via
//       DefaultHTTPSignaler against peerURL/webrtc/offer + wait for
//       DataChannel open + cache.
//     - Subsequent calls: return the cached PC.
//     - Health check: ConnectionState=Connected + DataChannel
//       ReadyState=Open. Failed or Closed states drop the cache
//       entry + re-dial.
//
// Each PeerConnection holds its own goroutine + DTLS link; PCStore
// is the lifecycle owner that closes them on shutdown.
//
// Refs #492 V0.9.2-ARCHITECTURE.md §S5 §20.
package webrtcrtc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// PCStore caches PeerConnections keyed by peer URL. Safe for concurrent
// use. Construct via NewPCStore; close via CloseAll on shutdown.
type PCStore struct {
	cfg      Config
	signaler Signaler

	// GatherBeforeOffer, when true, makes dial() block on full ICE
	// gathering before handing the offer to the signaler (non-trickle,
	// bundled candidates). Required for the #672 hub-relay path where
	// the hub's /v1/signaling/ice receiver is scaffold-only and trickle
	// can't be relied on. The direct-HTTP path leaves this false to
	// preserve its (lower-latency) trickle behavior.
	GatherBeforeOffer bool

	mu    sync.Mutex
	conns map[string]*PeerConnection
}

// NewPCStore constructs an empty store. cfg is forwarded to every
// NewPeerConnection call; signaler is the SDP/ICE relay (default
// DefaultHTTPSignaler when nil).
func NewPCStore(cfg Config, signaler Signaler) *PCStore {
	if signaler == nil {
		signaler = NewDefaultHTTPSignaler()
	}
	return &PCStore{
		cfg:      cfg,
		signaler: signaler,
		conns:    map[string]*PeerConnection{},
	}
}

// GetOrDial returns a connected PeerConnection for peerURL. If the
// store has a healthy cached PC, returns it; otherwise negotiates a
// fresh one via the signaler and caches the result.
//
// "Healthy" means ConnectionState == Connected AND DataChannel
// ReadyState == Open. Any other state triggers re-dial.
//
// dialTimeout caps both the SDP exchange + the wait for DataChannel
// open. Pass zero for the default 8s.
func (s *PCStore) GetOrDial(ctx context.Context, peerURL string, dialTimeout time.Duration) (*PeerConnection, error) {
	if peerURL == "" {
		return nil, errors.New("PCStore.GetOrDial: empty peerURL")
	}
	if dialTimeout <= 0 {
		dialTimeout = 8 * time.Second
	}

	// Fast path: return cached if healthy.
	s.mu.Lock()
	if pc, ok := s.conns[peerURL]; ok {
		if pc.isHealthy() {
			s.mu.Unlock()
			return pc, nil
		}
		// Stale — drop + re-dial.
		_ = pc.Close()
		delete(s.conns, peerURL)
	}
	s.mu.Unlock()

	// Slow path: negotiate.
	pc, err := s.dial(ctx, peerURL, dialTimeout)
	if err != nil {
		return nil, err
	}

	// Re-check the cache under the lock — another caller may have
	// won the race. If so, close our newly-dialed PC and return theirs.
	s.mu.Lock()
	if existing, ok := s.conns[peerURL]; ok && existing.isHealthy() {
		s.mu.Unlock()
		_ = pc.Close()
		return existing, nil
	}
	s.conns[peerURL] = pc
	s.mu.Unlock()
	return pc, nil
}

// dial performs the full SDP exchange + waits for the DataChannel to
// reach Open state. Returns the connected PC.
func (s *PCStore) dial(ctx context.Context, peerURL string, dialTimeout time.Duration) (*PeerConnection, error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	pc, err := NewPeerConnection(s.cfg)
	if err != nil {
		return nil, fmt.Errorf("PCStore.dial: NewPeerConnection: %w", err)
	}

	openCh := make(chan struct{}, 1)
	pc.OnOpen(func() {
		select {
		case openCh <- struct{}{}:
		default:
		}
	})

	var offer webrtc.SessionDescription
	if s.GatherBeforeOffer {
		offer, err = pc.CreateOfferGathered()
	} else {
		offer, err = pc.CreateOffer()
	}
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("PCStore.dial: CreateOffer: %w", err)
	}
	answer, err := s.signaler.ExchangeOffer(dialCtx, peerURL, offer)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("PCStore.dial: signaler.ExchangeOffer: %w", err)
	}
	if err := pc.SetRemoteAnswer(answer); err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("PCStore.dial: SetRemoteAnswer: %w", err)
	}

	select {
	case <-openCh:
		return pc, nil
	case <-dialCtx.Done():
		_ = pc.Close()
		return nil, fmt.Errorf("PCStore.dial: DataChannel never opened: %w", dialCtx.Err())
	}
}

// Get returns the cached PC for peerURL, or nil if none. Does NOT
// dial. Used by tests + observers.
func (s *PCStore) Get(peerURL string) *PeerConnection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[peerURL]
}

// Len returns the number of cached PCs.
func (s *PCStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Close drops + closes the PC for peerURL. No-op when no entry.
func (s *PCStore) Close(peerURL string) error {
	s.mu.Lock()
	pc, ok := s.conns[peerURL]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	delete(s.conns, peerURL)
	s.mu.Unlock()
	return pc.Close()
}

// CloseAll closes every cached PC + clears the registry. Called on
// runtime shutdown.
func (s *PCStore) CloseAll() error {
	s.mu.Lock()
	conns := s.conns
	s.conns = map[string]*PeerConnection{}
	s.mu.Unlock()
	var firstErr error
	for _, pc := range conns {
		if err := pc.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// isHealthy reports whether the PC is fully connected + the
// DataChannel is open. Inspects pion state directly so the store
// can re-dial on failure without callers having to know.
func (p *PeerConnection) isHealthy() bool {
	p.mu.Lock()
	pc := p.pc
	ch := p.ch
	p.mu.Unlock()
	if pc == nil || ch == nil {
		return false
	}
	if pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
		return false
	}
	return ch.ReadyState() == webrtc.DataChannelStateOpen
}
