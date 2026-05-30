// internal/keychain/install.go — #322 H6.1. Explicit-backend
// installation: bypasses the Active() auto-selection chain in favor
// of a caller-supplied Backend. Used by cmd/run.go when
// --keychain-backend=openbao is set.
//
// Refs #322 H6.1 #322 H6.
package keychain

import (
	"errors"
	"sync"
)

var (
	installedMu sync.Mutex
	installed   Backend
)

// Install sets the active Backend, bypassing Active()'s platform
// chain. Subsequent Set/Get/Delete calls route through b. Calling
// Install with nil restores the auto-select behavior.
//
// Refs #322 H6.1.
func Install(b Backend) {
	installedMu.Lock()
	defer installedMu.Unlock()
	installed = b
	// Reset the once.Do guard so the next Active() call re-evaluates
	// against the installed backend.
	once = sync.Once{}
	chosen = nil
}

// activeOverride returns the installed Backend (if any) — Active()
// consults this before falling through to the platform chain.
func activeOverride() Backend {
	installedMu.Lock()
	defer installedMu.Unlock()
	return installed
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
