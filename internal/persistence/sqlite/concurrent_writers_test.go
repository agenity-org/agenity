package sqlite

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

// TestSQLite_ConcurrentWriters_NoSQLiteBusy is the #296 regression gate.
// Spawns N goroutines hammering the same database with concurrent
// AuthSecretRepository.Save calls. Before the fix this reliably caught
// `SQLITE_BUSY` ~50% of the time on chepherd's boot sequence; with the
// busy_timeout PRAGMA + SetMaxOpenConns(1) it MUST succeed 100% of the
// time.
//
// Refs #296.
func TestSQLite_ConcurrentWriters_NoSQLiteBusy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := NewStore(ctx, filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	const N = 32
	var wg sync.WaitGroup
	errs := make(chan error, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Half write to auth_secrets, half to events — both are
			// boot-time hot paths in the #296 RCA.
			if i%2 == 0 {
				if err := s.AuthSecrets().Save(ctx, "test-purpose",
					[]byte("key-bytes-fake"), "HS256"); err != nil {
					errs <- err
				}
			} else {
				// Use a different sessions Save (not events — Events
				// interface differs across versions, but Sessions is
				// stable). Same race surface: concurrent writer
				// against the shared db handle.
				if err := s.Sessions().Save(ctx, "sess-concurrent",
					map[string]any{"k": i}); err != nil {
					errs <- err
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent writer: %v", err)
	}
}
