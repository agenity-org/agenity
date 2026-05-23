// Package log tails the chepherd or legacy Python supervisor log file
// without holding it open. Each Tail call emits new lines via a channel
// and exits cleanly when the context is cancelled.
//
// Resilient to log rotation (file shrink) and to file-not-yet-exists.
package log

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"time"
)

// Line is one log line emitted by Tail.
type Line struct {
	Text string // raw line without trailing newline
	When time.Time
}

// DefaultLogPaths returns candidate paths for the supervisor log,
// in priority order. The chepherd Go daemon writes to the first;
// the legacy Python supervisor writes to the second. The tailer
// tries them in order and watches whichever exists.
func DefaultLogPaths() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".local", "state", "chepherd", "chepherd.log"),
		filepath.Join(home, ".local", "state", "workflow", "supervisor.log"),
	}
}

// Tail follows the log file at `path`, emitting a Line for each new line.
// Blocks until ctx is done. If the file doesn't exist yet, it waits.
// Closes `out` before returning.
//
// `historyLines` (>0) means: also emit the last N lines from the file
// at startup, before tailing-forward. Useful for pre-populating the
// TUI's log pane on launch.
func Tail(ctx context.Context, path string, historyLines int, out chan<- Line) {
	defer close(out)

	var f *os.File
	var err error

	// Wait for the file to exist if it doesn't yet (up to 30s).
	for waited := 0; waited < 30; waited++ {
		f, err = os.Open(path)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
	if f == nil {
		return
	}
	defer f.Close()

	// Emit history if requested.
	if historyLines > 0 {
		hist := readLastLines(f, historyLines)
		for _, l := range hist {
			select {
			case <-ctx.Done():
				return
			case out <- Line{Text: l, When: time.Now()}:
			}
		}
	}

	// Seek to end for forward-tailing.
	if _, err := f.Seek(0, 2); err != nil {
		return
	}

	reader := bufio.NewReader(f)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err == nil {
			text := line
			if len(text) > 0 && text[len(text)-1] == '\n' {
				text = text[:len(text)-1]
			}
			select {
			case <-ctx.Done():
				return
			case out <- Line{Text: text, When: time.Now()}:
			}
			continue
		}

		// EOF or transient — check for rotation, then wait briefly.
		if rotated(f, path) {
			f.Close()
			next, openErr := os.Open(path)
			if openErr != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(500 * time.Millisecond):
					continue
				}
			}
			f = next
			reader = bufio.NewReader(f)
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// rotated reports whether the open file `f` no longer points at `path`'s
// current inode, or path's size is smaller than our seek position (truncate).
func rotated(f *os.File, path string) bool {
	openStat, err := f.Stat()
	if err != nil {
		return true
	}
	pathStat, err := os.Stat(path)
	if err != nil {
		return false // file vanished; not rotated, just temporarily gone
	}
	if !os.SameFile(openStat, pathStat) {
		return true
	}
	pos, err := f.Seek(0, 1)
	if err == nil && pathStat.Size() < pos {
		return true
	}
	return false
}

// readLastLines returns up to n lines from the end of f (in file order).
// Uses backward-chunked seek for efficiency on large logs.
func readLastLines(f *os.File, n int) []string {
	const chunk = 8192
	stat, err := f.Stat()
	if err != nil {
		return nil
	}
	size := stat.Size()
	if size == 0 {
		return nil
	}
	var buf []byte
	pos := size
	for pos > 0 && countLines(buf) <= n {
		read := int64(chunk)
		if pos < read {
			read = pos
		}
		pos -= read
		chunkBuf := make([]byte, read)
		if _, err := f.ReadAt(chunkBuf, pos); err != nil {
			break
		}
		buf = append(chunkBuf, buf...)
		if pos == 0 {
			break
		}
	}
	// Restore position to wherever caller's loop will pick up.
	_, _ = f.Seek(size, 0)

	// Split into lines, keep last n non-empty.
	var lines []string
	start := 0
	for i, b := range buf {
		if b == '\n' {
			if i > start {
				lines = append(lines, string(buf[start:i]))
			}
			start = i + 1
		}
	}
	if start < len(buf) {
		lines = append(lines, string(buf[start:]))
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func countLines(b []byte) int {
	c := 0
	for _, x := range b {
		if x == '\n' {
			c++
		}
	}
	return c
}
