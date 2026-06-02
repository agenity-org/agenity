// internal/webrtcrtc/hub_signaler_test.go — #672 hub-relayed WebRTC A2A
// (epic). Pins HubSignaler's offer→poll→answer flow against an
// in-process hub queue + the non-trickle gathered-SDP helpers.
package webrtcrtc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// queueHub is the minimal in-process signaling queue (DrainPending on
// read) the real chepherd-hub implements.
type queueHub struct {
	mu     sync.Mutex
	frames map[string][]HubFrame
}

func newQueueHub() *queueHub { return &queueHub{frames: map[string][]HubFrame{}} }

func (h *queueHub) server() *httptest.Server {
	mux := http.NewServeMux()
	post := func(kind string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("X-Chepherd-Org"); got == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var f HubFrame
			_ = json.NewDecoder(r.Body).Decode(&f)
			f.Kind = kind
			h.mu.Lock()
			h.frames[f.ToOrgID] = append(h.frames[f.ToOrgID], f)
			h.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		}
	}
	mux.HandleFunc("/v1/signaling/offer", post("offer"))
	mux.HandleFunc("/v1/signaling/answer", post("answer"))
	mux.HandleFunc("/v1/signaling/ice", post("ice"))
	mux.HandleFunc("/v1/signaling/pending", func(w http.ResponseWriter, r *http.Request) {
		org := r.URL.Query().Get("orgId")
		h.mu.Lock()
		fr := h.frames[org]
		delete(h.frames, org)
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"org": org, "frames": fr, "count": len(fr)})
	})
	return httptest.NewServer(mux)
}

// drainOffersFor returns the offer frames queued for org (consuming).
func (h *queueHub) drainOffersFor(org string) []HubFrame {
	h.mu.Lock()
	defer h.mu.Unlock()
	fr := h.frames[org]
	delete(h.frames, org)
	return fr
}

func (h *queueHub) enqueue(f HubFrame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frames[f.ToOrgID] = append(h.frames[f.ToOrgID], f)
}

func TestHubSignaler_ExchangeOffer_PollsForAnswer(t *testing.T) {
	t.Parallel()
	hub := newQueueHub()
	srv := hub.server()
	defer srv.Close()

	sig := NewHubSignaler(srv.URL, "org-a", srv.Client())
	sig.PollInterval = 20 * time.Millisecond

	// Simulated answerer: watch org-b's mailbox, echo an answer with the
	// SAME sessionId so the offerer's poll loop correlates it.
	const wantAnswerSDP = "v=0\r\no=- answer 0 IN IP4 0.0.0.0\r\n"
	go func() {
		for {
			offers := hub.drainOffersFor("org-b")
			for _, o := range offers {
				if o.Kind != "offer" {
					continue
				}
				payload, _ := json.Marshal(webrtc.SessionDescription{
					Type: webrtc.SDPTypeAnswer, SDP: wantAnswerSDP,
				})
				hub.enqueue(HubFrame{
					Kind: "answer", FromOrgID: "org-b", ToOrgID: "org-a",
					SessionID: o.SessionID, Payload: payload,
				})
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	answer, err := sig.ExchangeOffer(ctx, HubPeerScheme+"org-b",
		webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "v=0\r\no=- offer 0 IN IP4 0.0.0.0\r\n"})
	if err != nil {
		t.Fatalf("ExchangeOffer: %v", err)
	}
	if answer.Type != webrtc.SDPTypeAnswer || answer.SDP != wantAnswerSDP {
		t.Fatalf("answer = %+v, want answer SDP %q", answer, wantAnswerSDP)
	}
}

func TestHubSignaler_RejectsNonHubPeer(t *testing.T) {
	t.Parallel()
	sig := NewHubSignaler("http://hub", "org-a", http.DefaultClient)
	_, err := sig.ExchangeOffer(context.Background(), "https://peer.example.com",
		webrtc.SessionDescription{})
	if err == nil || !strings.Contains(err.Error(), "not a hub peer") {
		t.Fatalf("err = %v, want 'not a hub peer'", err)
	}
}

func TestHubSignaler_ExchangeOffer_ContextDeadline(t *testing.T) {
	t.Parallel()
	hub := newQueueHub()
	srv := hub.server()
	defer srv.Close()
	sig := NewHubSignaler(srv.URL, "org-a", srv.Client())
	sig.PollInterval = 10 * time.Millisecond
	// No answerer ever responds → ExchangeOffer must honor the deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := sig.ExchangeOffer(ctx, HubPeerScheme+"org-b",
		webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "x"})
	if err == nil {
		t.Fatal("expected deadline error, got nil")
	}
}

func TestCreateOfferGathered_BundlesCandidates(t *testing.T) {
	t.Parallel()
	pc, err := NewPeerConnection(Config{ICEServers: nil})
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	defer pc.Close()
	offer, err := pc.CreateOfferGathered()
	if err != nil {
		t.Fatalf("CreateOfferGathered: %v", err)
	}
	// Non-trickle: the gathered offer SDP must already carry at least one
	// host candidate inline (a=candidate), so the answerer can complete
	// the connection without any follow-up ICE frames.
	if !strings.Contains(offer.SDP, "a=candidate") {
		t.Fatalf("gathered offer SDP carries no a=candidate line:\n%s", offer.SDP)
	}
}
