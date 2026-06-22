// Package canon — operator-authored working principles, Layer 1 of the
// v0.9 3-layer agent context model (#198).
//
// Singleton record (id="default" for v0.9) stored under
// $stateDir/canon/. Versioned (monotonic int) — every PUT bumps
// Version and snapshots the prior body to /history/v{N}.md so the
// admin UI can show diffs + rollback.
//
// REJECTED architectural alternatives:
//   - Mount host ~/.claude/CLAUDE.md into the pod (host coupling)
//   - Inline canon body in every Skill (30KB × 10 skills duplication)
//
// CHOSEN: canon is a first-class chepherd-state entity, auto-applied
// as Layer 1 of every spawned agent's system prompt — no opt-in, no
// per-team toggle, environment-stable.
package canon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// Canon is the singleton operator-canon record.
type Canon struct {
	ID        string    `json:"id"`         // singleton "default" for v0.9
	Title     string    `json:"title"`      // operator-facing name
	Body      string    `json:"body"`       // markdown content
	Version   int       `json:"version"`    // monotonic; bumped on every save
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
}

// Store is the persistence layer.
//
// Two backing modes (mutually exclusive; chosen by constructor):
//   - File-on-disk under $stateDir/canon/ (v0.9.1 default, kept for
//     backward compat — use NewStore(stateDir))
//   - persistence.CanonRepository delegate (v0.9.2 path — use
//     NewStoreFromRepository; see store_repo.go)
type Store struct {
	dir  string                       // file-on-disk mode
	repo persistence.CanonRepository  // repo mode (v0.9.2)
	mu   sync.RWMutex
}

// NewStore opens (or initialises) the canon directory.
func NewStore(stateDir string) (*Store, error) {
	dir := filepath.Join(stateDir, "canon")
	if err := os.MkdirAll(filepath.Join(dir, "history"), 0o700); err != nil {
		return nil, fmt.Errorf("canon.NewStore: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Get returns the current canon. If none exists yet, returns a blank
// canon with version=0 — never nil.
func (s *Store) Get() (*Canon, error) {
	if s.repo != nil {
		return s.repoGet()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := os.ReadFile(s.currentPath())
	if err != nil {
		if os.IsNotExist(err) {
			now := time.Now().UTC()
			return &Canon{
				ID:        "default",
				Title:     "Operator Canon",
				Body:      "",
				Version:   0,
				CreatedAt: now,
				UpdatedAt: now,
			}, nil
		}
		return nil, err
	}
	var c Canon
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Put replaces the canon body. Bumps Version, archives the prior
// version under /history/.
func (s *Store) Put(body, updatedBy, title string) (*Canon, error) {
	if s.repo != nil {
		return s.repoPut(body, updatedBy, title)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.getLocked()
	if err != nil {
		return nil, err
	}
	// Archive prior body if it existed.
	if cur.Version > 0 {
		hpath := filepath.Join(s.dir, "history", "v"+strconv.Itoa(cur.Version)+".json")
		if data, err := json.MarshalIndent(cur, "", "  "); err == nil {
			_ = os.WriteFile(hpath, data, 0o600)
		}
	}
	now := time.Now().UTC()
	if title == "" {
		title = cur.Title
	}
	if title == "" {
		title = "Operator Canon"
	}
	next := &Canon{
		ID:        "default",
		Title:     title,
		Body:      body,
		Version:   cur.Version + 1,
		UpdatedBy: updatedBy,
		UpdatedAt: now,
		CreatedAt: cur.CreatedAt,
	}
	if next.CreatedAt.IsZero() {
		next.CreatedAt = now
	}
	return next, s.saveLocked(next)
}

// History returns up to `limit` prior versions, newest-first. The
// current version is NOT included (use Get for that).
func (s *Store) History(limit int) ([]*Canon, error) {
	if s.repo != nil {
		return s.repoHistory(limit)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(filepath.Join(s.dir, "history"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []*Canon{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, "history", e.Name()))
		if err != nil {
			continue
		}
		var c Canon
		if err := json.Unmarshal(b, &c); err != nil {
			continue
		}
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Rollback restores a prior version's body as the current canon (bumps
// Version, archives current). Returns the new current.
func (s *Store) Rollback(toVersion int, actor string) (*Canon, error) {
	if s.repo != nil {
		return s.repoRollback(toVersion, actor)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hpath := filepath.Join(s.dir, "history", "v"+strconv.Itoa(toVersion)+".json")
	b, err := os.ReadFile(hpath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrVersionNotFound
		}
		return nil, err
	}
	var prior Canon
	if err := json.Unmarshal(b, &prior); err != nil {
		return nil, err
	}
	cur, err := s.getLocked()
	if err != nil {
		return nil, err
	}
	// Archive current first.
	if cur.Version > 0 {
		curHpath := filepath.Join(s.dir, "history", "v"+strconv.Itoa(cur.Version)+".json")
		if data, err := json.MarshalIndent(cur, "", "  "); err == nil {
			_ = os.WriteFile(curHpath, data, 0o600)
		}
	}
	now := time.Now().UTC()
	next := &Canon{
		ID:        "default",
		Title:     prior.Title,
		Body:      prior.Body,
		Version:   cur.Version + 1,
		UpdatedBy: actor,
		UpdatedAt: now,
		CreatedAt: cur.CreatedAt,
	}
	if next.CreatedAt.IsZero() {
		next.CreatedAt = now
	}
	return next, s.saveLocked(next)
}

// internals

func (s *Store) currentPath() string {
	return filepath.Join(s.dir, "current.json")
}

func (s *Store) getLocked() (*Canon, error) {
	b, err := os.ReadFile(s.currentPath())
	if err != nil {
		if os.IsNotExist(err) {
			now := time.Now().UTC()
			return &Canon{
				ID: "default", Title: "Operator Canon", Body: "",
				Version: 0, CreatedAt: now, UpdatedAt: now,
			}, nil
		}
		return nil, err
	}
	var c Canon
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) saveLocked(c *Canon) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.currentPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.currentPath())
}

var ErrVersionNotFound = errors.New("canon: version not found")
