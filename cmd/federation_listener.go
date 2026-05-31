// cmd/federation_listener.go binds the optional federation-facing
// mTLS HTTP listener that #527 Wave T3.1 adds alongside the
// dashboard listener. Cross-org peers terminate mTLS here while
// the dashboard listener stays plain TLS (browsers can't present
// client certs).
//
// The handler is the SAME runtimehttp.Server mux the dashboard
// uses — every D3 / D1 / T2 / A4 endpoint is reachable to mTLS-
// verified peers + the existing application-layer auth (D3 grants,
// A4 extended-card auth gate) still applies orthogonally. mTLS at
// the listener is the FIRST gate; AuthMiddleware + GrantCheck are
// the SECOND.
//
// Refs #527 #487 V0.9.2-ARCHITECTURE.md §15.1 §22.
package cmd

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/chepherd/chepherd/internal/federation"
	"github.com/chepherd/chepherd/internal/runtimehttp"
)

// startFederationListener binds the federation-facing TLS listener
// on addr + serves the same handler as the dashboard. Returns the
// actual bound address (so callers using addr ":0" can discover
// the kernel-assigned port) + the *http.Server for shutdown.
func startFederationListener(addr string, rs *runtimehttp.Server) (string, *http.Server, error) {
	if rs.FederationMTLS == nil {
		return "", nil, fmt.Errorf("startFederationListener: no MTLSConfig (set --federation-mtls=true first)")
	}
	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	tlsCfg := federation.BuildServerTLSConfig(rs.FederationMTLS)
	tlsLn := tls.NewListener(tcpLn, tlsCfg)
	srv := &http.Server{
		Handler:           rs.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = srv.Serve(tlsLn)
	}()
	return tcpLn.Addr().String(), srv, nil
}
