package canon

import (
	"errors"
	"strings"
	"testing"
)

func TestEmptyOnFirstGet(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	c, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("Get should return non-nil even before first Put")
	}
	if c.Version != 0 {
		t.Errorf("version = %d, want 0 for unset canon", c.Version)
	}
	if c.ID != "default" {
		t.Errorf("id = %q, want default", c.ID)
	}
}

func TestPutBumpsVersion(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	c1, err := s.Put("hello world", "alice", "Canon")
	if err != nil {
		t.Fatal(err)
	}
	if c1.Version != 1 {
		t.Errorf("first Put version = %d, want 1", c1.Version)
	}
	c2, _ := s.Put("hello world v2", "alice", "")
	if c2.Version != 2 {
		t.Errorf("second Put version = %d, want 2", c2.Version)
	}
	cur, _ := s.Get()
	if cur.Version != 2 || !strings.Contains(cur.Body, "v2") {
		t.Errorf("Get after Puts didn't reflect last write: %+v", cur)
	}
}

func TestHistoryAndRollback(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	_, _ = s.Put("v1", "alice", "Canon")
	_, _ = s.Put("v2", "alice", "")
	_, _ = s.Put("v3", "alice", "")
	hist, err := s.History(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) < 2 {
		t.Fatalf("expected ≥2 history entries, got %d", len(hist))
	}
	// Newest-first ordering
	if len(hist) >= 2 && hist[0].Version < hist[1].Version {
		t.Errorf("history not newest-first: %d before %d", hist[0].Version, hist[1].Version)
	}
	// Rollback to v1
	rb, err := s.Rollback(1, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if rb.Body != "v1" {
		t.Errorf("rollback body = %q, want 'v1'", rb.Body)
	}
	if rb.Version != 4 {
		t.Errorf("rollback version = %d, want 4 (bumps fwd)", rb.Version)
	}
}

func TestRollbackNotFound(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	_, _ = s.Put("v1", "alice", "")
	_, err := s.Rollback(99, "")
	if !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("want ErrVersionNotFound, got %v", err)
	}
}

func TestPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, _ := NewStore(dir)
	_, _ = s1.Put("persistent body", "alice", "TestCanon")
	s2, _ := NewStore(dir)
	c, _ := s2.Get()
	if c.Body != "persistent body" || c.Version != 1 {
		t.Fatalf("persistence broken: %+v", c)
	}
}

// Banned-vocab guard — canon package source files must never contain
// the historical banned-vocab list / ~/.claude/CLAUDE.md mount.
// We test by re-reading the package source dir at test time.
func TestNoBannedVocabInCanon(t *testing.T) {
	// This test is intentionally trivial — the actual scan runs in
	// the project-wide ./scripts/banned-vocab.sh CI check. Here we
	// just lock the rule by reading our own builtins source if any.
	// Keep it as a placeholder so future contributors notice the
	// banned-vocab discipline.
}
