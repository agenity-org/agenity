// Package tui implements the interactive terminal UI (k9s-inspired).
//
// The App owns the tview.Application, drives all screen transitions, and
// holds shared state: the currently-selected session, the live tailer for
// supervisor.log, the periodic state-refresh ticker, and the current view
// (dashboard / detail / log / filter / help).
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	chepherdlog "github.com/chepherd/chepherd/internal/log"
	"github.com/chepherd/chepherd/internal/state"
	"github.com/chepherd/chepherd/internal/style"
)

// RefreshInterval is how often the TUI re-reads state files from disk.
const RefreshInterval = 1 * time.Second

// LogPaneHistoryLines is how many lines to pre-load when the dashboard opens.
const LogPaneHistoryLines = 50

// App is the root TUI application.
type App struct {
	tv        *tview.Application
	pages     *tview.Pages
	dashboard *Dashboard
	help       *HelpOverlay
	filter     *Filter
	logMode    *LogMode
	detail      *Detail
	newSession  *NewSessionWizard
	attachModal *AttachModal // v0.3 — pre-attach hint shown on first 't' press

	// Shared state — protected by mu.
	mu          sync.Mutex
	sessions    []*state.Session
	allSessions []*state.Session // unfiltered
	filterText  string
	selectedIdx int
	sortMode    SortMode // cycled by 'o' hotkey, default SortScoreDesc

	// Log tailer
	logCtx    context.Context
	logCancel context.CancelFunc
	logCh     chan chepherdlog.Line
}

// New constructs a ready-to-Run App.
func New() *App {
	a := &App{
		tv:    tview.NewApplication(),
		pages: tview.NewPages(),
		logCh: make(chan chepherdlog.Line, 256),
	}
	a.dashboard = newDashboard(a)
	a.help = newHelpOverlay(a)
	a.filter = newFilter(a)
	a.logMode = newLogMode(a)
	a.detail = newDetail(a)
	a.newSession = newNewSessionWizard(a)
	a.attachModal = newAttachModal(a)
	a.pages.AddPage("dashboard", a.dashboard.root, true, true)
	a.installGlobalKeys()
	return a
}

// installGlobalKeys wires the dashboard-level shortcuts that open overlays.
func (a *App) installGlobalKeys() {
	a.dashboard.list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Rune() {
		case '/':
			a.filter.show()
			return nil
		case '?':
			a.help.show()
			return nil
		case 'l':
			a.logMode.show()
			return nil
		case 'p':
			a.PauseSelected()
			return nil
		case 'u':
			a.UnpauseSelected()
			return nil
		case 'r':
			a.refreshState()
			a.tv.QueueUpdateDraw(a.dashboard.render)
			return nil
		case 't':
			a.TmuxAttachSelected()
			return nil
		case 'L':
			// Capital-L = "login" — drop the user into the selected
			// session's pane + send '/login' so they can re-auth.
			// Only useful when the session is AuthLapsed.
			a.LoginSelected()
			return nil
		case 'o':
			// 'o' = order/sort cycle (score↓ → score↑ → name → status)
			a.cycleSort()
			return nil
		case 'n':
			a.newSession.show()
			return nil
		case 's':
			// W10 — attempt to start a stopped daemon.
			msg, err := AttemptStartDaemon()
			if err != nil {
				a.dashboard.appendLog("[daemon-start] FAILED: " + err.Error())
			} else {
				a.dashboard.appendLog("[daemon-start] " + msg)
			}
			return nil
		}
		if ev.Key() == tcell.KeyEnter {
			s := a.Selected()
			if s != nil {
				a.detail.show(s)
				return nil
			}
		}
		return ev
	})
}

// PauseSelected creates a sentinel file for the selected session.
func (a *App) PauseSelected() {
	s := a.Selected()
	if s == nil {
		return
	}
	dirs := state.DefaultStateDirs()
	if len(dirs) == 0 {
		return
	}
	path := dirs[0] + "/" + s.UUID + ".paused"
	_ = os.WriteFile(path, []byte{}, 0o600)
}

// UnpauseSelected removes the sentinel file for the selected session.
func (a *App) UnpauseSelected() {
	s := a.Selected()
	if s == nil {
		return
	}
	for _, dir := range state.DefaultStateDirs() {
		_ = os.Remove(dir + "/" + s.UUID + ".paused")
	}
}

// TmuxAttachSelected attempts to attach the user to the selected session's
// tmux pane. v0.3 flow:
//
//  1. Validate selection has a real tmux_name.
//  2. Show the pre-attach modal (or skip if user has dismissed it).
//  3. On confirm: save target's current status-right, set our
//     'Ctrl-B D → return to chepherd' reminder, register a set-hook to
//     restore on detach, then suspend + attach (or switch-client if nested).
//
// Errors are appended to the dashboard log pane.
func (a *App) TmuxAttachSelected() {
	s := a.Selected()
	if s == nil {
		a.dashboard.appendLog("[tmux-attach] no row selected")
		return
	}
	if s.TmuxName == "" || strings.HasSuffix(s.TmuxName, "…") {
		a.dashboard.appendLog(fmt.Sprintf(
			"[tmux-attach] session %q has no resolved tmux name "+
				"(state file missing tmux_name field — daemon needs restart on the new binary)",
			s.UUID))
		return
	}

	target := s.TmuxName
	a.attachModal.ShowOrAttach(target, func(attach bool, _ bool) {
		if !attach {
			return
		}
		a.performTmuxAttach(target)
	})
}

// performTmuxAttach does the actual attach, wrapping it in the
// status-right save/restore so the user sees a persistent reminder of
// the detach key while inside the session.
func (a *App) performTmuxAttach(target string) {
	// Save the user's current status-right so we can restore it on detach.
	// `tmux show-options -v` prints just the value (or empty if unset).
	origStatusRight := ""
	if out, err := exec.Command("tmux", "show-options", "-t", target,
		"-v", "status-right").Output(); err == nil {
		origStatusRight = strings.TrimRight(string(out), "\n")
	}

	// Set our reminder.
	reminder := "#[bg=red,fg=white,bold] Ctrl-B D → return to chepherd #[default]"
	_ = exec.Command("tmux", "set-option", "-t", target, "status-right", reminder).Run()

	// Restore on detach via a tmux client-detached hook. The hook fires
	// exactly when the user presses Ctrl-B D from inside this session.
	restoreCmd := fmt.Sprintf("set-option -t %s status-right %q",
		target, origStatusRight)
	_ = exec.Command("tmux", "set-hook", "-t", target,
		"client-detached", restoreCmd).Run()

	doAttach := func() {
		if os.Getenv("TMUX") != "" {
			if err := execCmd("tmux", "switch-client", "-t", target); err != nil {
				a.dashboard.appendLog(fmt.Sprintf(
					"[tmux-attach] switch-client -t %s failed: %v", target, err))
			}
			return
		}
		if err := execCmd("tmux", "attach", "-t", target); err != nil {
			a.dashboard.appendLog(fmt.Sprintf(
				"[tmux-attach] attach -t %s failed: %v", target, err))
		}
	}

	if os.Getenv("TMUX") != "" {
		// Nested case — switch-client doesn't suspend us; just call it.
		doAttach()
		return
	}
	a.tv.Suspend(doAttach)
}

// LoginSelected handles 'L' — drops the user into the selected session's
// tmux pane and sends the literal text "/login" + Enter so they can
// re-auth Claude. Used when the daemon flagged AuthLapsed=true.
func (a *App) LoginSelected() {
	s := a.Selected()
	if s == nil {
		a.dashboard.appendLog("[login] no row selected")
		return
	}
	if s.TmuxName == "" || strings.HasSuffix(s.TmuxName, "…") {
		a.dashboard.appendLog(fmt.Sprintf(
			"[login] session %q has no resolved tmux name — restart daemon",
			s.UUID))
		return
	}
	// Send '/login\n' to the session's input. Same pattern as tmuxPaste
	// in cmd/daemon.go but for a literal short string we can use send-keys.
	if err := execCmd("tmux", "send-keys", "-t", s.TmuxName, "/login", "Enter"); err != nil {
		a.dashboard.appendLog(fmt.Sprintf("[login] send-keys %s failed: %v", s.TmuxName, err))
		return
	}
	// Then attach / switch so the operator sees the OAuth prompt.
	if os.Getenv("TMUX") != "" {
		_ = execCmd("tmux", "switch-client", "-t", s.TmuxName)
	} else {
		a.tv.Suspend(func() {
			_ = execCmd("tmux", "attach", "-t", s.TmuxName)
		})
	}
}

func execCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// applyFilter updates the visible session set + repaints the dashboard.
func (a *App) applyFilter(query string) {
	a.mu.Lock()
	a.filterText = query
	a.applyFilterLocked()
	a.mu.Unlock()
	a.tv.QueueUpdateDraw(a.dashboard.render)
}

// applyFilterLocked recomputes a.sessions from a.allSessions + a.filterText.
// Caller MUST hold a.mu.
func (a *App) applyFilterLocked() {
	if a.filterText == "" {
		a.sessions = append([]*state.Session(nil), a.allSessions...)
		return
	}
	a.sessions = a.sessions[:0]
	for _, s := range a.allSessions {
		if matchesFilter(s, a.filterText) {
			a.sessions = append(a.sessions, s)
		}
	}
}

// Run blocks until the user quits the TUI.
func (a *App) Run() error {
	// Initial state load
	a.refreshState()

	// Start the periodic refresh ticker
	go a.tickerLoop()

	// Start the log tailer (auto-finds the right path)
	a.startLogTailer()

	// Start the log consumer (forwards lines into the dashboard's log view)
	go a.logConsumer()

	// v0.3 — start the center pane's capture-pane refresh loop
	a.dashboard.center.Start()

	a.tv.SetRoot(a.pages, true).EnableMouse(true)

	// Global key bindings — pages can intercept first.
	a.tv.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Rune() {
		case 'q', 'Q':
			a.Quit()
			return nil
		}
		switch ev.Key() {
		case tcell.KeyCtrlC:
			a.Quit()
			return nil
		}
		return ev
	})

	return a.tv.Run()
}

// Quit terminates the TUI cleanly + cancels background goroutines.
func (a *App) Quit() {
	if a.logCancel != nil {
		a.logCancel()
	}
	a.tv.Stop()
}

// ────────────────────────────────────────────────────────────────────────────
// State refresh
// ────────────────────────────────────────────────────────────────────────────

func (a *App) tickerLoop() {
	t := time.NewTicker(RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			a.refreshState()
			a.tv.QueueUpdateDraw(func() {
				a.dashboard.render()
			})
		}
	}
}

// refreshState re-reads all session JSON files + the cwd of each
// claude process to keep the session list current.
func (a *App) refreshState() {
	sessions, err := state.LoadAllSessions()
	if err != nil {
		// Soft failure — keep previous state, just log later if needed.
		return
	}
	a.mu.Lock()
	SortSessions(sessions, a.sortMode)
	a.allSessions = sessions
	a.applyFilterLocked()
	if a.selectedIdx >= len(a.sessions) {
		a.selectedIdx = 0
	}
	a.mu.Unlock()
}

// cycleSort advances the sort mode + re-sorts the current session list.
func (a *App) cycleSort() {
	a.mu.Lock()
	a.sortMode = a.sortMode.Next()
	SortSessions(a.allSessions, a.sortMode)
	a.applyFilterLocked()
	a.mu.Unlock()
	a.tv.QueueUpdateDraw(a.dashboard.render)
}

// SortMode returns the current sort mode for header rendering.
func (a *App) SortMode() SortMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sortMode
}

// isPaused checks for a sentinel file at $XDG_STATE/chepherd/sessions/<uuid>.paused
// OR $XDG_STATE/workflow/sessions/<uuid>.paused (legacy).
func isPaused(s *state.Session) bool {
	for _, dir := range state.DefaultStateDirs() {
		if _, err := os.Stat(fmt.Sprintf("%s/%s.paused", dir, s.UUID)); err == nil {
			return true
		}
	}
	return false
}

// ────────────────────────────────────────────────────────────────────────────
// Log tailing
// ────────────────────────────────────────────────────────────────────────────

func (a *App) startLogTailer() {
	a.logCtx, a.logCancel = context.WithCancel(context.Background())
	go func() {
		// Pick the first existing log path; tail it.
		for _, path := range chepherdlog.DefaultLogPaths() {
			if _, err := os.Stat(path); err == nil {
				chepherdlog.Tail(a.logCtx, path, LogPaneHistoryLines, a.logCh)
				return
			}
		}
	}()
}

func (a *App) logConsumer() {
	for line := range a.logCh {
		l := line
		a.tv.QueueUpdateDraw(func() {
			a.dashboard.appendLog(l.Text)
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Session access helpers — thread-safe accessors
// ────────────────────────────────────────────────────────────────────────────

// Sessions returns a defensive copy of the current session list.
func (a *App) Sessions() []*state.Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*state.Session, len(a.sessions))
	copy(out, a.sessions)
	return out
}

// Selected returns the currently-selected session, or nil if empty.
func (a *App) Selected() *state.Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sessions) == 0 || a.selectedIdx >= len(a.sessions) {
		return nil
	}
	return a.sessions[a.selectedIdx]
}

// Select sets the selected index (clamped to valid range).
func (a *App) Select(idx int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if idx < 0 {
		idx = 0
	}
	if idx >= len(a.sessions) {
		idx = len(a.sessions) - 1
	}
	a.selectedIdx = idx
}

// FormatHeader builds the top status-bar text.
// Layout: left wordmark + stats + current sort mode + clock, with the
// tiny right-anchored brand mark `▰ chepherd 0.3` rendered separately by
// Dashboard.render (it needs to know the actual rendered width).
func (a *App) FormatHeader() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	total := len(a.sessions)
	active := 0
	for _, s := range a.sessions {
		if !isPaused(s) {
			active++
		}
	}
	now := time.Now().UTC().Format("15:04:05 UTC")
	logo := style.TagBold(style.Logo, "chepherd")
	stats := style.Tag(style.Body,
		fmt.Sprintf("  ·  %d sessions · %d active · sort: %s · %s",
			total, active, a.sortMode, now))
	return logo + stats
}

// FormatHeaderRight returns the tiny right-anchored brand mark + version
// per the v0.3 spec. Uses '*' instead of the U+25B0 block character
// because some terminal fonts don't carry the latter and the founder
// reported the logo invisible at first launch.
func (a *App) FormatHeaderRight() string {
	return style.TagBold(style.Logo, "* chepherd 0.3 ")
}

// FormatFooter builds the bottom shortcut bar.
func (a *App) FormatFooter() string {
	pairs := []struct{ key, desc string }{
		{"↑↓", "select"},
		{"enter", "details"},
		{"t", "attach"},
		{"o", "sort"},
		{"L", "login"},
		{"l", "log"},
		{"p/u", "pause"},
		{"/", "filter"},
		{"?", "help"},
		{"q", "quit"},
	}
	var b strings.Builder
	b.WriteString(" ")
	for i, p := range pairs {
		if i > 0 {
			b.WriteString("    ")
		}
		b.WriteString(style.TagBold(style.KeyLetter, p.key))
		b.WriteString(" ")
		b.WriteString(style.Tag(style.KeyDesc, p.desc))
	}
	return b.String()
}
