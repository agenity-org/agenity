package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/agenity-org/agenity/internal/state"
	"github.com/agenity-org/agenity/internal/style"
)

// Detail is W4 — the drill-in view from pressing Enter on a session row.
// Layout per wireframe: breadcrumb header + IDENTITY + SCORECARD + TREND +
// RECENT INTERVENTIONS + CURRENT IN-PROGRESS sections + footer.
type Detail struct {
	app *App

	root    *tview.Flex
	crumbs  *tview.TextView
	body    *tview.TextView
	footer  *tview.TextView
	current *state.Session
}

func newDetail(a *App) *Detail {
	d := &Detail{app: a}

	d.crumbs = tview.NewTextView().SetDynamicColors(true)
	d.crumbs.SetBackgroundColor(style.CrumbBg)

	d.body = tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true).
		SetScrollable(true)
	d.body.SetBackgroundColor(tcell.ColorBlack)
	d.body.SetBorderPadding(0, 0, 2, 2)

	d.footer = tview.NewTextView().SetDynamicColors(true)
	d.footer.SetBackgroundColor(tcell.ColorBlack)

	d.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(d.crumbs, 1, 0, false).
		AddItem(newBlankRow(), 1, 0, false).
		AddItem(d.body, 0, 1, true).
		AddItem(newBlankRow(), 1, 0, false).
		AddItem(d.footer, 1, 0, false)
	d.root.SetBackgroundColor(tcell.ColorBlack)

	d.body.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Rune() {
		case 't':
			a.TmuxAttachSelected()
			return nil
		case 'l':
			a.logMode.show()
			return nil
		case 'p':
			a.PauseSelected()
			a.tv.QueueUpdateDraw(func() { d.render(a.Selected()) })
			return nil
		case 'u':
			a.UnpauseSelected()
			a.tv.QueueUpdateDraw(func() { d.render(a.Selected()) })
			return nil
		case 'q':
			a.Quit()
			return nil
		case '?':
			a.help.show()
			return nil
		}
		if ev.Key() == tcell.KeyEsc {
			d.dismiss()
			return nil
		}
		return ev
	})
	return d
}

func (d *Detail) show(s *state.Session) {
	d.current = s
	d.render(s)
	d.app.pages.AddPage("detail", d.root, true, true)
	d.app.tv.SetFocus(d.body)
}

func (d *Detail) dismiss() {
	// SwitchToPage + RemovePage; no tv.Draw() — that re-enters the
	// tview loop from inside an input handler (Esc routed here from
	// SetInputCapture) and froze on the founder's TTY.
	d.app.pages.SwitchToPage("dashboard")
	d.app.pages.RemovePage("detail")
	d.app.tv.SetFocus(d.app.dashboard.list)
}

func (d *Detail) render(s *state.Session) {
	if s == nil {
		d.body.SetText(style.Tag(style.Ambient, "  (no session)"))
		return
	}

	// Breadcrumb: chepherd › sessions › openova-27
	d.crumbs.SetText(fmt.Sprintf(" %s %s %s %s %s",
		style.TagBg(style.CrumbFg, style.CrumbBg, "chepherd"),
		style.TagBg(style.CrumbSep, style.CrumbBg, "›"),
		style.TagBg(style.CrumbFg, style.CrumbBg, "sessions"),
		style.TagBg(style.CrumbSep, style.CrumbBg, "›"),
		style.TagBgBold(style.CrumbActive, style.CrumbBg, s.TmuxName)))

	var b strings.Builder
	w := &b

	// IDENTITY
	section(w, "IDENTITY", "────────")
	row(w, "tmux session", style.Tag(style.Primary, s.TmuxName))
	row(w, "uuid", style.Tag(style.IssueRef, truncOrAll(s.UUID, 36)))
	if s.LastInterventionAt != "" {
		row(w, "last intervene at", style.Tag(style.Timestamp, truncOrAll(s.LastInterventionAt, 19)))
	}
	row(w, "intervention count", style.Tag(style.Metric, fmt.Sprintf("%d", s.InterventionCount)))
	if isPaused(s) {
		row(w, "status", style.Tag(style.Pause, "paused (sentinel file present)"))
	}
	fmt.Fprintf(w, "\n\n")

	// SCORECARD
	section(w, "SCORECARD", "─────────")
	row(w, "band", formatBandText(s.Band))
	if s.NextTickAt != "" {
		if dt, err := time.Parse(time.RFC3339, s.NextTickAt); err == nil {
			d := time.Until(dt)
			row(w, "next tick", style.Tag(style.Metric, durationStr(d)))
		}
	}
	row(w, "last verdict", style.Tag(style.VerdictColor(s.LastVerdict), nonEmpty(s.LastVerdict, "—")))
	fmt.Fprintf(w, "\n")
	if s.LastScorecard != nil {
		axes := []struct{ name, key string }{
			{"G  goal     ", "G"},
			{"V  velocity ", "V"},
			{"F  focus    ", "F"},
			{"E  end-state", "E"},
		}
		for _, ax := range axes {
			n := s.LastScorecard[ax.key]
			fmt.Fprintf(w, "  %s   %s %s   %s\n",
				style.Tag(style.Primary, ax.name),
				style.TagBold(style.ScoreColor(n), fmt.Sprintf("%2d", n)),
				style.Tag(style.Ambient, "/ 10"),
				style.Tag(style.ScoreColor(n), gaugeBar(n, 10)))
		}
	}
	fmt.Fprintf(w, "\n\n")

	// TREND — sparkline of last 10 V scores
	if len(s.ScorecardHistory) > 0 {
		section(w, "TREND (last 10 ticks)", "─────────────────────")
		for _, ax := range []string{"G", "V", "F", "E"} {
			fmt.Fprintf(w, "  %s  %s\n",
				style.TagBold(style.Primary, ax),
				sparkLineForAxis(s.ScorecardHistory, ax, 10))
		}
		fmt.Fprintf(w, "\n\n")
	}

	// LAST COACH TOPIC
	if s.LastCoachTopic != "" {
		section(w, "LAST COACH TOPIC", "────────────────")
		fmt.Fprintf(w, "  %s\n", style.Tag(style.Primary, s.LastCoachTopic))
		fmt.Fprintf(w, "\n\n")
	}

	d.body.SetText(b.String())
	d.body.ScrollToBeginning()

	// Footer
	pairs := []struct{ k, desc string }{
		{"esc", "back"},
		{"t", "tmux attach"},
		{"l", "log"},
		{"p", "pause"},
		{"u", "unpause"},
		{"?", "help"},
		{"q", "quit"},
	}
	var fb strings.Builder
	fb.WriteString(" ")
	for i, p := range pairs {
		if i > 0 {
			fb.WriteString("    ")
		}
		fmt.Fprintf(&fb, "%s %s",
			style.TagBold(style.KeyLetter, p.k),
			style.Tag(style.KeyDesc, p.desc))
	}
	d.footer.SetText(fb.String())
}

// section writes a section title + dim rule below.
func section(w *strings.Builder, title, rule string) {
	fmt.Fprintf(w, "  %s\n", style.TagBold(style.Title, title))
	fmt.Fprintf(w, "  %s\n\n", style.Tag(style.TitleRule, rule))
}

// row writes "  label  :  value" with consistent label width.
func row(w *strings.Builder, label, value string) {
	fmt.Fprintf(w, "  %s :  %s\n",
		style.Tag(style.Primary, padLabel(label, 22)),
		value)
}

func padLabel(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func durationStr(d time.Duration) string {
	if d < 0 {
		return "now"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func truncOrAll(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// sparkLineForAxis renders a 10-bar sparkline for a given scorecard axis.
func sparkLineForAxis(hist []map[string]any, axis string, n int) string {
	if n > len(hist) {
		n = len(hist)
	}
	start := len(hist) - n
	if start < 0 {
		start = 0
	}
	bars := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, h := range hist[start:] {
		raw, ok := h[axis]
		if !ok {
			b.WriteString(" ")
			continue
		}
		var v int
		switch x := raw.(type) {
		case float64:
			v = int(x)
		case int:
			v = x
		case int64:
			v = int(x)
		}
		if v < 0 {
			v = 0
		}
		if v > 10 {
			v = 10
		}
		// Map 0-10 to 0-7 of bar height
		idx := v * (len(bars) - 1) / 10
		var color tcell.Color
		switch {
		case v <= 3:
			color = style.BandCrisis
		case v <= 6:
			color = style.BandConcerned
		default:
			color = style.BandTrusted
		}
		b.WriteString(style.Tag(color, string(bars[idx])))
	}
	return b.String()
}
