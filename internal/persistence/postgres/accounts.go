package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// AccountRepository implements persistence.AccountRepository against
// SQLite. Persists operator account identity + LLM-provider credential
// reference bindings (doc inventory #54).
//
// Refs #208.
type AccountRepository struct {
	db *sql.DB
}

func NewAccountRepository(db *sql.DB) *AccountRepository {
	return &AccountRepository{db: db}
}

func (r *AccountRepository) Get(ctx context.Context, id string) (*persistence.Account, error) {
	if id == "" {
		return nil, errors.New("postgres AccountRepository: empty id")
	}
	var a persistence.Account
	err := r.db.QueryRowContext(ctx,
		`SELECT id, class, label, keychain_key, email, created_at, updated_at
		 FROM accounts WHERE id = $1`,
		id,
	).Scan(&a.ID, &a.Class, &a.Label, &a.KeychainKey, &a.Email, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("postgres accounts Get %q: not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres accounts Get %q: %w", id, err)
	}
	return &a, nil
}

func (r *AccountRepository) List(ctx context.Context) ([]*persistence.Account, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, class, label, keychain_key, email, created_at, updated_at
		 FROM accounts ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres accounts List: %w", err)
	}
	defer rows.Close()
	var out []*persistence.Account
	for rows.Next() {
		var a persistence.Account
		if err := rows.Scan(&a.ID, &a.Class, &a.Label, &a.KeychainKey, &a.Email, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

func (r *AccountRepository) Save(ctx context.Context, a *persistence.Account) error {
	if a == nil {
		return errors.New("postgres AccountRepository: nil account")
	}
	if a.ID == "" {
		return errors.New("postgres AccountRepository: empty account ID")
	}
	if a.Class == "" {
		return errors.New("postgres AccountRepository: empty account Class")
	}
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO accounts (id, class, label, keychain_key, email, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT(id) DO UPDATE SET
		     class = excluded.class,
		     label = excluded.label,
		     keychain_key = excluded.keychain_key,
		     email = excluded.email,
		     updated_at = excluded.updated_at`,
		a.ID, a.Class, a.Label, a.KeychainKey, a.Email, a.CreatedAt, a.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres accounts Save %q: %w", a.ID, err)
	}
	return nil
}

func (r *AccountRepository) Delete(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("postgres AccountRepository: empty id")
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM accounts WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("postgres accounts Delete %q: %w", id, err)
	}
	return nil
}

var _ persistence.AccountRepository = (*AccountRepository)(nil)
