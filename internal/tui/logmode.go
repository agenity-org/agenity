package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/chepherd/chepherd/internal/style"
)

// LogMode is W6 — full-screen filtered log viewer.
// Filtered by selected session by default; 'a' toggles all-sessions.
// '/' opens an in-log search. 'p' pauses auto-scroll. 'w' toggles wrap.
type LogMode struct {
	app *App

	root         *tview.Flex
	header       *tview.TextView
	logView      *tview.TextView
	footer       *tview.TextView
	searchView   *tview.InputField

	filterToSession bool   // true = only show selected session's lines
	pauseScroll     bool   // true = don't auto-scroll on new lines
	wrap            bool
	searchActive    bool
	searchQuery     string
}

func newLogMode(a *App) *LogMode {
	lm := &LogMode{
		app:             a,
		filterToSession: true,
		pauseScroll:     false,
		wrap:            false,
	}

	lm.header = tview.NewTextView().SetDynamicColors(true)
	lm.header.SetBackgroundColor(tcell.ColorBlack)

	lm.logView = tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(false).
		SetScrollable(true).
		SetChangedFunc(func() {})
	lm.logView.SetBackgroundColor(tcell.ColorBlack)
	lm.logView.SetBorderPadding(0, 0, 2, 2)

	lm.footer = tview.NewTextView().SetDynamicColors(true)
	lm.footer.SetBackgroundColor(tcell.ColorBlack)

	lm.searchView = tview.NewInputField().
		SetLabel(style.TagBold(style.KeyLetter, "/")+" ").
		SetFieldBackgroundColor(tcell.ColorBlack).
		SetFieldTextColor(style.Border)
	lm.searchView.SetDoneFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyEsc:
			lm.searchActive = false
			lm.searchQuery = ""
			lm.layout()
		case tcell.KeyEnter:
			lm.searchQuery = lm.searchView.GetText()
			lm.searchActive = false
			lm.layout()
		}
	})

	lm.layout()
	lm.installKeys()
	return lm
}

func (lm *LogMode) layout() {
	lm.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(lm.header, 1, 0, false).
		AddItem(newBlankRow(), 1, 0, false)
	if lm.searchActive {
		lm.root.AddItem(lm.searchView, 3, 0, true)
	}
	lm.root.AddItem(lm.logView, 0, 1, !lm.searchActive).
		AddItem(newBlankRow(), 1, 0, false).
		AddItem(lm.footer, 1, 0, false)
	lm.root.SetBackgroundColor(tcell.ColorBlack)

	lm.refreshHeader()
	lm.refreshFooter()
}

func (lm *LogMode) installKeys() {
	lm.logView.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Rune() {
		case '/':
			lm.searchActive = true
			lm.searchView.SetText(lm.searchQuery)
			lm.layout()
			lm.app.pages.RemovePage("log")
			lm.app.pages.AddPage("log", lm.root, true, true)
			lm.app.tv.SetFocus(lm.searchView)
			return nil
		case 'a':
			lm.filterToSession = !lm.filterToSession
			lm.repopulate()
			lm.refreshHeader()
			lm.refreshFooter()
			return nil
		case 'p':
			lm.pauseScroll = !lm.pauseScroll
			lm.refreshFooter()
			return nil
		case 'w':
			lm.wrap = !lm.wrap
			lm.logView.SetWordWrap(lm.wrap)
			lm.refreshFooter()
			return nil
		case 'q':
			lm.app.Quit()
			return nil
		}
		if ev.Key() == tcell.KeyEsc {
			lm.dismiss()
			return nil
		}
		return ev
	})
}

func (lm *LogMode) refreshHeader() {
	sess := lm.app.Selected()
	var target string
	if lm.filterToSession && sess != nil {
		target = sess.TmuxName
	} else {
		target = "all sessions"
	}
	if lm.searchQuery != "" {
		target += " · search: " + lm.searchQuery
	}
	lm.header.SetText(style.Tag(style.Logo, "  chepherd") + " " +
		style.Tag(style.Ambient, "· log · "+target))
}

func (lm *LogMode) refreshFooter() {
	pause := "off"
	if lm.pauseScroll {
		pause = "on"
	}
	wrap := "off"
	if lm.wrap {
		wrap = "on"
	}
	pairs := []struct{ k, d string }{
		{"esc", "back"},
		{"/", "search"},
		{"a", "all/selected"},
		{"p", "pause:" + pause},
		{"w", "wrap:" + wrap},
		{"q", "quit"},
	}
	var b strings.Builder
	b.WriteString(" ")
	for i, p := range pairs {
		if i > 0 {
			b.WriteString("    ")
		}
		fmt.Fprintf(&b, "%s %s",
			style.TagBold(style.KeyLetter, p.k),
			style.Tag(style.KeyDesc, p.d))
	}
	lm.footer.SetText(b.String())
}

// repopulate re-renders the log view from scratch using the dashboard's
// recent log buffer. Triggered when toggling 'a' or 'search'.
func (lm *LogMode) repopulate() {
	lm.logView.Clear()
	for _, line := range lm.app.dashboard.logBuffer {
		if lm.matchesView(line) {
			fmt.Fprintln(lm.logView, colorizeLogLine(line))
		}
	}
	if !lm.pauseScroll {
		lm.logView.ScrollToEnd()
	}
}

// matchesView decides if a line should appear in the current filtered view.
func (lm *LogMode) matchesView(line string) bool {
	if lm.searchQuery != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(lm.searchQuery)) {
		return false
	}
	if !lm.filterToSession {
		return true
	}
	sess := lm.app.Selected()
	if sess == nil {
		return true
	}
	// Lines have format: "[timestamp] <session-name>: …"
	return strings.Contains(line, sess.TmuxName+":")
}

// appendLog pushes a new tail line.
func (lm *LogMode) appendLog(line string) {
	if !lm.matchesView(line) {
		return
	}
	fmt.Fprintln(lm.logView, colorizeLogLine(line))
	if !lm.pauseScroll {
		lm.logView.ScrollToEnd()
	}
}

func (lm *LogMode) show() {
	lm.refreshHeader()
	lm.refreshFooter()
	lm.repopulate()
	lm.app.pages.AddPage("log", lm.root, true, true)
	lm.app.tv.SetFocus(lm.logView)
}

func (lm *LogMode) dismiss() {
	lm.app.pages.RemovePage("log")
	lm.app.tv.SetFocus(lm.app.dashboard.list)
}
