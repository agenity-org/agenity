// chepherd-relay — signaling + auth + push proxy for chepherd-rc.
//
// See README.md for the privacy contract + endpoint surface.
// See https://github.com/chepherd/chepherd/blob/main/docs/PROTOCOL.md for
// the wire protocol this server implements.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/chepherd/chepherd-relay/internal/auth"
	"github.com/chepherd/chepherd-relay/internal/obs"
	"github.com/chepherd/chepherd-relay/internal/push"
	"github.com/chepherd/chepherd-relay/internal/registry"
	"github.com/chepherd/chepherd-relay/internal/wsrelay"
)

// Version is overridden at build time via -ldflags.
var Version = "0.2.0-rc3"

func main() {
	addr := flag.String("addr", ":9889",
		"HTTP listen address (CAO convention port 9889)")
	jwksURL := flag.String("jwks", os.Getenv("CHEPHERD_RELAY_JWKS"),
		"identity-svc JWKS endpoint (also: CHEPHERD_RELAY_JWKS env)")
	issuer := flag.String("issuer", os.Getenv("CHEPHERD_RELAY_ISSUER"),
		"expected JWT issuer (also: CHEPHERD_RELAY_ISSUER env)")
	audience := flag.String("audience", "chepherd-rc",
		"expected JWT audience")
	flag.Parse()

	bootCtx, bootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer bootCancel()
	obsShutdown, err := obs.Init(bootCtx, obs.FromEnv("chepherd-relay", Version))
	if err != nil {
		log.Fatalf("obs init: %v", err)
	}

	srv := newRelay()

	// Auth verifier — required for signaling endpoints; bypassed for health.
	var verifier *auth.Verifier
	if *jwksURL != "" {
		verifier = auth.New(*jwksURL, *issuer, *audience)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", srv.health)
	mux.HandleFunc("/v1/stats", srv.stats)

	// Protected endpoints — wrap with auth middleware when verifier is set.
	protect := func(h http.HandlerFunc) http.HandlerFunc {
		if verifier == nil {
			// Dev mode without JWKS — allow all (logs a warning).
			return h
		}
		wrapped := verifier.Middleware(http.HandlerFunc(h))
		return wrapped.ServeHTTP
	}
	mux.HandleFunc("/v1/signaling/initiate", protect(srv.signalingInitiate))
	mux.HandleFunc("/v1/signaling/poll", protect(srv.signalingPoll))
	mux.HandleFunc("/v1/signaling/answer", protect(srv.signalingAnswer))
	// REST-style aliases used by the modern chepherd-rc-web + iOS + Android
	// clients (trickled-ICE friendly). The initiate/poll/answer trio is
	// kept around for backward compatibility with older daemons.
	mux.HandleFunc("/v1/signaling/offer", protect(srv.signalingOffer))
	mux.HandleFunc("/v1/signaling/candidate", protect(srv.signalingPostCandidate))
	mux.HandleFunc("/v1/signaling/candidates", protect(srv.signalingPollCandidates))
	mux.HandleFunc("/v1/register", protect(srv.registerBastion))
	mux.HandleFunc("/v1/bastions", protect(srv.listMyBastions))
	mux.HandleFunc("/v1/push/register", protect(srv.pushRegister))
	mux.HandleFunc("/v1/push/send", protect(srv.pushSend))
	// Relayed-mode data plane — clients + daemons WebSocket here when
	// WebRTC isn't an option (symmetric NAT, corporate proxies). The
	// relay forwards every frame VERBATIM — privacy trade-off is
	// explicit per protocol §1.
	mux.HandleFunc("/v1/signaling/ws", protect(srv.wsHub.HandleHTTP))

	if verifier == nil {
		log.Println("WARNING: --jwks not set; auth bypassed (dev mode only).")
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           obs.Middleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	go func() {
		log.Printf("chepherd-relay %s listening on %s", Version, *addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
	if obsShutdown != nil {
		_ = obsShutdown(shutCtx)
	}
}

// ─── relay core ─────────────────────────────────────────────────────────

type relay struct {
	mu sync.Mutex
	// pendingOffers: bastion_id → queue of waiting offers from clients
	pendingOffers map[string]chan *signalEnvelope
	// pendingAnswers: client_id → waiting answer from bastion
	pendingAnswers map[string]chan *signalEnvelope
	// candidateQueues: peer_id → trickled-ICE candidate fan-in queue
	candidateQueues map[string]chan json.RawMessage
	// startedAt
	startedAt time.Time
	// registry tracks which bastions are registered + their daemon tokens
	registry registry.Registry
	// pushTokens tracks per-user device tokens for APNs/FCM dispatch
	pushTokens push.TokenRegistry
	// pushDispatcher routes a Notification to the right backend
	pushDispatcher push.Dispatcher
	// wsHub multiplexes relayed-mode WS clients ↔ daemons by bastion_id
	wsHub *wsrelay.Hub
}

func newRelay() *relay {
	return &relay{
		pendingOffers:   map[string]chan *signalEnvelope{},
		pendingAnswers:  map[string]chan *signalEnvelope{},
		candidateQueues: map[string]chan json.RawMessage{},
		startedAt:       time.Now().UTC(),
		registry:        registry.NewMemory(),
		pushTokens:      push.NewMemory(),
		pushDispatcher:  push.FromEnv(context.Background()),
		wsHub:           wsrelay.New(),
	}
}

// pushRegister upserts a device push token for the authenticated user.
// POST /v1/push/register
// Body: {device_id, platform, value, bundle_id?}
func (r *relay) pushRegister(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	claims := auth.ClaimsFromContext(req.Context())
	if claims == nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	var body struct {
		DeviceID string `json:"device_id"`
		Platform string `json:"platform"` // "ios" | "android" | "webpush"
		Value    string `json:"value"`
		BundleID string `json:"bundle_id"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.DeviceID == "" || body.Value == "" {
		http.Error(w, "device_id and value required", http.StatusBadRequest)
		return
	}
	var plat push.Platform
	switch body.Platform {
	case "ios":
		plat = push.PlatformiOS
	case "android":
		plat = push.PlatformAndroid
	case "webpush":
		plat = push.PlatformWebPush
	default:
		http.Error(w, "invalid platform", http.StatusBadRequest)
		return
	}
	if err := r.pushTokens.Upsert(req.Context(), push.Registration{
		UserID:   claims.UserID,
		DeviceID: body.DeviceID,
		Token: push.Token{
			Platform: plat,
			Value:    body.Value,
			BundleID: body.BundleID,
		},
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// pushSend dispatches a notification to all the caller-user's devices.
// POST /v1/push/send
// Body: {title, body, sound?, priority?, session_uuid, collapse_key?, ttl_seconds?}
// Caller is the daemon (authenticated via its daemon token) — its claims
// carry the operator user_id, so we send to THAT user's devices.
func (r *relay) pushSend(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	claims := auth.ClaimsFromContext(req.Context())
	if claims == nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	var body struct {
		Title       string `json:"title"`
		Body        string `json:"body"`
		Sound       string `json:"sound"`
		Priority    string `json:"priority"` // "normal" | "high"
		SessionUUID string `json:"session_uuid"`
		CollapseKey string `json:"collapse_key"`
		TTLSeconds  int    `json:"ttl_seconds"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	regs, err := r.pushTokens.ListByUser(req.Context(), claims.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var prio push.Priority
	if body.Priority == "high" {
		prio = push.PriorityHigh
	}
	notif := push.Notification{
		Title:       body.Title,
		Body:        body.Body,
		Sound:       body.Sound,
		Priority:    prio,
		SessionUUID: body.SessionUUID,
		CollapseKey: body.CollapseKey,
		TTL:         time.Duration(body.TTLSeconds) * time.Second,
	}
	var sent, failed int
	for _, reg := range regs {
		if err := r.pushDispatcher.Send(req.Context(), reg.Token, notif); err != nil {
			failed++
			_ = r.pushTokens.MarkFailure(req.Context(), reg.DeviceID)
			obs.CaptureError(err, map[string]string{
				"device_id": reg.DeviceID,
				"platform":  fmt.Sprintf("%v", reg.Token.Platform),
			})
			continue
		}
		sent++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"devices_attempted": len(regs),
		"devices_sent":      sent,
		"devices_failed":    failed,
	})
}

// registerBastion mints a fresh daemon token for a bastion.
// POST /v1/register
// Authenticated (user token); body: {id, capabilities, chepherd_version}
// Response: {daemon_token, bastion: {...}}
func (r *relay) registerBastion(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	claims := auth.ClaimsFromContext(req.Context())
	if claims == nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	var body struct {
		ID              string   `json:"id"`
		Capabilities    []string `json:"capabilities"`
		ChepherdVersion string   `json:"chepherd_version"`
		Hostname        string   `json:"hostname"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	tok, err := r.registry.Register(req.Context(), registry.Bastion{
		ID:              body.ID,
		UserID:          claims.UserID,
		ChepherdVersion: body.ChepherdVersion,
		Capabilities:    body.Capabilities,
		Hostname:        body.Hostname,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	b, _ := r.registry.Get(req.Context(), body.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"daemon_token": tok,
		"bastion":      b,
	})
}

// listMyBastions returns the bastions owned by the authenticated user.
// GET /v1/bastions
func (r *relay) listMyBastions(w http.ResponseWriter, req *http.Request) {
	claims := auth.ClaimsFromContext(req.Context())
	if claims == nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	bastions, _ := r.registry.ListByUser(req.Context(), claims.UserID)
	writeJSON(w, http.StatusOK, map[string]any{
		"bastions": bastions,
	})
}

// signalEnvelope is the relay's internal pass-through shape. It carries the
// SDP + ICE blob opaquely — the relay never inspects the contents.
type signalEnvelope struct {
	Peer string          `json:"peer"`
	SDP  json.RawMessage `json:"sdp"`
	ICE  json.RawMessage `json:"ice,omitempty"`
}

func (r *relay) health(w http.ResponseWriter, req *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"uptime_sec": int(time.Since(r.startedAt).Seconds()),
		"version":    Version,
	})
}

func (r *relay) stats(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	pendingOffers := len(r.pendingOffers)
	pendingAnswers := len(r.pendingAnswers)
	candidateQueues := len(r.candidateQueues)
	r.mu.Unlock()
	wsStats := r.wsHub.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"uptime_sec":              int(time.Since(r.startedAt).Seconds()),
		"pending_offer_queues":    pendingOffers,
		"pending_answer_channels": pendingAnswers,
		"candidate_queues":        candidateQueues,
		"ws_open_rooms":           wsStats.OpenRooms,
		"ws_rooms_with_daemon":    wsStats.RoomsWithDaemon,
		"ws_open_client_ws":       wsStats.OpenClientWs,
		"version":                 Version,
	})
}

// signalingInitiate: client posts offer + ICE for a named bastion.
// Blocks until the bastion responds with an answer (or 60s timeout).
//
// Note this endpoint does NOT see DataChannel contents — SDP only.
// Per protocol v1 §1 + §8 privacy contract.
func (r *relay) signalingInitiate(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var env signalEnvelope
	if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if env.Peer == "" {
		http.Error(w, "peer (bastion_id) required", http.StatusBadRequest)
		return
	}

	// Create the client-side wait channel.
	clientID := genClientID()
	answerCh := make(chan *signalEnvelope, 1)
	r.mu.Lock()
	r.pendingAnswers[clientID] = answerCh
	// Push the offer to the bastion's poll queue.
	queue, ok := r.pendingOffers[env.Peer]
	if !ok {
		queue = make(chan *signalEnvelope, 16)
		r.pendingOffers[env.Peer] = queue
	}
	r.mu.Unlock()

	env.Peer = clientID // overwrite to identify client to the bastion
	select {
	case queue <- &env:
	case <-time.After(2 * time.Second):
		http.Error(w, "bastion offer queue full or unavailable", http.StatusServiceUnavailable)
		return
	}

	// Wait for the answer.
	select {
	case answer := <-answerCh:
		writeJSON(w, http.StatusOK, answer)
	case <-time.After(60 * time.Second):
		http.Error(w, "bastion did not answer within 60s", http.StatusGatewayTimeout)
	case <-req.Context().Done():
		// client gave up
	}

	r.mu.Lock()
	delete(r.pendingAnswers, clientID)
	r.mu.Unlock()
}

// signalingPoll: bastion long-polls for offers addressed to it.
// Long-poll up to 25 sec (under the typical 30-sec proxy timeout).
func (r *relay) signalingPoll(w http.ResponseWriter, req *http.Request) {
	bastionID := req.URL.Query().Get("bastion")
	if bastionID == "" {
		http.Error(w, "?bastion= required", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	queue, ok := r.pendingOffers[bastionID]
	if !ok {
		queue = make(chan *signalEnvelope, 16)
		r.pendingOffers[bastionID] = queue
	}
	r.mu.Unlock()

	select {
	case env := <-queue:
		writeJSON(w, http.StatusOK, env)
	case <-time.After(25 * time.Second):
		w.WriteHeader(http.StatusNoContent)
	case <-req.Context().Done():
	}
}

// signalingOffer is the REST-style entry used by the modern clients.
// Body: {bastion_id, offer: <RTCSessionDescriptionInit>}.
// Response: {answer: <RTCSessionDescriptionInit>}.
// Internally wraps the existing initiate-poll-answer rendezvous so we
// don't duplicate logic.
func (r *relay) signalingOffer(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		BastionID string          `json:"bastion_id"`
		Offer     json.RawMessage `json:"offer"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.BastionID == "" {
		http.Error(w, "bastion_id required", http.StatusBadRequest)
		return
	}

	clientID := genClientID()
	answerCh := make(chan *signalEnvelope, 1)
	r.mu.Lock()
	r.pendingAnswers[clientID] = answerCh
	queue, ok := r.pendingOffers[body.BastionID]
	if !ok {
		queue = make(chan *signalEnvelope, 16)
		r.pendingOffers[body.BastionID] = queue
	}
	r.mu.Unlock()

	select {
	case queue <- &signalEnvelope{Peer: clientID, SDP: body.Offer}:
	case <-time.After(2 * time.Second):
		http.Error(w, "bastion offer queue full", http.StatusServiceUnavailable)
		return
	}

	select {
	case answer := <-answerCh:
		writeJSON(w, http.StatusOK, map[string]any{
			"answer":    json.RawMessage(answer.SDP),
			"client_id": clientID,
		})
	case <-time.After(60 * time.Second):
		http.Error(w, "bastion did not answer within 60s", http.StatusGatewayTimeout)
	case <-req.Context().Done():
	}

	r.mu.Lock()
	delete(r.pendingAnswers, clientID)
	r.mu.Unlock()
}

// signalingPostCandidate accepts a trickled ICE candidate from either
// the client (peer=bastion_id) or the bastion (peer=client_id).
// Body: {bastion_id, candidate: <RTCIceCandidateInit>}.
// The candidate is fanned into a per-peer queue; the OTHER side's
// candidates endpoint long-polls that queue.
func (r *relay) signalingPostCandidate(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		BastionID string          `json:"bastion_id"`
		Candidate json.RawMessage `json:"candidate"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.BastionID == "" || len(body.Candidate) == 0 {
		http.Error(w, "bastion_id and candidate required", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	q := r.candidateQueueLocked(body.BastionID)
	r.mu.Unlock()
	select {
	case q <- body.Candidate:
		w.WriteHeader(http.StatusNoContent)
	case <-time.After(1 * time.Second):
		http.Error(w, "candidate queue full", http.StatusServiceUnavailable)
	}
}

// signalingPollCandidates long-polls (up to 25 sec) for trickled remote
// candidates addressed to the requesting peer. The peer is identified
// by ?bastion_id= (the OTHER side of the rendezvous).
// Response: {candidates: [...], cursor: "<opaque>"}.
func (r *relay) signalingPollCandidates(w http.ResponseWriter, req *http.Request) {
	bid := req.URL.Query().Get("bastion_id")
	if bid == "" {
		http.Error(w, "bastion_id required", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	q := r.candidateQueueLocked(bid)
	r.mu.Unlock()

	var out []json.RawMessage
	select {
	case c := <-q:
		out = append(out, c)
		// Drain whatever else is immediately available without blocking.
		for {
			select {
			case extra := <-q:
				out = append(out, extra)
			default:
				goto done
			}
		}
	case <-time.After(25 * time.Second):
	case <-req.Context().Done():
		return
	}
done:
	writeJSON(w, http.StatusOK, map[string]any{
		"candidates": out,
		"cursor":     fmt.Sprintf("ts-%d", time.Now().UnixNano()),
	})
}

// candidateQueueLocked returns or lazily creates the trickled-ICE queue
// for a peer. Caller must hold r.mu.
func (r *relay) candidateQueueLocked(peer string) chan json.RawMessage {
	if r.candidateQueues == nil {
		r.candidateQueues = map[string]chan json.RawMessage{}
	}
	q, ok := r.candidateQueues[peer]
	if !ok {
		q = make(chan json.RawMessage, 32)
		r.candidateQueues[peer] = q
	}
	return q
}

// signalingAnswer: bastion posts its SDP answer + ICE back to the relay,
// which forwards it to the client's wait channel.
func (r *relay) signalingAnswer(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var env signalEnvelope
	if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if env.Peer == "" {
		http.Error(w, "peer (client_id) required", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	ch, ok := r.pendingAnswers[env.Peer]
	r.mu.Unlock()
	if !ok {
		http.Error(w, "client_id not waiting (already timed out?)", http.StatusGone)
		return
	}
	select {
	case ch <- &env:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		http.Error(w, "client wait channel closed", http.StatusGone)
	}
}

// ─── helpers ────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func genClientID() string {
	// Simple monotonic ID for v0.1. Production uses crypto/rand or ULID.
	return fmt.Sprintf("c-%d-%d", os.Getpid(), time.Now().UnixNano())
}
