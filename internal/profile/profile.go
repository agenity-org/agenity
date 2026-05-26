// Package profile centralises chepherd's deployment-profile presets (#129).
//
// CHEPHERD_PROFILE selects a named preset; individual CHEPHERD_* env vars
// always override the preset's values. Three presets are shipped today:
//
//	minimal     — hobby Podman (local container, local JWT auth)
//	standard    — generic K8s (operator spawner, local JWT auth, PVC)
//	enterprise  — OpenOva Sovereign (operator spawner, Keycloak OIDC, Cilium)
//
// The Resolve function is the single source of truth; both cmd/run.go and
// the /healthz endpoint read its output so what the operator sees in the
// banner is exactly what's wired in.
package profile

import (
	"os"
	"strings"
)

// Profile is the materialised set of options for one chepherd deployment.
type Profile struct {
	Name        string // "minimal" | "standard" | "enterprise" | "" (none)
	Spawner     string // "podman-sidecar" | "operator" | "direct"
	AuthMode    string // "local" | "oidc"
	OIDCIssuer  string // populated iff AuthMode=oidc
	StorageType string // "local-volume" | "pvc"
	TLSMode     string // "none" | "mesh"
}

// Resolve materialises the active profile from environment variables.
// Order of precedence (highest wins):
//
//  1. Individual CHEPHERD_SPAWNER / CHEPHERD_AUTH_MODE / etc.
//  2. CHEPHERD_PROFILE preset
//  3. Auto-detection (KUBERNETES_SERVICE_HOST present → operator/local)
//  4. Built-in default (minimal)
func Resolve() Profile {
	name := strings.ToLower(strings.TrimSpace(os.Getenv("CHEPHERD_PROFILE")))
	p := Profile{Name: name}

	// Step 1: preset values
	switch name {
	case "minimal":
		p.Spawner = "podman-sidecar"
		p.AuthMode = "local"
		p.StorageType = "local-volume"
		p.TLSMode = "none"
	case "standard":
		p.Spawner = "operator"
		p.AuthMode = "local"
		p.StorageType = "pvc"
		p.TLSMode = "none"
	case "enterprise":
		p.Spawner = "operator"
		p.AuthMode = "oidc"
		p.StorageType = "pvc"
		p.TLSMode = "mesh"
	default:
		// No profile → auto-detect K8s / fall through to minimal.
		if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
			p.Spawner = "operator"
		} else {
			p.Spawner = "podman-sidecar"
		}
		p.AuthMode = "local"
		p.StorageType = "local-volume"
		p.TLSMode = "none"
	}

	// Step 2: per-knob overrides win.
	if v := os.Getenv("CHEPHERD_SPAWNER"); v != "" {
		p.Spawner = v
	}
	if v := os.Getenv("CHEPHERD_AUTH_MODE"); v != "" {
		p.AuthMode = v
	}
	if v := os.Getenv("CHEPHERD_OIDC_ISSUER"); v != "" {
		p.OIDCIssuer = v
	}
	if v := os.Getenv("CHEPHERD_STORAGE"); v != "" {
		p.StorageType = v
	}
	if v := os.Getenv("CHEPHERD_TLS"); v != "" {
		p.TLSMode = v
	}
	return p
}
