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

// Dashboard is the v0.3 4-pane view:
//
//	header (1) │
//	daemonBar (1) │ (visible only when daemon down/stale)
//	body: list (left 18) │ center tmux mirror (48) │ right column (30)
//	right column: detail/scorecard (60%) over logView (40%)
//	footer (1)
type Dashboard struct {
	app *App

	root      *tview.Flex
	header    *tview.Flex // holds left+right text views
	headerL   *tview.TextView
	headerR   *tview.TextView
	daemonBar *tview.TextView // W10 — daemon-down/stale banner; empty when healthy
	list      *tview.Table
	center    *Center // v0.3 — read-only tmux mirror
	detail    *tview.TextView
	logView   *tview.TextView
	footer    *tview.TextView

	// Rolling log buffer reused by LogMode (W6) for full-screen view.
	logBuffer []string

	// lastWidth tracks the most recent terminal width so applyNarrowMode
	// only re-shuffles the layout when the width actually crosses a
	// breakpoint, not every render tick.
	lastWidth int
}

func newDashboard(a *App) *Dashboard {
	d := &Dashboard{app: a}

	// Header — top status bar (1 row). Two text views in a Flex so the
	// tiny brand mark on the right (`▰ chepherd 0.3`) stays right-anchored
	// regardless of terminal width. Left side carries the wordmark + stats.
	d.headerL = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)
	d.headerL.SetBackgroundColor(tcell.ColorBlack)
	d.headerR = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignRight)
	d.headerR.SetBackgroundColor(tcell.ColorBlack)
	d.header = tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(d.headerL, 0, 1, false).
		AddItem(d.headerR, 16, 0, false) // 14 cols for "▰ chepherd 0.3" + padding

	// Daemon health banner — empty unless daemon down/stale (W10)
	d.daemonBar = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)
	d.daemonBar.SetBackgroundColor(tcell.ColorBlack)

	// Session list — k9s-style table, selectable rows, bordered box
	d.list = tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0) // first row = header
	d.list.SetBackgroundColor(tcell.ColorBlack)
	d.list.SetSelectedStyle(tcell.StyleDefault.
		Background(style.SelectedBg).
		Foreground(style.SelectedFg).
		Bold(true))
	d.list.SetBorder(true).
		SetBorderColor(style.Border).
		SetBorderPadding(0, 0, 1, 1).
		SetTitle(" Sessions ").
		SetTitleColor(style.Title).
		SetTitleAlign(tview.AlignLeft)
	d.list.SetSelectionChangedFunc(func(row, col int) {
		// Header row is row 0; data rows start at 1.
		if row < 1 {
			row = 1
		}
		a.Select(row - 1)
		d.renderDetail()
	})

	// Detail pane — top-right, scorecard for the selected session
	d.detail = tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true).
		SetScrollable(true)
	d.detail.SetBackgroundColor(tcell.ColorBlack)
	d.detail.SetBorderPadding(0, 0, 1, 1).
		SetBorder(true).
		SetBorderColor(style.Border).
		SetTitle(" Scorecard ").
		SetTitleColor(style.Title).
		SetTitleAlign(tview.AlignLeft)

	// Center pane — v0.3 read-only tmux mirror of the selected session
	d.center = newCenter(a)

	// Log pane — bottom, bordered box
	d.logView = tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(false).
		SetScrollable(true).
		SetChangedFunc(func() { a.tv.Draw() })
	d.logView.SetBackgroundColor(tcell.ColorBlack)
	d.logView.SetBorderPadding(0, 0, 1, 1).
		SetBorder(true).
		SetBorderColor(style.Border).
		SetTitle(" Log ").
		SetTitleColor(style.Title).
		SetTitleAlign(tview.AlignLeft)

	// Footer
	d.footer = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)
	d.footer.SetBackgroundColor(tcell.ColorBlack)

	// v0.3 4-pane layout assembly:
	//
	//   header (1) → daemonBar (1) → body → footer (1)
	//
	// Body horizontal split: list (left ≈18) | center mirror (≈48) | right (≈30)
	// Right vertical split: detail/scorecard (60%) over log (40%)
	//
	// Proportions expressed as flex weights so they scale with terminal
	// width. Center pane is the visual anchor and the largest weight.
	rightCol := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(d.detail, 0, 3, false). // 60% of right column
		AddItem(d.logView, 0, 2, false) // 40% of right column

	body := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(d.list, 0, 18, true).
		AddItem(d.center.view, 0, 48, false).
		AddItem(rightCol, 0, 30, false)

	d.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(d.header, 1, 0, false).
		AddItem(d.daemonBar, 1, 0, false). // W10 — shown only when daemon down/stale
		AddItem(body, 0, 1, true).
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
	d.headerL.SetText(d.app.FormatHeader())
	d.headerR.SetText(d.app.FormatHeaderRight())
	d.footer.SetText(d.app.FormatFooter())

	// W10 — daemon health banner (visible only on issues).
	health := CheckDaemonHealth(d.app.Sessions())
	d.daemonBar.SetText(FormatDaemonBanner(health))

	d.renderList()
	d.renderDetail()
	d.applyNarrowMode()

	// v0.3 — keep the center pane locked on the currently-selected session.
	if s := d.app.Selected(); s != nil {
		d.center.SetTarget(s.TmuxName)
	} else {
		d.center.SetTarget("")
	}
}

// applyNarrowMode collapses panes when the terminal is too small.
// Per v0.3 spec: hide right pane <100 cols, hide left <70 cols. Center
// is never dropped — the live tmux mirror is the dashboard's reason to exist.
func (d *Dashboard) applyNarrowMode() {
	_, _, w, _ := d.root.GetRect()
	if w == 0 {
		return // not yet sized
	}
	// Re-build the body Flex per current width.
	body := tview.NewFlex().SetDirection(tview.FlexColumn)
	if w >= 70 {
		body.AddItem(d.list, 0, 18, true)
	}
	body.AddItem(d.center.view, 0, 48, false)
	if w >= 100 {
		rightCol := tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(d.detail, 0, 3, false).
			AddItem(d.logView, 0, 2, false)
		body.AddItem(rightCol, 0, 30, false)
	}
	// Re-assemble root only if the body shape actually changed —
	// guarded via a tracked width to avoid every-tick layout reshuffles.
	if d.lastWidth == w {
		return
	}
	d.lastWidth = w
	d.root.Clear()
	d.root.AddItem(d.header, 1, 0, false).
		AddItem(d.daemonBar, 1, 0, false).
		AddItem(body, 0, 1, true).
		AddItem(d.footer, 1, 0, false)
}

// renderList rebuilds the session table.
func (d *Dashboard) renderList() {
	sessions := d.app.Sessions()
	d.list.Clear()

	// Section title row (k9s convention: bold title + dim underline below)
	d.list.SetCell(0, 0, tview.NewTableCell(style.Tag(style.Title, "SESSIONS")).
		SetSelectable(false).
		SetExpansion(2))
	d.list.SetCell(0, 1, tview.NewTableCell(style.Tag(style.TitleRule, "  score")).
		SetSelectable(false))
	d.list.SetCell(0, 2, tview.NewTableCell(style.Tag(style.TitleRule, "  GVFE")).
		SetSelectable(false))
	d.list.SetCell(0, 3, tview.NewTableCell(style.Tag(style.TitleRule, "  band")).
		SetSelectable(false))
	d.list.SetCell(0, 4, tview.NewTableCell(style.Tag(style.TitleRule, "  next")).
		SetSelectable(false))

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
		nameCell := fmt.Sprintf("  %s  %s", dot, name)
		if s.AuthLapsed {
			nameCell += "  " + style.TagBold(style.BandCrisis, "🔒 LOGIN")
		}
		d.list.SetCell(row, 0, tview.NewTableCell(nameCell).
			SetExpansion(2))

		// Overall score (geomean of G·V·F·E) + 10-cell gauge — v0.3 spec.
		overallCell := formatGeomeanGauge(s)
		d.list.SetCell(row, 1, tview.NewTableCell("  "+overallCell))

		// Scorecard G/V/F/E
		scoreCell := formatScorecard(s)
		d.list.SetCell(row, 2, tview.NewTableCell("  "+scoreCell))

		// Band text
		bandText := formatBandText(band)
		d.list.SetCell(row, 3, tview.NewTableCell("  "+bandText))

		// Next tick countdown
		nextCell := formatNextTick(s)
		d.list.SetCell(row, 4, tview.NewTableCell("  "+nextCell))
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

// formatGeomeanGauge renders the v0.3 left-pane "overall" column:
//
//	"5.7  █████░░░░░"
//
// Number is the geometric mean of G·V·F·E (one weak axis drags it down),
// 10-cell gauge maps 0–10 to filled/empty cells. Color = ScoreColor of
// the rounded geomean so the eye picks crisis sessions instantly. '—'
// when no scorecard yet.
func formatGeomeanGauge(s *state.Session) string {
	gm := s.Geomean()
	if gm < 0 {
		return style.Tag(style.Ambient, " —")
	}
	filled := int(gm + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > 10 {
		filled = 10
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
	color := style.ScoreColor(filled)
	return fmt.Sprintf("%s %s",
		style.TagBold(color, fmt.Sprintf("%3.1f", gm)),
		style.Tag(color, bar))
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

	// LIVE — near-real-time signals refreshed by 'chepherd live' (every 5s, no LLM)
	if ls := s.LiveSignals; ls != nil {
		fmt.Fprintf(w, "%s\n", style.Tag(style.Title, "LIVE"))
		fmt.Fprintf(w, "%s %s\n",
			style.Tag(style.TitleRule, "────"),
			style.Tag(style.Timestamp, "(refreshed "+formatAgo(ls.RefreshedAt)+")"))
		fmt.Fprintf(w, "  %s  %s\n",
			style.Tag(style.Primary, "in-progress :"),
			style.Tag(style.Metric, fmt.Sprintf("%d", ls.InProgressCount)))
		fmt.Fprintf(w, "  %s  %s\n",
			style.Tag(style.Primary, "unclaimed   :"),
			style.Tag(style.Metric, fmt.Sprintf("%d", ls.UnclaimedCount)))
		fmt.Fprintf(w, "  %s  %s\n",
			style.Tag(style.Primary, "backlog     :"),
			style.Tag(style.Metric, fmt.Sprintf("%d", ls.BacklogCount)))
		fmt.Fprintf(w, "  %s  %s\n",
			style.Tag(style.Primary, "commits 1h  :"),
			style.Tag(style.Metric, fmt.Sprintf("%d", ls.CommitCountLast1H)))
		if ls.LastCommitAgeMin > 0 {
			fmt.Fprintf(w, "  %s  %s\n",
				style.Tag(style.Primary, "last commit :"),
				style.Tag(style.AgeColor(ls.LastCommitAgeMin),
					fmt.Sprintf("%.0fm ago", ls.LastCommitAgeMin)))
		}
		if ls.TrackerMtimeMin > 0 {
			fmt.Fprintf(w, "  %s  %s\n",
				style.Tag(style.Primary, "TRACKER     :"),
				style.Tag(style.AgeColor(ls.TrackerMtimeMin),
					fmt.Sprintf("%.0fm ago", ls.TrackerMtimeMin)))
		}
		fmt.Fprintf(w, "\n")
	}

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

// formatAgo turns an RFC3339 timestamp into a human-readable "Xs ago" / "Xm ago".
func formatAgo(ts string) string {
	if ts == "" {
		return "never"
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t, err = time.Parse(time.RFC3339, ts)
		if err != nil {
			return "?"
		}
	}
	d := time.Since(t)
	switch {
	case d < 10*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
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
