// internal/runtimehttp/daemon_a2a_cutover_410.go — Wave R5 #466
// daemon de-A2A cutover.
//
// chepherd-daemon no longer hosts the A2A surface. After R1-R4 the
// per-runner A2A endpoint at /a2a/<sid>/jsonrpc (Wave R2 #463) is
// the canonical entry; peers discover the per-runner URL via daemon's
// Wave D1 directory at /api/v1/agents/.
//
// To preserve operator-visibility we DON'T silently drop the legacy
// routes — they return HTTP 410 Gone with:
//   - a Deprecation: true response header (RFC 9745 Deprecation
//     Header Field)
//   - a Sunset: <wave-R5-merge-date> response header (RFC 8594)
//   - a Link: rel="successor-version" pointing at the directory
//   - a structured JSON-RPC -32601 ("method not found") body so
//     existing A2A clients see an error they can parse rather than
//     hanging on a bare TCP-level disconnect
//
// One stderr log line per request so operators see traffic at the
// dead endpoint + can find the upstream caller.
//
// Refs #466 #453 V0.9.2-ARCHITECTURE.md §5 #3 §22.
package runtimehttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

const (
	// r5SunsetDate is the date Wave R5 #466 retired the daemon-side
	// A2A surface. Used by the Sunset header per RFC 8594. ISO 8601.
	r5SunsetDate = "Sun, 31 May 2026 00:00:00 GMT"

	// r5SuccessorPath is the URL on this daemon where peers can
	// discover the new runner-hosted endpoints. Surfaced via the
	// Link: rel="successor-version" header on every 410 response.
	r5SuccessorPath = "/api/v1/agents/"
)

// r5A2ACutoverHandler returns the http.Handler that serves 410-Gone
// for daemon-legacy A2A paths. legacyPath is the operator-readable
// path name surfaced in the log line + JSON-RPC error message
// (e.g. "/jsonrpc", "/.well-known/agent-card.json").
//
// The handler is mounted by Server.Handler() in place of the
// pre-R5 a2a.RegisterRoutes call.
func r5A2ACutoverHandler(legacyPath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(os.Stderr,
			"[chepherd-daemon R5] LEGACY A2A request %s %s from %s — 410-Gone (caller should discover the per-runner endpoint via %s)\n",
			r.Method, legacyPath, r.RemoteAddr, r5SuccessorPath)

		w.Header().Set("Deprecation", "true")
		w.Header().Set("Sunset", r5SunsetDate)
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="successor-version"`, r5SuccessorPath))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"error": map[string]any{
				"code": -32601,
				"message": fmt.Sprintf(
					"daemon A2A surface retired by Wave R5 (#466): %s no longer serves A2A. "+
						"Discover per-runner endpoints via daemon's Wave D1 directory at %s, "+
						"then POST to the runner's /a2a/<sid>/jsonrpc.",
					legacyPath, r5SuccessorPath,
				),
			},
		})
	})
}
