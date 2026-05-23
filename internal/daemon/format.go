package daemon

import (
	"fmt"
	"strings"
)

// FormatSignalsForPrompt is the Go port of supervisor.py's
// _fmt_signals_for_prompt(). Output must match the Python format
// byte-for-byte so the same judge.md system prompt produces comparable
// verdicts during shadow-mode A/B testing.
func FormatSignalsForPrompt(s *Session, sig *Signals) string {
	var b strings.Builder
	w := &b

	fmt.Fprintf(w, "# Session: %s  (repo=%s, uuid=%s)\n",
		s.TmuxName, s.Repo, firstN(s.UUID, 8))
	fmt.Fprintf(w, "now: %s\n", sig.Now.Format("2006-01-02T15:04:05"))
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "## Signals\n")
	fmt.Fprintf(w, "- last_tool_call_age: %s\n", fmtAge(sig.LastToolCallAgeMin))
	fmt.Fprintf(w, "- last_founder_directive_age: %s\n", fmtAge(sig.LastFounderDirectiveAgeMin))
	fmt.Fprintf(w, "- last_assistant_ends_with_tool_call: %v\n", sig.LastAssistantEndsWithTool)
	fmt.Fprintf(w, "- monitor_running: %v\n", sig.MonitorRunning)
	fmt.Fprintf(w, "- git_last_commit_age: %s\n", fmtAge(sig.GitLastCommitAgeMin))
	fmt.Fprintf(w, "- tracker_mtime_age: %s\n", fmtAge(sig.TrackerMtimeAgeMin))
	fmt.Fprintf(w, "- in_progress_count: %d  (backlog: %d)\n", sig.InProgressCount, sig.BacklogCount)
	if len(sig.BannedPhraseHits) == 0 {
		fmt.Fprintf(w, "- banned_phrase_hits: none\n")
	} else {
		fmt.Fprintf(w, "- banned_phrase_hits: %v\n", sig.BannedPhraseHits)
	}
	fmt.Fprintf(w, "- pause_detected: %v\n", sig.PauseDetected)
	if sig.LastSupervisorMessage == "" {
		fmt.Fprintf(w, "- last_supervisor_message: none\n")
	} else {
		fmt.Fprintf(w, "- last_supervisor_message: %s\n", sig.LastSupervisorMessage)
	}
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "## Mission context\n")
	if len(sig.CurrentInProgressTitles) > 0 {
		fmt.Fprintf(w, "Current in-progress issues:\n")
		for _, t := range sig.CurrentInProgressTitles {
			fmt.Fprintf(w, "  - %s\n", t)
		}
	} else {
		fmt.Fprintf(w, "Current in-progress issues: (none — count=0)\n")
	}
	fmt.Fprintf(w, "Unclaimed backlog (open issues with NO status/* label, count=%d):\n",
		sig.UnclaimedBacklogCount)
	for _, t := range sig.UnclaimedBacklogTitles {
		fmt.Fprintf(w, "  - %s\n", t)
	}
	if len(sig.UnclaimedBacklogTitles) == 0 {
		fmt.Fprintf(w, "  (none unclaimed — either zero open issues or all are "+
			"status/parked|uat|blocked-ext|completed)\n")
	}
	fmt.Fprintf(w, "Recent commits (last 60min, %d):\n", len(sig.CommitsLastHour))
	for i, c := range sig.CommitsLastHour {
		if i >= 8 {
			break
		}
		fmt.Fprintf(w, "  - %s\n", c)
	}
	if len(sig.CommitsLastHour) == 0 {
		fmt.Fprintf(w, "  (none)\n")
	}
	fmt.Fprintf(w, "Recent PR activity (last 60min, %d):\n", len(sig.PRsLastHour))
	for i, p := range sig.PRsLastHour {
		if i >= 5 {
			break
		}
		fmt.Fprintf(w, "  - %s\n", p)
	}
	if len(sig.PRsLastHour) == 0 {
		fmt.Fprintf(w, "  (none)\n")
	}
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "## Anti-theater context\n")
	if sig.LastCoachTopic == "" {
		fmt.Fprintf(w, "- last_coach_topic: (no prior coach in 90min)\n")
	} else {
		fmt.Fprintf(w, "- last_coach_topic: %s\n", sig.LastCoachTopic)
	}
	if sig.LastCoachAt == "" {
		fmt.Fprintf(w, "- last_coach_at: (none)\n")
	} else {
		fmt.Fprintf(w, "- last_coach_at: %s\n", sig.LastCoachAt)
	}
	addr := "<nil>"
	if sig.AddressedLastCoach != nil {
		addr = fmt.Sprintf("%v", *sig.AddressedLastCoach)
	}
	fmt.Fprintf(w, "- addressed_last_coach: %s  "+
		"(true = >=1 commit since coach; false = busy elsewhere or silent)\n", addr)
	fmt.Fprintf(w, "- quiet_ratio_last_30min: %.2f  "+
		"(0.0 = always active; 1.0 = silent all window; >0.30 is burst-idle pattern)\n",
		sig.QuietRatioLast30Min)
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "## Last ~20 events (oldest → newest)\n")
	for _, ev := range sig.Events {
		fmt.Fprintf(w, "[%s] %s(%s): %s\n",
			ev.Ts.Format("15:04:05"),
			strings.ToUpper(ev.Role), ev.Kind, ev.Summary)
	}
	return b.String()
}

func fmtAge(p *float64) string {
	if p == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f min", *p)
}
