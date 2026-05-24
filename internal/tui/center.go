// Package tui — center.go renders the v0.3 dashboard's central pane:
// a read-only mirror of the selected session's tmux pane, refreshed
// every CenterRefreshInterval. ANSI colors preserved via tmux's `-e`
// flag + tview's [color] markup translation.
//
// Architecture: a background goroutine ticks every 500ms, runs
// `tmux capture-pane -t <name> -p -e -E -`, converts ANSI escapes to
// tview color tags, and updates a tview.TextView via QueueUpdateDraw.
// On selection change (Dashboard.list rowChanged) the goroutine swaps
// the target session name without restart.
package tui

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/chepherd/chepherd/internal/style"
)

// CenterRefreshInterval is how often capture-pane is re-run. 500ms is
// cheap (≈1ms per call locally) and feels live to the eye without
// flicker. Configurable later if needed.
const CenterRefreshInterval = 500 * time.Millisecond

// Center is the read-only tmux mirror pane.
type Center struct {
	app  *App
	view *tview.TextView

	mu      sync.Mutex
	target  string // current tmux session name being mirrored
	stopper chan struct{}
}

func newCenter(a *App) *Center {
	c := &Center{
		app:     a,
		view:    tview.NewTextView(),
		stopper: make(chan struct{}),
	}
	c.view.SetDynamicColors(true).
		SetScrollable(false).
		SetWrap(false)
	c.view.SetBackgroundColor(tcell.ColorBlack)
	c.view.SetBorder(true).
		SetBorderColor(style.Border).
		SetBorderPadding(0, 0, 1, 1).
		SetTitleColor(style.Title).
		SetTitleAlign(tview.AlignLeft)
	c.SetTarget("")
	return c
}

// SetTarget swaps which tmux session the mirror shows. Empty string =
// no target, view shows a placeholder. Safe to call from any goroutine.
func (c *Center) SetTarget(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.target == name {
		return
	}
	c.target = name
	if name == "" {
		c.view.SetTitle(" (no session selected) ")
		c.view.SetText(style.Tag(style.Ambient, "\n  no session selected"))
		return
	}
	c.view.SetTitle(fmt.Sprintf(" tmux: %s ", name))
}

// Start launches the refresh goroutine. Call once on app start.
// Stops cleanly when c.stopper is closed (via app.Quit's logCancel-style path).
func (c *Center) Start() {
	go c.loop()
}

func (c *Center) loop() {
	t := time.NewTicker(CenterRefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stopper:
			return
		case <-t.C:
			c.mu.Lock()
			target := c.target
			c.mu.Unlock()
			if target == "" {
				continue
			}
			body := captureTmuxPane(target)
			c.app.tv.QueueUpdateDraw(func() {
				c.view.SetText(body)
			})
		}
	}
}

// captureTmuxPane runs `tmux capture-pane -t <name> -p -e -E -` and
// converts ANSI color escapes to tview's dynamic-color markup so the
// TextView renders them.
//
// Flags:
//
//	-p   print to stdout (not a buffer)
//	-e   include escape sequences (preserve colors)
//	-E - cap at end of last line (don't include trailing blank scrollback)
func captureTmuxPane(name string) string {
	out, err := exec.Command("tmux", "capture-pane", "-t", name, "-p", "-e", "-E", "-").Output()
	if err != nil {
		return style.Tag(style.BandCrisis,
			fmt.Sprintf("\n  capture-pane failed for %q: %v\n  (session may have ended)", name, err))
	}
	return ansiToTview(out)
}

// ansiToTview rewrites the most common ANSI SGR escape sequences into
// tview's [foreground:background] color markup. This is a minimal,
// best-effort translator — it handles the foreground/reset cases that
// Claude Code, shells, and the supervisor inject sequences use. For
// everything else, it strips the escape (so we don't emit raw ANSI to
// the TextView, which would render as control characters).
func ansiToTview(b []byte) string {
	var out bytes.Buffer
	// Common pattern: ESC[<n>m for SGR. Walk byte-by-byte; flush text
	// between escapes; translate the escape to a tview color tag.
	openTag := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == 0x1b && i+1 < len(b) && b[i+1] == '[' {
			// Find the terminating letter (anything in [@-~]).
			j := i + 2
			for j < len(b) {
				if b[j] >= '@' && b[j] <= '~' {
					break
				}
				j++
			}
			if j >= len(b) {
				break
			}
			// Only translate SGR (terminator 'm'); skip others.
			if b[j] == 'm' {
				params := string(b[i+2 : j])
				if openTag {
					out.WriteString("[-:-]")
					openTag = false
				}
				if tag := sgrToTviewTag(params); tag != "" {
					out.WriteString(tag)
					openTag = true
				}
			}
			i = j
			continue
		}
		// Escape any literal '[' so tview doesn't interpret it as markup.
		if c == '[' {
			out.WriteString("[[")
			continue
		}
		out.WriteByte(c)
	}
	if openTag {
		out.WriteString("[-:-]")
	}
	return out.String()
}

// sgrToTviewTag converts an SGR parameter string like "31" or "1;33" into
// a tview color tag. Returns "" for resets / unsupported codes.
func sgrToTviewTag(params string) string {
	if params == "" || params == "0" {
		return "" // reset — already handled by closing previous tag
	}
	fg := ""
	bold := false
	for _, p := range strings.Split(params, ";") {
		switch p {
		case "0", "":
			return "" // reset mid-sequence — close any open tag
		case "1":
			bold = true
		case "30":
			fg = "black"
		case "31":
			fg = "red"
		case "32":
			fg = "green"
		case "33":
			fg = "yellow"
		case "34":
			fg = "blue"
		case "35":
			fg = "magenta"
		case "36":
			fg = "cyan"
		case "37":
			fg = "white"
		case "90":
			fg = "darkgray"
		case "91":
			fg = "lightcoral"
		case "92":
			fg = "lightgreen"
		case "93":
			fg = "lightyellow"
		case "94":
			fg = "lightblue"
		case "95":
			fg = "lightmagenta"
		case "96":
			fg = "lightcyan"
		case "97":
			fg = "lightwhite"
		}
	}
	if fg == "" && !bold {
		return ""
	}
	if bold {
		return fmt.Sprintf("[%s::b]", fg)
	}
	return fmt.Sprintf("[%s]", fg)
}
