// Package registry is the chepherd-relay's bastion registration ledger.
// One row per bastion, indexed by bastion_id. Holds the long-lived daemon
// token, the owning user_id, last-seen timestamp, and operational metadata
// the relay uses to route signaling + push.
//
// Storage backend is pluggable behind the Registry interface. v0.0.1
// ships an in-memory implementation; production deployments use the
// Postgres backend (TODO follow-up).
package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Bastion is one registered bastion.
type Bastion struct {
	ID              string    `json:"id"`           // operator-chosen, stable
	UserID          string    `json:"user_id"`      // owning identity
	DaemonToken     string    `json:"-"`            // long-lived; never returned in API
	LastSeenAt      time.Time `json:"last_seen_at"`
	CreatedAt       time.Time `json:"created_at"`
	ChepherdVersion string    `json:"chepherd_version,omitempty"`
	Capabilities    []string  `json:"capabilities,omitempty"`
	Hostname        string    `json:"hostname,omitempty"`
}

// Registry is the storage abstraction.
type Registry interface {
	// Register issues a fresh DaemonToken for a new bastion, or rotates
	// the existing one when ID + UserID match. Idempotent on re-call from
	// the same identity.
	Register(ctx context.Context, b Bastion) (token string, err error)

	// Touch updates last_seen_at when the bastion polls (signaling/poll).
	Touch(ctx context.Context, bastionID string) error

	// Get retrieves a bastion record by ID.
	Get(ctx context.Context, bastionID string) (*Bastion, error)

	// VerifyDaemonToken looks up the bastion that owns this long-lived
	// token. Returns ErrUnknownToken if not found.
	VerifyDaemonToken(ctx context.Context, token string) (*Bastion, error)

	// ListByUser returns all bastions belonging to a user.
	ListByUser(ctx context.Context, userID string) ([]*Bastion, error)
}

// Common errors.
var (
	ErrUnknownBastion = errors.New("registry: unknown bastion")
	ErrUnknownToken   = errors.New("registry: unknown daemon token")
	ErrTakenByOther   = errors.New("registry: bastion_id taken by another user")
)

// Memory is an in-memory Registry implementation. Used by tests + by
// chepherd-relay v0.0.1 dev mode. Production deployments swap in Postgres.
type Memory struct {
	mu        sync.RWMutex
	byID      map[string]*Bastion
	byToken   map[string]*Bastion
}

// NewMemory constructs an empty in-memory Registry.
func NewMemory() *Memory {
	return &Memory{
		byID:    map[string]*Bastion{},
		byToken: map[string]*Bastion{},
	}
}

// Register implements Registry.
func (m *Memory) Register(ctx context.Context, b Bastion) (string, error) {
	if b.ID == "" || b.UserID == "" {
		return "", errors.New("registry: ID and UserID required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.byID[b.ID]; ok {
		if existing.UserID != b.UserID {
			return "", ErrTakenByOther
		}
		// Idempotent re-register from same user — rotate token.
		delete(m.byToken, existing.DaemonToken)
		newTok, err := mintToken()
		if err != nil {
			return "", err
		}
		existing.DaemonToken = newTok
		existing.LastSeenAt = time.Now().UTC()
		existing.Capabilities = b.Capabilities
		existing.ChepherdVersion = b.ChepherdVersion
		existing.Hostname = b.Hostname
		m.byToken[newTok] = existing
		return newTok, nil
	}

	tok, err := mintToken()
	if err != nil {
		return "", err
	}
	b.DaemonToken = tok
	b.CreatedAt = time.Now().UTC()
	b.LastSeenAt = b.CreatedAt
	stored := b
	m.byID[b.ID] = &stored
	m.byToken[tok] = &stored
	return tok, nil
}

// Touch implements Registry.
func (m *Memory) Touch(ctx context.Context, bastionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.byID[bastionID]
	if !ok {
		return ErrUnknownBastion
	}
	b.LastSeenAt = time.Now().UTC()
	return nil
}

// Get implements Registry.
func (m *Memory) Get(ctx context.Context, bastionID string) (*Bastion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.byID[bastionID]
	if !ok {
		return nil, ErrUnknownBastion
	}
	bcopy := *b
	bcopy.DaemonToken = "" // never leak via Get
	return &bcopy, nil
}

// VerifyDaemonToken implements Registry.
func (m *Memory) VerifyDaemonToken(ctx context.Context, token string) (*Bastion, error) {
	if token == "" {
		return nil, ErrUnknownToken
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.byToken[token]
	if !ok {
		return nil, ErrUnknownToken
	}
	bcopy := *b
	bcopy.DaemonToken = "" // never echo back
	return &bcopy, nil
}

// ListByUser implements Registry.
func (m *Memory) ListByUser(ctx context.Context, userID string) ([]*Bastion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Bastion
	for _, b := range m.byID {
		if b.UserID == userID {
			bcopy := *b
			bcopy.DaemonToken = ""
			out = append(out, &bcopy)
		}
	}
	return out, nil
}

// mintToken returns a fresh 32-byte hex daemon token.
func mintToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "chep_" + hex.EncodeToString(b[:]), nil
}
