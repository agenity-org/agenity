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

// sgrToTviewTag converts an SGR parameter string like "31" or "1;38;5;208"
// into a tview color tag `[fg:bg:attrs]`. Supports:
//   - 16 ANSI named colors (30-37, 90-97 fg + 40-47, 100-107 bg)
//   - 256-color: `38;5;N` (fg) + `48;5;N` (bg)
//   - truecolor: `38;2;R;G;B` (fg) + `48;2;R;G;B` (bg)
//   - attributes: 1=bold, 2=dim, 3=italic, 4=underline, 7=reverse, 5=blink
//
// Returns "" for plain reset. Empty params or "0" → reset (caller emits [-:-]).
func sgrToTviewTag(params string) string {
	if params == "" || params == "0" {
		return "" // reset — caller closes previous tag
	}
	var fg, bg, attrs string
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		switch p {
		case "0", "":
			fg, bg, attrs = "", "", ""
		case "1":
			attrs += "b"
		case "2":
			attrs += "d"
		case "3":
			attrs += "i"
		case "4":
			attrs += "u"
		case "5":
			attrs += "l" // blink
		case "7":
			attrs += "r"
		case "22":
			// normal intensity — strip b + d
			attrs = stripAttr(attrs, 'b', 'd')
		case "23":
			attrs = stripAttr(attrs, 'i')
		case "24":
			attrs = stripAttr(attrs, 'u')
		case "27":
			attrs = stripAttr(attrs, 'r')
		case "39":
			fg = ""
		case "49":
			bg = ""
		// 16-color foreground
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
			fg = "darkmagenta"
		case "36":
			fg = "darkcyan"
		case "37":
			fg = "silver"
		case "90":
			fg = "gray"
		case "91":
			fg = "red"
		case "92":
			fg = "lime"
		case "93":
			fg = "yellow"
		case "94":
			fg = "blue"
		case "95":
			fg = "fuchsia"
		case "96":
			fg = "aqua"
		case "97":
			fg = "white"
		// 16-color background
		case "40":
			bg = "black"
		case "41":
			bg = "red"
		case "42":
			bg = "green"
		case "43":
			bg = "yellow"
		case "44":
			bg = "blue"
		case "45":
			bg = "darkmagenta"
		case "46":
			bg = "darkcyan"
		case "47":
			bg = "silver"
		case "100":
			bg = "gray"
		case "101":
			bg = "red"
		case "102":
			bg = "lime"
		case "103":
			bg = "yellow"
		case "104":
			bg = "blue"
		case "105":
			bg = "fuchsia"
		case "106":
			bg = "aqua"
		case "107":
			bg = "white"
		case "38":
			// Extended foreground: `38;5;N` (256-color) or `38;2;R;G;B` (truecolor)
			if i+1 < len(parts) {
				switch parts[i+1] {
				case "5":
					if i+2 < len(parts) {
						fg = xterm256ToHex(parts[i+2])
						i += 2
					}
				case "2":
					if i+4 < len(parts) {
						fg = fmt.Sprintf("#%02s%02s%02s",
							toHex2(parts[i+2]),
							toHex2(parts[i+3]),
							toHex2(parts[i+4]))
						i += 4
					}
				}
			}
		case "48":
			// Extended background: same shape with 48 instead of 38.
			if i+1 < len(parts) {
				switch parts[i+1] {
				case "5":
					if i+2 < len(parts) {
						bg = xterm256ToHex(parts[i+2])
						i += 2
					}
				case "2":
					if i+4 < len(parts) {
						bg = fmt.Sprintf("#%02s%02s%02s",
							toHex2(parts[i+2]),
							toHex2(parts[i+3]),
							toHex2(parts[i+4]))
						i += 4
					}
				}
			}
		}
	}
	if fg == "" && bg == "" && attrs == "" {
		return ""
	}
	return fmt.Sprintf("[%s:%s:%s]", fg, bg, attrs)
}

func stripAttr(s string, drop ...byte) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		skip := false
		for _, d := range drop {
			if s[i] == d {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, s[i])
		}
	}
	return string(out)
}

func toHex2(dec string) string {
	var n int
	fmt.Sscanf(dec, "%d", &n)
	if n < 0 {
		n = 0
	}
	if n > 255 {
		n = 255
	}
	return fmt.Sprintf("%02x", n)
}

// xterm256ToHex converts an xterm 256-color index (0-255) to a #RRGGBB
// hex string. 0-15 are the standard ANSI colors, 16-231 are a 6×6×6 RGB
// cube, 232-255 are 24 levels of grayscale.
func xterm256ToHex(s string) string {
	var n int
	fmt.Sscanf(s, "%d", &n)
	if n < 0 || n > 255 {
		return ""
	}
	switch {
	case n < 16:
		// Standard 16: map to fixed palette
		pal := []string{
			"#000000", "#800000", "#008000", "#808000",
			"#000080", "#800080", "#008080", "#c0c0c0",
			"#808080", "#ff0000", "#00ff00", "#ffff00",
			"#0000ff", "#ff00ff", "#00ffff", "#ffffff",
		}
		return pal[n]
	case n < 232:
		// 6×6×6 cube
		i := n - 16
		r := i / 36
		g := (i / 6) % 6
		b := i % 6
		toLevel := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + v*40
		}
		return fmt.Sprintf("#%02x%02x%02x", toLevel(r), toLevel(g), toLevel(b))
	default:
		// Grayscale 232-255
		v := 8 + (n-232)*10
		return fmt.Sprintf("#%02x%02x%02x", v, v, v)
	}
}
