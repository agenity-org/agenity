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
	"sort"
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
	tv         *tview.Application
	pages      *tview.Pages
	dashboard  *Dashboard

	// Shared state — protected by mu.
	mu          sync.Mutex
	sessions    []*state.Session
	selectedIdx int

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
	a.pages.AddPage("dashboard", a.dashboard.root, true, true)
	return a
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
	// Stable alphabetical sort by tmux_name
	sort.Slice(sessions, func(i, j int) bool {
		ai, aj := sessions[i].TmuxName, sessions[j].TmuxName
		// Push paused sessions to the bottom.
		ip, jp := isPaused(sessions[i]), isPaused(sessions[j])
		if ip != jp {
			return !ip
		}
		return ai < aj
	})
	a.mu.Lock()
	a.sessions = sessions
	if a.selectedIdx >= len(sessions) {
		a.selectedIdx = 0
	}
	a.mu.Unlock()
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
	logo := style.Tag(style.Logo, "chepherd")
	ver := style.Tag(style.Logo, "0.0.1-dev")
	stats := style.Tag(style.Body,
		fmt.Sprintf(" ─ %d sessions ─ %d active ─ %s", total, active, now))
	fresh := style.Tag(style.Timestamp, " ─ refreshed 0s ago")
	return fmt.Sprintf("%s %s%s%s", logo, ver, stats, fresh)
}

// FormatFooter builds the bottom shortcut bar.
func (a *App) FormatFooter() string {
	pairs := []struct{ key, desc string }{
		{"↑↓", "select"},
		{"enter", "details"},
		{"t", "tmux"},
		{"l", "log"},
		{"p/u", "pause/unpause"},
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
