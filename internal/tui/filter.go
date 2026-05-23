package tui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/chepherd/chepherd/internal/state"
	"github.com/chepherd/chepherd/internal/style"
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
