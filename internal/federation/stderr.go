package federation

import (
	"io"
	"os"
)

// stderrPrintf is a swappable stderr sink — tests redirect it via
// SetStderr to avoid noise on the test log.
var stderrPrintf io.Writer = os.Stderr

// SetStderr swaps the federation package's stderr sink. Tests use this
// to silence the diagnostic output. Passing nil restores the default
// os.Stderr sink.
func SetStderr(w io.Writer) {
	if w == nil {
		stderrPrintf = os.Stderr
		return
	}
	stderrPrintf = w
}
