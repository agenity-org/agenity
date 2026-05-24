// In-memory device-token registry. Production deployments swap this
// for a Postgres-backed implementation; the interface stays the same.

package push

import (
	"context"
	"sync"
	"time"
)

// Registration — one device's push credentials, owned by a user.
type Registration struct {
	UserID     string
	DeviceID   string // client-generated stable UUID
	Token      Token
	UpdatedAt  time.Time
	// LastFailureAt — when set, the registry can decay this entry; a
	// token that has failed for N days gets pruned (FCM/APNs both signal
	// uninstall via specific error codes).
	LastFailureAt time.Time
}

// TokenRegistry is the contract.
type TokenRegistry interface {
	Upsert(ctx context.Context, r Registration) error
	ListByUser(ctx context.Context, userID string) ([]Registration, error)
	MarkFailure(ctx context.Context, deviceID string) error
	Delete(ctx context.Context, deviceID string) error
}

// Memory — single-process in-memory registry. fine for v0.2.
type Memory struct {
	mu sync.RWMutex
	// by deviceID
	regs map[string]Registration
}

// NewMemory constructs an empty Memory registry.
func NewMemory() *Memory {
	return &Memory{regs: map[string]Registration{}}
}

// Upsert creates-or-replaces a registration by device ID.
func (m *Memory) Upsert(_ context.Context, r Registration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.UpdatedAt = time.Now().UTC()
	m.regs[r.DeviceID] = r
	return nil
}

// ListByUser returns all device tokens registered to that user.
func (m *Memory) ListByUser(_ context.Context, userID string) ([]Registration, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Registration
	for _, r := range m.regs {
		if r.UserID == userID {
			out = append(out, r)
		}
	}
	return out, nil
}

// MarkFailure records a delivery failure timestamp.
func (m *Memory) MarkFailure(_ context.Context, deviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.regs[deviceID]
	if !ok {
		return nil
	}
	r.LastFailureAt = time.Now().UTC()
	m.regs[deviceID] = r
	return nil
}

// Delete removes a registration (operator signed out / uninstalled).
func (m *Memory) Delete(_ context.Context, deviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.regs, deviceID)
	return nil
}
