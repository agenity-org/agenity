package tui

import (
	"sort"

	"github.com/agenity-org/agenity/internal/state"
)

// SortMode is one of 4 sort orderings for the session list.
// Cycled by the 'o' hotkey in the dashboard.
type SortMode int

const (
	// SortScoreDesc — geomean descending: worst sessions first (triage view).
	// This is the default — the broken session is always at the top.
	SortScoreDesc SortMode = iota
	// SortScoreAsc — geomean ascending: best sessions first.
	SortScoreAsc
	// SortName — alphabetical by tmux_name.
	SortName
	// SortStatus — crisis → concerned → standard → trusted → paused.
	SortStatus
)

// String returns the short label rendered in the header text.
func (m SortMode) String() string {
	switch m {
	case SortScoreDesc:
		return "score↓"
	case SortScoreAsc:
		return "score↑"
	case SortName:
		return "name"
	case SortStatus:
		return "status"
	default:
		return "?"
	}
}

// Next returns the next sort mode in the cycle.
func (m SortMode) Next() SortMode {
	return SortMode((int(m) + 1) % 4)
}

// statusRank maps band -> sort rank (lower = surfaces first). Crisis sessions
// surface first when sorting by status; paused sessions sink to the bottom.
func statusRank(s *state.Session) int {
	if isPaused(s) {
		return 5
	}
	switch s.Band {
	case "crisis":
		return 0
	case "concerned":
		return 1
	case "standard":
		return 2
	case "trusted":
		return 3
	default:
		return 4 // unknown band
	}
}

// SortSessions sorts the slice in place using the given mode. Stable sort so
// equal-key sessions keep their prior relative order (looks calmer on screen
// when scores tie).
func SortSessions(sessions []*state.Session, mode SortMode) {
	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		// Paused sessions always sink to the bottom regardless of mode —
		// they're not actionable. Same convention the prior dashboard had.
		ap, bp := isPaused(a), isPaused(b)
		if ap != bp {
			return !ap
		}
		switch mode {
		case SortScoreDesc:
			return a.Geomean() > b.Geomean()
		case SortScoreAsc:
			ag, bg := a.Geomean(), b.Geomean()
			// Sessions with no scorecard (Geomean == -1) sink to the bottom
			// even in ascending mode — "no data" is not "lowest score".
			if ag < 0 && bg >= 0 {
				return false
			}
			if bg < 0 && ag >= 0 {
				return true
			}
			return ag < bg
		case SortName:
			return a.TmuxName < b.TmuxName
		case SortStatus:
			ra, rb := statusRank(a), statusRank(b)
			if ra != rb {
				return ra < rb
			}
			return a.TmuxName < b.TmuxName
		}
		return a.TmuxName < b.TmuxName
	})
}
