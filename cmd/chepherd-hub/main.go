// Package main is the chepherd-hub binary — the public-internet
// surface that hosts cross-org chepherd-org services per
// V0.9.2-ARCHITECTURE.md §5 #46 + §10 Pattern 2 (#491 Wave F1).
//
// chepherd-hub is a SEPARATE binary from chepherd (which runs at the
// operator's premises) so the public-internet surface has its own
// deploy unit, blast radius, version cadence, and dependency closure.
// The operator runs chepherd; chepherd.org (or whoever operates the
// public hub) runs chepherd-hub. Peers reach this binary for the
// services no single operator can host alone:
//
//	┌──────────────────────────────────────────────────────────────┐
//	│  chepherd-hub                                                │
//	│  ──────────────                                              │
//	│  HTTP/HTTPS                                                  │
//	│    GET  /healthz                  — liveness + version       │
//	│    GET  /v1/cards                 — Agent Card directory     │
//	│                                     aggregator (F5 fills)    │
//	│    POST /v1/signaling/offer       ┐                          │
//	│    POST /v1/signaling/answer      ├─ WebRTC SDP relay (F5)   │
//	│    POST /v1/signaling/ice         ┘                          │
//	│    GET  /v1/relay/*               — reverse-proxy fallback   │
//	│                                     (F7 + F8 fill)           │
//	│                                                              │
//	│  UDP                                                         │
//	│    :3478                          — STUN server (pion/stun)  │
//	│    :3478 + tcp:443                — TURN relay   (pion/turn) │
//	└──────────────────────────────────────────────────────────────┘
//
// F1 SCOPE — this PR — is the binary scaffold: HTTP listener, all 5
// HTTP endpoint stubs returning 501 with TODO refs, healthz, config
// surface (env + flags), Dockerfile, lifecycle hooks. STUN + TURN
// listeners are stubbed via "log + sleep" pseudo-servers; F-series
// followups wire the real pion handlers behind those slots.
//
// Refs #491 #208 V0.9.2-ARCHITECTURE.md §5 #46 §10 Pattern 2.
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
	"syscall"
	"time"
)

// hubVersion is the embedded version string surfaced via /healthz.
// Bumped per release; lockstep with the parent chepherd repo.
const hubVersion = "0.9.4-f1"

// config carries all chepherd-hub knobs. Each field documents its
// env var fallback so the deploy unit (Dockerfile + k8s manifest)
// can drive the binary without flags.
type config struct {
	// listen is the HTTP listener address. Default :8443 (dev);
	// production is typically :443 behind a TLS-terminating proxy
	// or with --tls-cert-file + --tls-key-file set.
	listen string

	// tlsCertFile + tlsKeyFile enable HTTPS when both set. Empty
	// pair runs plaintext HTTP (suitable for behind-proxy deploys).
	tlsCertFile string
	tlsKeyFile  string

	// stunListen is the UDP address pion/stun binds. Default :3478
	// per RFC 5389. Empty disables the STUN listener (HTTP-only
	// dev/test).
	stunListen string

	// turnListen is the UDP address pion/turn binds. Same default
	// :3478 (STUN + TURN coexist on the same port per RFC 8656).
	// Empty disables TURN.
	turnListen string

	// allowedOrgs is the comma-separated allowlist of organization
	// identifiers (DNS-style: example.com,acme.org). Empty allows
	// any org (dev-only — production deploys MUST set this). F8
	// JWT federation enforces this; F1 just exposes the field.
	allowedOrgs string

	// turnSecret is the shared secret pion/turn uses to mint TURN
	// credentials. Empty disables TURN. F6 wires the actual
	// credential issuance flow.
	turnSecret string

	// startupTimeout caps how long the binary waits for listeners
	// to become ready before exiting with an error. Default 5s.
	startupTimeout time.Duration
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "chepherd-hub: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := parseFlags(os.Args[1:])
	srv := newServer(cfg)
	httpSrv := &http.Server{
		Addr:              cfg.listen,
		Handler:           srv.mux(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	// HTTP listener (TLS conditionally).
	go func() {
		var err error
		if cfg.tlsCertFile != "" && cfg.tlsKeyFile != "" {
			err = httpSrv.ListenAndServeTLS(cfg.tlsCertFile, cfg.tlsKeyFile)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Printf("chepherd-hub: HTTP listener error: %v", err)
		}
	}()
	scheme := "http"
	if cfg.tlsCertFile != "" {
		scheme = "https"
	}
	fmt.Printf("✓ chepherd-hub HTTP listening on %s://%s (version=%s, allowed-orgs=%q)\n",
		scheme, cfg.listen, hubVersion, cfg.allowedOrgs)

	// STUN + TURN pseudo-listeners (F1 stubs — F3/F6 wire real
	// pion handlers behind these slots).
	stunStop := startSTUNStub(cfg)
	turnStop := startTURNStub(cfg)

	// Graceful shutdown.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	fmt.Println("chepherd-hub: shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	srv.signaling.CloseAll()
	stunStop()
	turnStop()
	return nil
}

func parseFlags(args []string) *config {
	cfg := &config{
		listen:         envOr("CHEPHERD_HUB_LISTEN", ":8443"),
		tlsCertFile:    os.Getenv("CHEPHERD_HUB_TLS_CERT"),
		tlsKeyFile:     os.Getenv("CHEPHERD_HUB_TLS_KEY"),
		stunListen:     envOr("CHEPHERD_HUB_STUN_LISTEN", ":3478"),
		turnListen:     envOr("CHEPHERD_HUB_TURN_LISTEN", ":3478"),
		allowedOrgs:    os.Getenv("CHEPHERD_HUB_ALLOWED_ORGS"),
		turnSecret:     os.Getenv("CHEPHERD_HUB_TURN_SECRET"),
		startupTimeout: 5 * time.Second,
	}
	fs := flag.NewFlagSet("chepherd-hub", flag.ContinueOnError)
	fs.StringVar(&cfg.listen, "listen", cfg.listen, "HTTP listener address (default :8443; env CHEPHERD_HUB_LISTEN)")
	fs.StringVar(&cfg.tlsCertFile, "tls-cert", cfg.tlsCertFile, "TLS cert path (env CHEPHERD_HUB_TLS_CERT)")
	fs.StringVar(&cfg.tlsKeyFile, "tls-key", cfg.tlsKeyFile, "TLS key path (env CHEPHERD_HUB_TLS_KEY)")
	fs.StringVar(&cfg.stunListen, "stun-listen", cfg.stunListen, "STUN UDP listener (default :3478; empty disables)")
	fs.StringVar(&cfg.turnListen, "turn-listen", cfg.turnListen, "TURN UDP listener (default :3478; empty disables)")
	fs.StringVar(&cfg.allowedOrgs, "allowed-orgs", cfg.allowedOrgs, "comma-separated org allowlist (dev-empty; production REQUIRED)")
	fs.StringVar(&cfg.turnSecret, "turn-secret", cfg.turnSecret, "TURN shared secret (env CHEPHERD_HUB_TURN_SECRET)")
	if err := fs.Parse(args); err != nil {
		return cfg
	}
	return cfg
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// server holds runtime state for the hub binary. F1 kept it
// minimal; F5 #495 added the signaling queue. F6/F7/F8 will add
// TURN allocations + cached cards here.
type server struct {
	cfg       *config
	signaling *signalingQueue
}

func newServer(cfg *config) *server {
	return &server{
		cfg:       cfg,
		signaling: newSignalingQueue(),
	}
}

func (s *server) mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/cards", s.handleCards)
	mux.HandleFunc("/v1/cards/", s.handleCards)
	// #495 Wave F5 — SDP signaling relay. Replaces the F1 stubs.
	mux.HandleFunc("/v1/signaling/offer", s.makeSignalingHandler(SignalingOffer))
	mux.HandleFunc("/v1/signaling/answer", s.makeSignalingHandler(SignalingAnswer))
	mux.HandleFunc("/v1/signaling/ice", s.makeSignalingHandler(SignalingICE))
	mux.HandleFunc("/v1/signaling/pending", s.handleSignalingPending)
	mux.HandleFunc("/v1/relay/", s.handleRelay)
	return mux
}

// ─── /healthz ─────────────────────────────────────────────────────

func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"binary":  "chepherd-hub",
		"version": hubVersion,
		"stubs": map[string]string{
			"cards": "F5",
			"stun":  "F3",
			"turn":  "F6",
			"relay": "F7+F8",
		},
		"implemented": map[string]string{
			"signaling": "F5 #495",
		},
	})
}

// ─── /v1/cards ────────────────────────────────────────────────────

const notImplCardsF5 = `endpoint stub — Wave F5 #495 follow-up will implement the Agent Card directory aggregator (peers POST their card URL → hub indexes for discovery). See V0.9.2-ARCHITECTURE.md §5 #46. The signaling relay portion of F5 is wired (offer/answer/ice/pending).`

func (s *server) handleCards(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":     "not implemented",
		"todo_ref":  "F5 #495",
		"detail":    notImplCardsF5,
		"method":    r.Method,
		"path":      r.URL.Path,
	})
}

// ─── /v1/signaling/{offer,answer,ice,pending} ─────────────────────
// Implemented in signaling.go per #495 Wave F5.

// ─── /v1/relay/* ──────────────────────────────────────────────────

const notImplRelayF7F8 = `endpoint stub — Wave F7 #497 + F8 #498 will implement the reverse-proxy fallback (peers behind hard NATs that can't reach DataChannel use HTTP relay through this hub). See V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 9.`

func (s *server) handleRelay(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":    "not implemented",
		"todo_ref": "F7 #497 + F8 #498",
		"detail":   notImplRelayF7F8,
		"method":   r.Method,
		"path":     r.URL.Path,
	})
}

// ─── STUN + TURN stubs ────────────────────────────────────────────

// startSTUNStub starts a log-only placeholder for the STUN server.
// F3 #493 wires the real pion/stun server behind this slot. Returns
// a stop function the graceful shutdown calls.
func startSTUNStub(cfg *config) func() {
	if cfg.stunListen == "" {
		return func() {}
	}
	log.Printf("[chepherd-hub] STUN stub (F3 will wire pion/stun on udp:%s)", cfg.stunListen)
	stop := make(chan struct{})
	go func() {
		<-stop
	}()
	return func() { close(stop) }
}

// startTURNStub starts a log-only placeholder for the TURN relay.
// F6 #496 wires the real pion/turn server behind this slot.
func startTURNStub(cfg *config) func() {
	if cfg.turnListen == "" {
		return func() {}
	}
	if cfg.turnSecret == "" {
		log.Printf("[chepherd-hub] TURN stub disabled (F1 scaffold; --turn-secret empty; F6 will wire pion/turn on udp:%s)", cfg.turnListen)
	} else {
		log.Printf("[chepherd-hub] TURN stub (F6 will wire pion/turn on udp:%s)", cfg.turnListen)
	}
	stop := make(chan struct{})
	go func() {
		<-stop
	}()
	return func() { close(stop) }
}
