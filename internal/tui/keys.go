package tui

// Centralised key binding table. Each screen reads from here so shortcuts
// stay consistent + the help overlay reflects truth (not separate strings).

// KeyBinding describes one key + what it does in a given context.
type KeyBinding struct {
	Key   string // human-friendly: "↑↓", "enter", "esc", "t"
	Desc  string // short verb: "select", "details", "tmux"
	Group string // "Navigation" / "Filtering" / "Actions" / "Views"
}

// DashboardKeys is what the W1 footer shows + what the help overlay groups.
var DashboardKeys = []KeyBinding{
	{Key: "↑↓", Desc: "select", Group: "Navigation"},
	{Key: "j/k", Desc: "select (vim)", Group: "Navigation"},
	{Key: "g/G", Desc: "first/last", Group: "Navigation"},
	{Key: "enter", Desc: "details", Group: "Navigation"},
	{Key: "tab", Desc: "cycle pane", Group: "Navigation"},
	{Key: "/", Desc: "filter list", Group: "Filtering"},
	{Key: "f", Desc: "filter events", Group: "Filtering"},
	{Key: "a", Desc: "all sessions log", Group: "Filtering"},
	{Key: "t", Desc: "tmux attach", Group: "Actions"},
	{Key: "p", Desc: "pause session", Group: "Actions"},
	{Key: "u", Desc: "unpause session", Group: "Actions"},
	{Key: "r", Desc: "refresh now", Group: "Actions"},
	{Key: "l", Desc: "log full-screen", Group: "Views"},
	{Key: "i", Desc: "issues drilldown", Group: "Views"},
	{Key: "?", Desc: "help", Group: "Views"},
	{Key: "q", Desc: "quit", Group: "Views"},
}

// FooterKeys returns just the 8 most-used keys for the bottom bar.
func FooterKeys() []KeyBinding {
	pick := []string{"↑↓", "enter", "t", "l", "p/u", "/", "?", "q"}
	out := make([]KeyBinding, 0, len(pick))
	for _, want := range pick {
		for _, kb := range DashboardKeys {
			if kb.Key == want {
				out = append(out, kb)
				break
			}
		}
		// Synthetic combos that aren't in the table verbatim:
		if want == "p/u" {
			out = append(out, KeyBinding{Key: "p/u", Desc: "pause/unpause"})
		}
	}
	return out
}
