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
	"strconv"
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
	// credentials per draft-uberti-behave-turn-rest. Empty disables
	// TURN (the credentials endpoint returns 503; the udp listener
	// stays a log-only stub).
	turnSecret string

	// turnRealm is the TURN realm name embedded in auth challenges
	// (RFC 5389 §15.7). Empty → "chepherd-hub". Production
	// deploys typically set this to the public hostname.
	turnRealm string

	// turnRelayIP is the IP address pion advertises as the relay
	// address to clients. When empty F6 derives it from the UDP
	// listener bind addr (sensible for single-host deploys; multi-
	// homed production hosts MUST set this to the public IP).
	turnRelayIP string

	// turnTCPListen is the optional tcp listen address for the
	// TURN-over-TCP fallback (UDP-blocked networks). Empty disables.
	// Wired in a follow-up — F6 ships the udp listener + the cred
	// URI list optionally advertises the tcp endpoint.
	turnTCPListen string

	// turnPublicHost is the externally-reachable host:port runners
	// receive in the TURN URI list. When empty the URI uses the
	// listen address (suitable for local-dev tests).
	turnPublicHost string

	// turnRelayMin/turnRelayMax bound the UDP port range pion allocates for
	// TURN relay allocations (#675). When both are >0 and max>=min the
	// server uses RelayAddressGeneratorPortRange so the deploy only needs a
	// small, known UDP window opened on the node/firewall instead of the
	// whole ephemeral range. Zero (default) keeps the legacy unbounded
	// RelayAddressGeneratorStatic behaviour.
	turnRelayMin int
	turnRelayMax int

	// federationTargets is the comma-separated <orgID>=<daemonURL>
	// pairs registry the hub forwards /v1/federation/auth requests
	// to (#498 Wave F8). Empty disables federation (/v1/federation/auth
	// returns 502 for any target). Production: a directory feeds
	// this; v0.9.4 uses static flag config.
	federationTargets string

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

	// STUN pseudo-listener (F1 stub — F3 wires real pion behind it).
	stunStop := startSTUNStub(cfg)
	// #496 Wave F6 — real pion/turn server replaces F1's log-only stub.
	turn, turnStop := startTURN(cfg)
	srv.turn = turn

	// Graceful shutdown.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	fmt.Println("chepherd-hub: shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	srv.signaling.CloseAll()
	srv.registry.CloseAll()
	srv.tunnels.closeAll()
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
	fs.StringVar(&cfg.turnRealm, "turn-realm", envOr("CHEPHERD_HUB_TURN_REALM", ""),
		"#496 Wave F6 — TURN realm (RFC 5389); empty defaults to \"chepherd-hub\"")
	fs.StringVar(&cfg.turnRelayIP, "turn-relay-ip", envOr("CHEPHERD_HUB_TURN_RELAY_IP", ""),
		"#496 Wave F6 — public IP pion advertises to clients; empty derives from listen addr (production REQUIRED on multi-homed hosts)")
	fs.StringVar(&cfg.turnTCPListen, "turn-tcp-listen", envOr("CHEPHERD_HUB_TURN_TCP_LISTEN", ""),
		"#496 Wave F6 — optional TURN-over-TCP listener for UDP-blocked networks (e.g. :443)")
	fs.StringVar(&cfg.turnPublicHost, "turn-public-host", envOr("CHEPHERD_HUB_TURN_PUBLIC_HOST", ""),
		"#496 Wave F6 — host:port runners receive in the TURN URI list (default: derive from listen addr)")
	fs.IntVar(&cfg.turnRelayMin, "turn-relay-min", envOrInt("CHEPHERD_HUB_TURN_RELAY_MIN", 0),
		"#675 — min UDP relay port pion allocates (0=unbounded legacy RelayAddressGeneratorStatic)")
	fs.IntVar(&cfg.turnRelayMax, "turn-relay-max", envOrInt("CHEPHERD_HUB_TURN_RELAY_MAX", 0),
		"#675 — max (inclusive) UDP relay port; must be >= --turn-relay-min to bound the range")
	fs.StringVar(&cfg.federationTargets, "federation-targets",
		envOr("CHEPHERD_HUB_FEDERATION_TARGETS", ""),
		"#498 Wave F8 — comma-separated <orgID>=<daemonURL> pairs the hub relays /v1/federation/auth to (empty disables)")
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

// envOrInt is the int counterpart of envOr (#675 — relay port range).
func envOrInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// server holds runtime state for the hub binary. F1 kept it
// minimal; F5 #495 added signaling queue; F6 #496 TURN relay;
// F7 #497 tunnels; F8 #498 federation registry.
type server struct {
	cfg        *config
	signaling  *signalingQueue
	registry   *registryStore                // #672 — peer-discovery directory
	turn       *turnRelay                    // nil when TURN disabled (no --turn-secret)
	tunnels    *tunnelManager                // #497 Wave F7 — reverse-proxy tunnels
	federation *federationRegistryWithClient // #498 Wave F8 — cross-org JWT relay
}

func newServer(cfg *config) *server {
	return &server{
		cfg:        cfg,
		signaling:  newSignalingQueue(),
		registry:   newRegistryStore(),
		tunnels:    newTunnelManager(),
		federation: loadFederationTargetsFromConfig(cfg),
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
	// #672 — peer-discovery directory. Daemons announce/heartbeat
	// their presence; peers list live orgs for cross-party discovery.
	mux.HandleFunc("/v1/registry/announce", s.handleRegistryAnnounce)
	mux.HandleFunc("/v1/registry/peers", s.handleRegistryPeers)
	// #496 Wave F6 — TURN credentials mint endpoint.
	mux.HandleFunc("/v1/turn/credentials", s.handleTURNCredentials)
	// #497 Wave F7 — reverse-proxy tunnel + tunnel control.
	// /v1/relay/tunnel  : runner-initiated WS upgrade
	// /v1/relay/{org}/* : inbound HTTP forwarded over tunnel
	mux.HandleFunc("/v1/relay/", s.handleRelayInbound)
	// #498 Wave F8 — cross-org JWT federation relay.
	mux.HandleFunc("/v1/federation/auth", s.handleFederationAuth)
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
		},
		"implemented": map[string]string{
			"signaling":  "F5 #495",
			"turn":       "F6 #496",
			"relay":      "F7 #497",
			"federation": "F8 #498",
		},
		"turn":       s.turnStatus(),
		"tunnels":    s.tunnelsStatus(),
		"federation": s.federationStatus(),
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
// Implemented in tunnel.go per #497 Wave F7.

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

// startTURNStub removed in #496 Wave F6 — replaced by startTURN in
// turn.go which wires the real pion/turn/v5 server.
