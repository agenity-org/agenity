// Package style is the canonical color + style spec for chepherd.
// Mirrors the k9s stock.yaml palette (verified against derailed/k9s upstream).
//
// All UI code MUST reference these constants — never hard-code colors.
// Re-skinning later is a single-file change.
package style

import (
	"github.com/gdamore/tcell/v2"
)

// ────────────────────────────────────────────────────────────────────────────
// Color palette — k9s stock skin verbatim
// ────────────────────────────────────────────────────────────────────────────

// Body / ambient
var (
	Body       = mustColor("#5F9EA0") // cadetblue — ambient default text
	Primary    = mustColor("#FFFFFF") // white — primary readable text
	Background = tcell.ColorBlack     // black — universal background
)

// Brand
var (
	Logo = mustColor("#FFA500") // orange — chepherd logo + brand pop
)

// Frame chrome
var (
	Title       = mustColor("#00FFFF") // aqua — section / pane titles
	TitleRule   = mustColor("#87CEFA") // lightskyblue — the ─── under titles
	Border      = mustColor("#1E90FF") // dodgerblue — pane borders
	BorderFocus = mustColor("#87CEFA") // lightskyblue — focused border
)

// Menu / footer
var (
	KeyLetter  = mustColor("#1E90FF") // dodgerblue — bold key letter
	KeyDesc    = mustColor("#FFFFFF") // white — shortcut description
	KeyNumeric = mustColor("#FF00FF") // fuchsia — numeric key (rare)
)

// Breadcrumbs
var (
	CrumbFg     = tcell.ColorBlack     // black
	CrumbBg     = mustColor("#4682B4") // steelblue
	CrumbActive = mustColor("#FFA500") // orange — current leaf
	CrumbSep    = mustColor("#3A4A5A") // dim — separator chars
)

// ────────────────────────────────────────────────────────────────────────────
// Semantic — band, scorecard, verdict, event colors
// ────────────────────────────────────────────────────────────────────────────

// Trust bands
var (
	BandTrusted   = mustColor("#ADFF2F") // greenyellow — healthy
	BandStandard  = mustColor("#5F9EA0") // cadetblue — neutral
	BandConcerned = mustColor("#FF8C00") // darkorange — warning
	BandCrisis    = mustColor("#FF4500") // orangered — critical
	BandPaused    = mustColor("#778899") // lightslategray — dormant
)

// Verdict
var (
	VerdictSilent    = BandStandard         // cadetblue — silent is neutral
	VerdictPraise    = mustColor("#ADFF2F") // greenyellow
	VerdictCoach     = BandConcerned        // darkorange
	VerdictIntervene = BandCrisis           // orangered
)

// Special events
var (
	Injected    = mustColor("#9370DB") // mediumpurple — coach-message landed
	Escalating  = mustColor("#FFEFD5") // papayawhip — model upgraded
	APIError    = mustColor("#FF4500") // orangered — judge call failed
	Pause       = mustColor("#778899") // lightslategray — paused session
	Adopted     = mustColor("#00CED1") // darkturquoise — externally-started
	TmuxAttach  = mustColor("#87CEFA") // lightskyblue — switching surface
)

// Numbers + references
var (
	Metric   = mustColor("#FFEFD5") // papayawhip — counts / numeric metrics
	IssueRef = mustColor("#4682B4") // steelblue — #1234 style refs
	Marked   = mustColor("#B8860B") // darkgoldenrod — flagged item
)

// Age fields — color by recency
var (
	AgeFresh = mustColor("#ADFF2F") // <5 min
	AgeWarn  = mustColor("#FF8C00") // 5-30 min
	AgeStale = mustColor("#FF4500") // >30 min
)

// Cost
var (
	CostLow  = mustColor("#ADFF2F") // <$0.10
	CostMid  = mustColor("#FF8C00") // $0.10-$0.20
	CostHigh = mustColor("#FF4500") // >$0.20
)

// History timestamps + dim metadata
var (
	Timestamp = mustColor("#778899") // lightslategray
	Ambient   = mustColor("#5F9EA0") // cadetblue — same as Body
	Subdued   = mustColor("#3A4A5A") // grey-40 for separators
)

// Selection (k9s table cursor pattern)
var (
	SelectedFg = tcell.ColorBlack
	SelectedBg = mustColor("#00FFFF") // aqua
)

// ────────────────────────────────────────────────────────────────────────────
// Scorecard digit / gauge bar coloring helpers
// ────────────────────────────────────────────────────────────────────────────

// ScoreColor returns the band color for a 0-10 axis score.
//
//	0-3 → orangered (crisis-tier)
//	4-6 → darkorange (concerned-tier)
//	7-10 → greenyellow (healthy-tier)
func ScoreColor(score int) tcell.Color {
	switch {
	case score <= 3:
		return BandCrisis
	case score <= 6:
		return BandConcerned
	default:
		return BandTrusted
	}
}

// AgeColor returns the freshness color for an age in minutes.
func AgeColor(minutes float64) tcell.Color {
	switch {
	case minutes < 5:
		return AgeFresh
	case minutes < 30:
		return AgeWarn
	default:
		return AgeStale
	}
}

// CostColor returns the bucket color for a per-call USD cost.
func CostColor(usd float64) tcell.Color {
	switch {
	case usd < 0.10:
		return CostLow
	case usd < 0.20:
		return CostMid
	default:
		return CostHigh
	}
}

// BandColor maps a trust-band name to its color.
func BandColor(band string) tcell.Color {
	switch band {
	case "trusted":
		return BandTrusted
	case "concerned":
		return BandConcerned
	case "crisis":
		return BandCrisis
	case "paused":
		return BandPaused
	case "standard":
		fallthrough
	default:
		return BandStandard
	}
}

// VerdictColor maps a verdict literal to its color.
func VerdictColor(verdict string) tcell.Color {
	switch verdict {
	case "intervene":
		return VerdictIntervene
	case "coach":
		return VerdictCoach
	case "praise":
		return VerdictPraise
	case "silent":
		fallthrough
	default:
		return VerdictSilent
	}
}

// ────────────────────────────────────────────────────────────────────────────
// tview tag helpers — produce "[fg:bg:attr]…[-:-:-]" strings
// ────────────────────────────────────────────────────────────────────────────

// Tag emits a tview color tag for foreground only.
// Use the result inside tview.TextView.SetDynamicColors(true) sinks.
//
//	view.SetText(style.Tag(style.Title, "SESSIONS"))
func Tag(c tcell.Color, text string) string {
	return "[" + colorTag(c) + "]" + text + "[-]"
}

// TagBold emits a tview color tag with bold attribute.
func TagBold(c tcell.Color, text string) string {
	return "[" + colorTag(c) + "::b]" + text + "[-:-:-]"
}

// TagBg emits a tview tag with explicit fg + bg + attribute reset.
func TagBg(fg, bg tcell.Color, text string) string {
	return "[" + colorTag(fg) + ":" + colorTag(bg) + "]" + text + "[-:-:-]"
}

// TagBgBold emits a tview tag with fg + bg + bold.
func TagBgBold(fg, bg tcell.Color, text string) string {
	return "[" + colorTag(fg) + ":" + colorTag(bg) + ":b]" + text + "[-:-:-]"
}

// colorTag converts a tcell.Color to a tview color tag string ("#rrggbb" or named).
func colorTag(c tcell.Color) string {
	if c == tcell.ColorDefault {
		return ""
	}
	if c == tcell.ColorBlack {
		return "black"
	}
	r, g, b := c.RGB()
	return rgbHex(r, g, b)
}

func rgbHex(r, g, b int32) string {
	const hex = "0123456789abcdef"
	out := []byte("#000000")
	out[1] = hex[(r>>4)&0xf]
	out[2] = hex[r&0xf]
	out[3] = hex[(g>>4)&0xf]
	out[4] = hex[g&0xf]
	out[5] = hex[(b>>4)&0xf]
	out[6] = hex[b&0xf]
	return string(out)
}

func mustColor(hex string) tcell.Color {
	// hex must be "#rrggbb"
	if len(hex) != 7 || hex[0] != '#' {
		panic("style: invalid hex color: " + hex)
	}
	parseNibble := func(b byte) int32 {
		switch {
		case b >= '0' && b <= '9':
			return int32(b - '0')
		case b >= 'a' && b <= 'f':
			return int32(b-'a') + 10
		case b >= 'A' && b <= 'F':
			return int32(b-'A') + 10
		default:
			panic("style: invalid hex digit: " + string(b))
		}
	}
	r := parseNibble(hex[1])<<4 | parseNibble(hex[2])
	g := parseNibble(hex[3])<<4 | parseNibble(hex[4])
	b := parseNibble(hex[5])<<4 | parseNibble(hex[6])
	return tcell.NewRGBColor(r, g, b)
}
