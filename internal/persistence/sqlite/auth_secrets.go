package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// AuthSecretRepository implements persistence.AuthSecretRepository
// against SQLite. Persists daemon-internal signing secrets keyed by
// purpose: "dashboard-hs256" (v0.9.1 HS256 dashboard token signer)
// and "a2a-es256-priv" (v0.9.2 ES256 A2A JWT signer private key).
//
// Refs #208.
type AuthSecretRepository struct {
	db *sql.DB
}

func NewAuthSecretRepository(db *sql.DB) *AuthSecretRepository {
	return &AuthSecretRepository{db: db}
}

func (r *AuthSecretRepository) Get(ctx context.Context, purpose string) (*persistence.AuthSecret, error) {
	if purpose == "" {
		return nil, errors.New("sqlite AuthSecretRepository: empty purpose")
	}
	var s persistence.AuthSecret
	err := r.db.QueryRowContext(ctx,
		`SELECT purpose, key, algorithm, created_at FROM auth_secrets WHERE purpose = $1`,
		purpose,
	).Scan(&s.Purpose, &s.Key, &s.Algorithm, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sqlite auth_secrets Get %q: not found", purpose)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite auth_secrets Get %q: %w", purpose, err)
	}
	return &s, nil
}

// Save upserts the secret. If a row with the same purpose exists it is
// overwritten with the new key + algorithm (created_at stays initial
// insertion time per the schema's DEFAULT semantics — overwriting an
// existing row updates the bytes but not the timestamp; if you need a
// rotation timestamp, add a `rotated_at` column in a future migration).
func (r *AuthSecretRepository) Save(ctx context.Context, purpose string, key []byte, alg string) error {
	if purpose == "" {
		return errors.New("sqlite AuthSecretRepository: empty purpose")
	}
	if alg == "" {
		return errors.New("sqlite AuthSecretRepository: empty algorithm")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO auth_secrets (purpose, key, algorithm, created_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT(purpose) DO UPDATE SET
		     key = excluded.key,
		     algorithm = excluded.algorithm`,
		purpose, key, alg, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("sqlite auth_secrets Save %q: %w", purpose, err)
	}
	return nil
}

var _ persistence.AuthSecretRepository = (*AuthSecretRepository)(nil)
