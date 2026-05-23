package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/chepherd/chepherd/internal/state"
	"github.com/chepherd/chepherd/internal/style"
)

// Dashboard is the W1 view: header + (session-list | detail) + log + footer.
type Dashboard struct {
	app *App

	root    *tview.Flex
	header  *tview.TextView
	list    *tview.Table
	detail  *tview.TextView
	logView *tview.TextView
	footer  *tview.TextView

	// Rolling log buffer reused by LogMode (W6) for full-screen view.
	logBuffer []string
}

func newDashboard(a *App) *Dashboard {
	d := &Dashboard{app: a}

	// Header — top status bar (1 row, dynamic colors)
	d.header = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)
	d.header.SetBackgroundColor(tcell.ColorBlack)

	// Session list — k9s-style table, selectable rows
	d.list = tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0) // first row = header
	d.list.SetBackgroundColor(tcell.ColorBlack)
	d.list.SetSelectedStyle(tcell.StyleDefault.
		Background(style.SelectedBg).
		Foreground(style.SelectedFg).
		Bold(true))
	d.list.SetSelectionChangedFunc(func(row, col int) {
		// Header row is row 0; data rows start at 1.
		if row < 1 {
			row = 1
		}
		a.Select(row - 1)
		d.renderDetail()
	})

	// Detail pane — right side
	d.detail = tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true).
		SetScrollable(true)
	d.detail.SetBackgroundColor(tcell.ColorBlack)
	d.detail.SetBorderPadding(0, 0, 2, 2) // 2-space horizontal padding

	// Log pane — bottom
	d.logView = tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(false).
		SetScrollable(true).
		SetChangedFunc(func() { a.tv.Draw() })
	d.logView.SetBackgroundColor(tcell.ColorBlack)
	d.logView.SetBorderPadding(0, 0, 2, 2)

	// Footer
	d.footer = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)
	d.footer.SetBackgroundColor(tcell.ColorBlack)

	// Layout assembly — body split (list | detail), log below, headers top/bottom.
	body := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(d.list, 0, 2, true).
		AddItem(d.detail, 0, 3, false)

	d.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(d.header, 1, 0, false).
		AddItem(newBlankRow(), 1, 0, false).
		AddItem(body, 0, 1, true).
		AddItem(newBlankRow(), 1, 0, false).
		AddItem(d.logView, 8, 0, false).
		AddItem(newBlankRow(), 1, 0, false).
		AddItem(d.footer, 1, 0, false)
	d.root.SetBackgroundColor(tcell.ColorBlack)

	// First paint
	d.render()
	return d
}

// newBlankRow returns a 1-row spacer (k9s-style breathing room).
func newBlankRow() *tview.TextView {
	tv := tview.NewTextView()
	tv.SetBackgroundColor(tcell.ColorBlack)
	return tv
}

// render redraws everything from current state.
func (d *Dashboard) render() {
	d.header.SetText(d.app.FormatHeader())
	d.footer.SetText(d.app.FormatFooter())
	d.renderList()
	d.renderDetail()
}

// renderList rebuilds the session table.
func (d *Dashboard) renderList() {
	sessions := d.app.Sessions()
	d.list.Clear()

	// Section title row (k9s convention: bold title + dim underline below)
	d.list.SetCell(0, 0, tview.NewTableCell(style.Tag(style.Title, "SESSIONS")).
		SetSelectable(false).
		SetExpansion(2))
	d.list.SetCell(0, 1, tview.NewTableCell("").SetSelectable(false))
	d.list.SetCell(0, 2, tview.NewTableCell("").SetSelectable(false))
	d.list.SetCell(0, 3, tview.NewTableCell("").SetSelectable(false))

	// Each session = one row
	for i, s := range sessions {
		row := i + 1

		// Dot + name
		band := s.Band
		if isPaused(s) {
			band = "paused"
		}
		dot := style.Tag(style.BandColor(band), "●")
		if band == "paused" {
			dot = style.Tag(style.BandPaused, "○")
		}
		name := style.Tag(style.Title, s.TmuxName)
		d.list.SetCell(row, 0, tview.NewTableCell(fmt.Sprintf("  %s  %s", dot, name)).
			SetExpansion(2))

		// Scorecard
		scoreCell := formatScorecard(s)
		d.list.SetCell(row, 1, tview.NewTableCell("  "+scoreCell))

		// Band text
		bandText := formatBandText(band)
		d.list.SetCell(row, 2, tview.NewTableCell("  "+bandText))

		// Next tick countdown
		nextCell := formatNextTick(s)
		d.list.SetCell(row, 3, tview.NewTableCell("  "+nextCell))
	}

	// Select first row by default
	if d.list.GetRowCount() > 1 {
		current, _ := d.list.GetSelection()
		if current < 1 {
			d.list.Select(1, 0)
		}
	}
}

func formatScorecard(s *state.Session) string {
	if s.LastScorecard == nil {
		return style.Tag(style.Ambient, "?/?/?/?")
	}
	g, v, f, e := s.LastScorecard["G"], s.LastScorecard["V"], s.LastScorecard["F"], s.LastScorecard["E"]
	return fmt.Sprintf("%s/%s/%s/%s",
		style.Tag(style.ScoreColor(g), digitStr(g)),
		style.Tag(style.ScoreColor(v), digitStr(v)),
		style.Tag(style.ScoreColor(f), digitStr(f)),
		style.Tag(style.ScoreColor(e), digitStr(e)))
}

func digitStr(n int) string {
	if n < 0 {
		return "?"
	}
	return fmt.Sprintf("%d", n)
}

func formatBandText(band string) string {
	switch band {
	case "trusted":
		return style.Tag(style.BandTrusted, "trusted")
	case "concerned":
		return style.Tag(style.BandConcerned, "concerned")
	case "crisis":
		return style.TagBold(style.BandCrisis, "CRISIS")
	case "paused":
		return style.Tag(style.BandPaused, "paused")
	case "":
		return style.Tag(style.Ambient, "—")
	default:
		return style.Tag(style.BandStandard, band)
	}
}

func formatNextTick(s *state.Session) string {
	if s.NextTickAt == "" {
		return ""
	}
	dt, err := time.Parse(time.RFC3339, s.NextTickAt)
	if err != nil {
		return ""
	}
	d := time.Until(dt)
	if d < 0 {
		return style.Tag(style.AgeFresh, "now")
	}
	var label string
	switch {
	case d < time.Minute:
		label = fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		label = fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		label = fmt.Sprintf("%dh", int(d.Hours()))
	}
	return style.Tag(style.Ambient, "next "+label)
}

// renderDetail re-renders the right pane based on current selection.
func (d *Dashboard) renderDetail() {
	s := d.app.Selected()
	if s == nil {
		d.detail.SetText(style.Tag(style.Ambient, "  (no session selected — file an issue if you see this)"))
		return
	}

	var b strings.Builder
	w := &b

	// Title
	fmt.Fprintf(w, "%s\n", style.TagBold(style.Title, s.TmuxName))
	fmt.Fprintf(w, "%s\n\n", style.Tag(style.Ambient,
		"─ pid "+intOrDash(s.UUID)+" ─ uuid "+truncate(s.UUID, 12)))

	// IDENTITY section
	fmt.Fprintf(w, "%s\n", style.Tag(style.Title, "IDENTITY"))
	fmt.Fprintf(w, "%s\n", style.Tag(style.TitleRule, "────────"))
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "  %s  %s\n",
		labelW("band            "), formatBandText(s.Band))
	if s.LastVerdict != "" {
		fmt.Fprintf(w, "  %s  %s\n",
			labelW("last verdict    "), style.Tag(style.VerdictColor(s.LastVerdict), s.LastVerdict))
	}
	if s.InterventionCount > 0 {
		fmt.Fprintf(w, "  %s  %s\n",
			labelW("interventions   "), style.Tag(style.Metric, fmt.Sprintf("%d", s.InterventionCount)))
	}
	if s.LastInterventionAt != "" {
		fmt.Fprintf(w, "  %s  %s\n",
			labelW("last intervene  "),
			style.Tag(style.Timestamp, truncate(s.LastInterventionAt, 19)))
	}

	fmt.Fprintf(w, "\n")

	// SCORECARD section
	if s.LastScorecard != nil {
		fmt.Fprintf(w, "%s\n", style.Tag(style.Title, "SCORECARD"))
		fmt.Fprintf(w, "%s\n", style.Tag(style.TitleRule, "─────────"))
		fmt.Fprintf(w, "\n")
		axes := []struct{ name, key string }{
			{"G  goal     ", "G"},
			{"V  velocity ", "V"},
			{"F  focus    ", "F"},
			{"E  end-state", "E"},
		}
		for _, ax := range axes {
			n := s.LastScorecard[ax.key]
			gauge := gaugeBar(n, 10)
			fmt.Fprintf(w, "  %s  %s %s   %s\n",
				style.Tag(style.Primary, ax.name),
				style.Tag(style.ScoreColor(n), fmt.Sprintf("%2d", n)),
				style.Tag(style.Ambient, "/ 10"),
				style.Tag(style.ScoreColor(n), gauge))
		}
		fmt.Fprintf(w, "\n")
	}

	// LAST COACH
	if s.LastCoachTopic != "" {
		fmt.Fprintf(w, "%s\n", style.Tag(style.Title, "LAST COACH TOPIC"))
		fmt.Fprintf(w, "%s\n", style.Tag(style.TitleRule, "────────────────"))
		fmt.Fprintf(w, "\n")
		fmt.Fprintf(w, "  %s\n", style.Tag(style.Primary, s.LastCoachTopic))
	}

	d.detail.SetText(b.String())
	d.detail.ScrollToBeginning()
}

func gaugeBar(filled, total int) string {
	if filled < 0 {
		filled = 0
	}
	if filled > total {
		filled = total
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", total-filled)
}

func labelW(s string) string {
	return style.Tag(style.Primary, s+":")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func intOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s[:8] + "…"
}

// appendLog pushes a new log line into the bottom pane + the rolling buffer.
func (d *Dashboard) appendLog(line string) {
	d.logBuffer = append(d.logBuffer, line)
	if len(d.logBuffer) > 2000 {
		d.logBuffer = d.logBuffer[len(d.logBuffer)-2000:]
	}
	// Only render in the small dashboard log pane if line matches selected session.
	sel := d.app.Selected()
	if sel != nil && !containsSession(line, sel.TmuxName) {
		// Keep buffered but don't display — keeps the small pane focused.
		return
	}
	colored := colorizeLogLine(line)
	fmt.Fprintf(d.logView, "%s\n", colored)
	d.logView.ScrollToEnd()
	// Forward to LogMode if it's open.
	if d.app.logMode != nil {
		d.app.logMode.appendLog(line)
	}
}

func containsSession(line, session string) bool {
	return session == "" || strings.Contains(line, session+":")
}

// colorizeLogLine applies semantic colors to a supervisor log line.
// The patterns mirror the python supervisor's _colorize() output.
func colorizeLogLine(line string) string {
	// Quick semantic classification by content keywords.
	switch {
	case strings.Contains(line, "INJECTED"):
		return style.Tag(style.Injected, line)
	case strings.Contains(line, "judge API error"), strings.Contains(line, "subprocess"):
		return style.Tag(style.APIError, line)
	case strings.Contains(line, "ESCALATING"):
		return style.Tag(style.Escalating, line)
	case strings.Contains(line, "verdict=intervene"):
		return style.Tag(style.VerdictIntervene, line)
	case strings.Contains(line, "verdict=coach"):
		return style.Tag(style.VerdictCoach, line)
	case strings.Contains(line, "verdict=praise"):
		return style.Tag(style.VerdictPraise, line)
	case strings.Contains(line, "verdict=silent"):
		return style.Tag(style.Ambient, line)
	case strings.Contains(line, "BAND "):
		return style.Tag(style.Adopted, line)
	case strings.Contains(line, "founder pause detected"), strings.Contains(line, "paused (sentinel"):
		return style.Tag(style.Pause, line)
	case strings.HasPrefix(strings.TrimSpace(line), "[") && strings.Contains(line, "slack minute"):
		return style.Tag(style.Subdued, line)
	default:
		return style.Tag(style.Body, line)
	}
}
