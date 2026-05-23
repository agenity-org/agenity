package daemon

import (
	"strings"
	"time"
)

// TrustBand maps a session's recent verdict history + scorecard trend to
// the next-tick interval. Exact same logic as Python supervisor's
// _compute_trust_band() so the Go shadow daemon produces comparable bands.
type TrustBand string

const (
	BandTrusted   TrustBand = "trusted"
	BandStandard  TrustBand = "standard"
	BandConcerned TrustBand = "concerned"
	BandCrisis    TrustBand = "crisis"
)

// Intervals (minutes) per band.
var BandIntervalMin = map[TrustBand]int{
	BandTrusted:   30,
	BandStandard:  10,
	BandConcerned: 5,
	BandCrisis:    2,
}

// ComputeBand returns the new band + next-tick-interval-minutes for a
// session, given prior state + current signals + the just-computed verdict.
func ComputeBand(state map[string]any, sig *Signals, v *Verdict) (TrustBand, int) {
	// Recent verdict history including the current one.
	var vh []string
	if raw, ok := state["verdict_history"].([]any); ok {
		for _, x := range raw {
			if s, ok := x.(string); ok {
				vh = append(vh, s)
			}
		}
	}
	vh = append(vh, v.Verdict)
	if len(vh) > 5 {
		vh = vh[len(vh)-5:]
	}
	recentIntervene := 0
	for _, s := range vh {
		if s == "intervene" {
			recentIntervene++
		}
	}

	sameTopicCount := 0
	if n, ok := state["same_topic_count"].(float64); ok {
		sameTopicCount = int(n)
	}

	pref := v.PrincipleRef
	isTheater := strings.Contains(pref, "P9") && strings.Contains(pref, "P14")

	// CRISIS triggers.
	if isTheater || recentIntervene >= 2 || sameTopicCount >= 3 {
		return BandCrisis, BandIntervalMin[BandCrisis]
	}

	// Current verdict = intervene → at least concerned.
	if v.Verdict == "intervene" {
		return BandConcerned, BandIntervalMin[BandConcerned]
	}

	// Mean V across recent ticks.
	var vSum, vN int
	if raw, ok := state["scorecard_history"].([]any); ok {
		start := len(raw) - 5
		if start < 0 {
			start = 0
		}
		for _, x := range raw[start:] {
			if h, ok := x.(map[string]any); ok {
				if vv, ok := h["V"].(float64); ok {
					vSum += int(vv)
					vN++
				}
			}
		}
	}
	if v.Scorecard != nil {
		if vv, ok := v.Scorecard["V"]; ok {
			vSum += vv
			vN++
		}
	}
	meanV := 5
	if vN > 0 {
		meanV = vSum / vN
	}

	// CONCERNED triggers: low velocity, unaddressed coach, burst-idle.
	if meanV < 4 ||
		(sig.AddressedLastCoach != nil && !*sig.AddressedLastCoach) ||
		sig.QuietRatioLast30Min > 0.30 {
		return BandConcerned, BandIntervalMin[BandConcerned]
	}

	// Fresh founder directive — stay alert at standard at least.
	freshDirective := sig.LastFounderDirectiveAgeMin != nil && *sig.LastFounderDirectiveAgeMin < 10

	// TRUSTED: high steady V, no recent intervene, no fresh directive.
	if meanV >= 7 && recentIntervene == 0 && !freshDirective {
		return BandTrusted, BandIntervalMin[BandTrusted]
	}

	return BandStandard, BandIntervalMin[BandStandard]
}

// RecordVerdictToState appends the verdict's metadata to a running state map.
func RecordVerdictToState(state map[string]any, sig *Signals, v *Verdict) {
	state["last_verdict"] = v.Verdict
	state["last_tick_at"] = sig.Now.UTC().Format(time.RFC3339)
	vh := getStringList(state, "verdict_history")
	vh = append(vh, v.Verdict)
	if len(vh) > 20 {
		vh = vh[len(vh)-20:]
	}
	state["verdict_history"] = vh
	if v.Scorecard != nil {
		state["last_scorecard"] = mapStringInt(v.Scorecard)
		sch := getMapList(state, "scorecard_history")
		entry := map[string]any{"at": sig.Now.UTC().Format(time.RFC3339)}
		for k, val := range v.Scorecard {
			entry[k] = val
		}
		sch = append(sch, entry)
		if len(sch) > 20 {
			sch = sch[len(sch)-20:]
		}
		state["scorecard_history"] = sch
	}
}

func getStringList(state map[string]any, key string) []string {
	var out []string
	raw, ok := state[key].([]any)
	if !ok {
		return out
	}
	for _, x := range raw {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func getMapList(state map[string]any, key string) []map[string]any {
	var out []map[string]any
	raw, ok := state[key].([]any)
	if !ok {
		return out
	}
	for _, x := range raw {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func mapStringInt(m map[string]int) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}
