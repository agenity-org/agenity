package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// KeychainRepository implements persistence.KeychainRepository against
// SQLite. This is the SQLite-backed Backend alongside the existing
// macos/linux/file backends in internal/keychain/keychain.go.
//
// Refs #208.
type KeychainRepository struct {
	db *sql.DB
}

func NewKeychainRepository(db *sql.DB) *KeychainRepository {
	return &KeychainRepository{db: db}
}

func (r *KeychainRepository) Get(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", errors.New("postgres KeychainRepository: empty key")
	}
	var v string
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM keychain WHERE key = $1`,
		key,
	).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("postgres keychain Get %q: not found", key)
	}
	if err != nil {
		return "", fmt.Errorf("postgres keychain Get %q: %w", key, err)
	}
	return v, nil
}

func (r *KeychainRepository) Set(ctx context.Context, key, value string) error {
	if key == "" {
		return errors.New("postgres KeychainRepository: empty key")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO keychain (key, value, updated_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT(key) DO UPDATE SET
		     value = excluded.value,
		     updated_at = excluded.updated_at`,
		key, value, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("postgres keychain Set %q: %w", key, err)
	}
	return nil
}

func (r *KeychainRepository) Delete(ctx context.Context, key string) error {
	if key == "" {
		return errors.New("postgres KeychainRepository: empty key")
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM keychain WHERE key = $1`,
		key,
	)
	if err != nil {
		return fmt.Errorf("postgres keychain Delete %q: %w", key, err)
	}
	return nil
}

var _ persistence.KeychainRepository = (*KeychainRepository)(nil)
