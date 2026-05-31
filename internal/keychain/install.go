// internal/keychain/install.go — #322 H6.1. Explicit-backend
// installation: bypasses the Active() auto-selection chain in favor
// of a caller-supplied Backend. Used by cmd/run.go when
// --keychain-backend=openbao is set.
//
// Refs #322 H6.1 #322 H6.
package keychain

import (
	"errors"
)

// installed is the explicitly-installed Backend (when set via
// Install). Protected by keychain.go's `mu` along with chosen +
// chosenOK — single mutex avoids the data race -race detector found
// pre-#522 where installedMu protected only `installed` while
// Install also wrote `chosen = nil` + reset `once` outside any
// shared lock.
var installed Backend

// Install sets the active Backend, bypassing Active()'s platform
// chain. Subsequent Set/Get/Delete calls route through b. Calling
// Install with nil restores the auto-select behavior.
//
// Refs #322 H6.1 #522.
func Install(b Backend) {
	mu.Lock()
	defer mu.Unlock()
	installed = b
	// Invalidate the cached chosen so the next Active() call re-
	// evaluates against the new installed override (or, when nil,
	// re-runs the platform chain).
	chosen = nil
	chosenOK = false
}

// ErrConfigIncomplete is returned by NewOpenBaoBackendFromFlags when
// the operator set --keychain-backend=openbao but omitted addr or
// token-file.
var ErrConfigIncomplete = errors.New("keychain: openbao config incomplete (need --openbao-addr + --openbao-token-file)")

// NewOpenBaoBackendFromFlags constructs an OpenBaoBackend from CLI
// flag values. Returns ErrConfigIncomplete when either addr or
// tokenFile is empty. mount defaults to 'secret'.
//
// Refs #322 H6.1.
func NewOpenBaoBackendFromFlags(addr, tokenFile, mount string) (*OpenBaoBackend, error) {
	if addr == "" || tokenFile == "" {
		return nil, ErrConfigIncomplete
	}
	return NewOpenBaoBackend(addr, "", tokenFile, mount)
}
