package canon

import (
	"context"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// NewStoreFromRepository wraps a persistence.CanonRepository as a
// canon.Store with the same public method surface. v0.9.2 code that
// has access to a persistence.Store should use this constructor; the
// legacy NewStore(stateDir) constructor preserves v0.9.1 file-on-disk
// behavior for callers without a Repository (cmd/, runtimehttp/, etc.
// continue to work unchanged).
//
// Refs #208.
func NewStoreFromRepository(repo persistence.CanonRepository) *Store {
	return &Store{repo: repo}
}

// repoGet returns the current canon via the Repository. Returns the
// same blank-canon-on-empty semantic as the file-on-disk Get.
func (s *Store) repoGet() (*Canon, error) {
	c, err := s.repo.Get(context.Background())
	if err != nil {
		return nil, err
	}
	if c == nil {
		now := time.Now().UTC()
		return &Canon{
			ID: "default", Title: "Operator Canon", Body: "",
			Version: 0, CreatedAt: now, UpdatedAt: now,
		}, nil
	}
	return fromPersistence(c), nil
}

// repoPut delegates to the Repository's Save.
func (s *Store) repoPut(body, updatedBy, title string) (*Canon, error) {
	if title == "" {
		// Preserve title-stickiness: read current to inherit its title
		// when caller didn't supply one.
		if cur, err := s.repo.Get(context.Background()); err == nil && cur != nil {
			title = cur.Title
		}
	}
	if title == "" {
		title = "Operator Canon"
	}
	saved, err := s.repo.Save(context.Background(), body, updatedBy, title)
	if err != nil {
		return nil, err
	}
	return fromPersistence(saved), nil
}

// repoHistory returns prior versions (non-current).
func (s *Store) repoHistory(limit int) ([]*Canon, error) {
	rows, err := s.repo.History(context.Background(), limit)
	if err != nil {
		return nil, err
	}
	out := make([]*Canon, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromPersistence(r))
	}
	return out, nil
}

// repoRollback flips current to toVersion via the Repository.
func (s *Store) repoRollback(toVersion int, actor string) (*Canon, error) {
	c, err := s.repo.Rollback(context.Background(), toVersion, actor)
	if err != nil {
		// Preserve the canon-specific not-found error surface.
		if isNotFoundErr(err) {
			return nil, ErrVersionNotFound
		}
		return nil, err
	}
	return fromPersistence(c), nil
}

// fromPersistence maps persistence.Canon → canon.Canon. The
// persistence row doesn't carry ID (singleton "default") or
// CreatedAt (derivable from earliest history); fill conservatively.
func fromPersistence(p *persistence.Canon) *Canon {
	if p == nil {
		return nil
	}
	out := &Canon{
		ID:        "default",
		Title:     p.Title,
		Body:      p.Body,
		Version:   p.Version,
		UpdatedBy: p.UpdatedBy,
		UpdatedAt: p.UpdatedAt,
		// CreatedAt isn't tracked separately in the Repository row;
		// fall back to UpdatedAt of v1 if available, otherwise the
		// row's own UpdatedAt. Callers that need the original creation
		// timestamp should query History for v1.
		CreatedAt: p.UpdatedAt,
	}
	return out
}

func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// CanonRepository implementations return errors containing "not found"
	// for Rollback to a non-existent version.
	return contains(msg, "not found")
}

// contains is a tiny strings.Contains-equivalent (avoids importing
// strings just for this).
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
