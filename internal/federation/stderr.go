package federation

import (
	"io"
	"os"
	"sync"
)

// stderrMu guards stderrCurrent. Read on every diagnostic write
// (announceOnce / pollOnce / fetchAndPersist) + written by SetStderr.
// Reader contention is bounded — diagnostic writes are background +
// the only writer is test setup/teardown.
var (
	stderrMu      sync.RWMutex
	stderrCurrent io.Writer = os.Stderr
)

// SetStderr swaps the federation package's stderr sink. Tests use this
// to silence the diagnostic output. Passing nil restores the default
// os.Stderr sink. Safe for concurrent use with active writers.
func SetStderr(w io.Writer) {
	stderrMu.Lock()
	defer stderrMu.Unlock()
	if w == nil {
		stderrCurrent = os.Stderr
		return
	}
	stderrCurrent = w
}

// stderrPrintf is the package-level destination — kept as an
// io.Writer wrapper so callers can fmt.Fprintf to it directly without
// rediscovering the lock on each call.
type stderrWriter struct{}

func (stderrWriter) Write(p []byte) (int, error) {
	stderrMu.RLock()
	w := stderrCurrent
	stderrMu.RUnlock()
	return w.Write(p)
}

var stderrPrintf io.Writer = stderrWriter{}
