package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/chepherd/chepherd/internal/state"
	"github.com/chepherd/chepherd/internal/style"
)

// DaemonHealth describes the current liveness of the supervisor daemon.
// Read by Dashboard.render() and used to draw a banner when the daemon is
// down or stale, so users immediately see why the data isn't updating.
type DaemonHealth struct {
	// Running: at least one supervisor process is alive
	Running bool
	// Stale: state files exist but newest one's last_tick_at is > StaleThreshold
	Stale bool
	// StaleMinutes: how old the freshest state file is
	StaleMinutes float64
	// Source: which supervisor we detected ("chepherd" or "workflow" or "")
	Source string
}

// StaleThreshold — if no session was ticked in this long, daemon is considered stale.
// Adaptive cadence trusted-band is 30min, so we add a 5-min grace.
const StaleThreshold = 35 * time.Minute

// CheckDaemonHealth inspects state files + processes to determine whether
// the supervisor daemon is healthy. Cheap — runs every dashboard refresh.
func CheckDaemonHealth(sessions []*state.Session) DaemonHealth {
	h := DaemonHealth{}

	// 1. Process check — is supervisor.py or chepherd shadow running?
	out, _ := exec.Command("pgrep", "-f", "supervisor.py").Output()
	if len(out) > 0 {
		h.Running = true
		h.Source = "workflow"
	}
	out, _ = exec.Command("pgrep", "-f", "chepherd shadow").Output()
	if len(out) > 0 {
		h.Running = true
		if h.Source == "" {
			h.Source = "chepherd"
		}
	}

	// 2. Staleness — what's the freshest last_tick_at across all sessions?
	var newest time.Time
	for _, s := range sessions {
		// Look for refreshed_at in live_signals (chepherd live), else last_intervention_at.
		var ts time.Time
		if s.LiveSignals != nil && s.LiveSignals.RefreshedAt != "" {
			ts, _ = time.Parse(time.RFC3339Nano, s.LiveSignals.RefreshedAt)
			if ts.IsZero() {
				ts, _ = time.Parse(time.RFC3339, s.LiveSignals.RefreshedAt)
			}
		}
		if ts.IsZero() && s.LastInterventionAt != "" {
			ts, _ = time.Parse(time.RFC3339Nano, s.LastInterventionAt)
			if ts.IsZero() {
				ts, _ = time.Parse(time.RFC3339, s.LastInterventionAt)
			}
		}
		if ts.After(newest) {
			newest = ts
		}
	}
	if !newest.IsZero() {
		age := time.Since(newest)
		h.StaleMinutes = age.Minutes()
		if age > StaleThreshold {
			h.Stale = true
		}
	}
	return h
}

// FormatDaemonBanner returns a styled one-line banner about the daemon's
// state. Empty string when healthy (no banner shown). Pinned to the top
// of the dashboard between the header and the SESSIONS pane.
func FormatDaemonBanner(h DaemonHealth) string {
	if h.Running && !h.Stale {
		return "" // healthy — no banner needed
	}

	var icon, msg string
	var fg tcell.Color

	switch {
	case !h.Running:
		icon = "⚠"
		fg = style.BandCrisis
		msg = "supervisor daemon NOT RUNNING — showing last-known state from disk · " +
			"start it with: systemctl --user start chepherd"
	case h.Stale:
		icon = "◴"
		fg = style.BandConcerned
		msg = fmt.Sprintf(
			"supervisor stale — last tick %.0fm ago · "+
				"check logs: tail -F ~/.local/state/chepherd/chepherd.log",
			h.StaleMinutes)
	}

	return fmt.Sprintf(" %s  %s",
		style.TagBold(fg, icon),
		style.Tag(fg, msg))
}

// AttemptStartDaemon tries to start the daemon. Used by the "press s to start"
// shortcut in the dashboard when the daemon is detected as down.
func AttemptStartDaemon() (string, error) {
	// 1. Try systemd --user.
	if err := exec.Command("systemctl", "--user", "start", "chepherd").Run(); err == nil {
		return "systemctl --user start chepherd → success", nil
	}
	// 2. Fall back to inline shadow daemon if systemd unit isn't installed.
	chepherdBin, err := exec.LookPath("chepherd")
	if err != nil {
		home, _ := os.UserHomeDir()
		chepherdBin = filepath.Join(home, ".local", "bin", "chepherd")
	}
	if _, err := os.Stat(chepherdBin); err != nil {
		return "", fmt.Errorf("chepherd binary not found in PATH or ~/.local/bin")
	}
	// Spawn in background — detached.
	cmd := exec.Command(chepherdBin, "shadow")
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to spawn: %w", err)
	}
	return fmt.Sprintf("spawned chepherd shadow PID %d", cmd.Process.Pid), nil
}
