package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/chepherd/chepherd/internal/style"
)

// HelpOverlay is W7 — a modal card showing all keybindings grouped by intent.
// Dims background, displays on top of whatever screen the user was on.
type HelpOverlay struct {
	app   *App
	modal *tview.Frame
	inner *tview.TextView
}

func newHelpOverlay(a *App) *HelpOverlay {
	h := &HelpOverlay{app: a}
	h.inner = tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true).
		SetScrollable(true)
	h.inner.SetBackgroundColor(tcell.ColorBlack)
	h.inner.SetBorderPadding(1, 1, 2, 2)
	h.inner.SetText(h.renderText())

	h.modal = tview.NewFrame(h.inner).
		SetBorders(0, 0, 0, 0, 0, 0)
	h.modal.SetBorder(true).
		SetBorderColor(style.Border).
		SetTitle(style.TagBold(style.Title, " chepherd · help ")).
		SetTitleAlign(tview.AlignLeft).
		SetBackgroundColor(tcell.ColorBlack)

	h.inner.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Rune() {
		case '?', 'q':
			h.dismiss()
			return nil
		}
		if ev.Key() == tcell.KeyEsc {
			h.dismiss()
			return nil
		}
		return ev
	})
	return h
}

func (h *HelpOverlay) renderText() string {
	groups := map[string][]KeyBinding{}
	order := []string{"Navigation", "Filtering", "Actions", "Views"}
	for _, kb := range DashboardKeys {
		groups[kb.Group] = append(groups[kb.Group], kb)
	}

	var b strings.Builder
	for i, g := range order {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s\n", style.TagBold(style.Title, g))
		fmt.Fprintf(&b, "%s\n", style.Tag(style.TitleRule, strings.Repeat("─", len(g))))
		for _, kb := range groups[g] {
			fmt.Fprintf(&b, "  %s   %s\n",
				padRight(style.TagBold(style.KeyLetter, kb.Key), 22),
				style.Tag(style.Primary, kb.Desc))
		}
	}

	fmt.Fprintf(&b, "\n%s\n", style.Tag(style.Ambient,
		"press ? or esc to close"))
	return b.String()
}

func padRight(s string, n int) string {
	// We can't count display width of color tags, so this is heuristic.
	// Strip tags for measurement.
	visible := stripTags(s)
	if len(visible) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(visible))
}

func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '[':
			inTag = true
		case r == ']':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// show centers the modal on top of the current page.
func (h *HelpOverlay) show() {
	// Center the modal — use a Grid to position it.
	grid := tview.NewGrid().
		SetColumns(0, 60, 0).
		SetRows(0, 24, 0).
		AddItem(h.modal, 1, 1, 1, 1, 0, 0, true)
	grid.SetBackgroundColor(tcell.ColorBlack)
	h.app.pages.AddPage("help", grid, true, true)
	h.app.tv.SetFocus(h.inner)
}

func (h *HelpOverlay) dismiss() {
	h.app.pages.RemovePage("help")
	// Restore focus to the dashboard's list.
	h.app.tv.SetFocus(h.app.dashboard.list)
}
