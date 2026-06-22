package tui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/agenity-org/agenity/internal/state"
	"github.com/agenity-org/agenity/internal/style"
)

// Filter is W3 — a slide-in filter box at the top of the session list.
// Matches substring (case-insensitive) on tmux_name. Live updates per keystroke.
type Filter struct {
	app    *App
	input  *tview.InputField
	prev   string
	active bool
}

func newFilter(a *App) *Filter {
	f := &Filter{app: a}
	f.input = tview.NewInputField().
		SetLabel(style.TagBold(style.KeyLetter, "/")+" ").
		SetFieldBackgroundColor(tcell.ColorBlack).
		SetFieldTextColor(style.Border).
		SetLabelColor(style.KeyLetter)
	f.input.SetBackgroundColor(tcell.ColorBlack)
	f.input.SetBorder(true).
		SetBorderColor(style.Border)
	f.input.SetChangedFunc(func(text string) {
		f.app.applyFilter(text)
	})
	f.input.SetDoneFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyEsc:
			f.dismiss(true) // clear filter
		case tcell.KeyEnter:
			f.dismiss(false) // keep filter, hide box
		}
	})
	// Belt-and-suspenders: also capture Esc + Ctrl-G + Ctrl-C at the
	// input level so the dismiss fires even if tview's SetDoneFunc
	// chain is interrupted by a parent SetInputCapture. The filter
	// box previously trapped the user — pressing Esc didn't escape.
	f.input.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEsc, tcell.KeyCtrlG:
			f.dismiss(true)
			return nil
		case tcell.KeyCtrlC:
			// Treat Ctrl-C as "abandon filter" inside the input rather
			// than as "quit app" — the user expects a familiar escape.
			f.dismiss(true)
			return nil
		}
		return ev
	})
	return f
}

// show opens the filter input above the session list.
func (f *Filter) show() {
	if f.active {
		return
	}
	f.active = true
	f.input.SetText(f.prev) // restore prior filter if any

	// Construct a Flex that puts the filter input on top of the dashboard body.
	grid := tview.NewGrid().
		SetColumns(0).
		SetRows(3, 0).
		AddItem(f.input, 0, 0, 1, 1, 0, 0, true).
		AddItem(f.app.dashboard.root, 1, 0, 1, 1, 0, 0, false)
	grid.SetBackgroundColor(tcell.ColorBlack)
	f.app.pages.AddPage("filter", grid, true, true)
	f.app.tv.SetFocus(f.input)
}

func (f *Filter) dismiss(clear bool) {
	if !f.active {
		return
	}
	f.active = false
	if clear {
		f.input.SetText("")
		f.prev = ""
		f.app.applyFilter("")
	} else {
		f.prev = f.input.GetText()
	}
	// Switch back to the dashboard page + drop the filter overlay.
	// Do NOT call tv.Draw() — that's a tview re-entry from inside an
	// input handler (Esc/Enter routed here via SetInputCapture +
	// SetDoneFunc) and froze the dismiss on the founder's TTY.
	// SwitchToPage + RemovePage are enough; tickerLoop redraws ≤1s.
	f.app.pages.SwitchToPage("dashboard")
	f.app.pages.RemovePage("filter")
	f.app.tv.SetFocus(f.app.dashboard.list)
}

// matches reports whether a session passes the current filter string.
func matchesFilter(s *state.Session, q string) bool {
	if q == "" {
		return true
	}
	q = strings.ToLower(q)
	return strings.Contains(strings.ToLower(s.TmuxName), q) ||
		strings.Contains(strings.ToLower(s.UUID), q) ||
		strings.Contains(strings.ToLower(s.Band), q)
}
