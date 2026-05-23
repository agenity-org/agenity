package style

import (
	"fmt"
	"os"

	"github.com/gdamore/tcell/v2"
)

// useANSI reports whether direct ANSI output is desired for the current
// stdout. Honours the NO_COLOR convention + FORCE_COLOR override.
func useANSI() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

var ansiEnabled = useANSI()

// Reset returns the ANSI escape to clear all attributes.
func Reset() string {
	if !ansiEnabled {
		return ""
	}
	return "\033[0m"
}

// Color emits an ANSI fg color sequence (true-color, 24-bit) for the given tcell.Color.
func Color(c tcell.Color) string {
	if !ansiEnabled {
		return ""
	}
	if c == tcell.ColorDefault || c == tcell.ColorBlack {
		return ""
	}
	r, g, b := c.RGB()
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
}

// Bold returns the ANSI bold attribute (combine with Color).
func Bold() string {
	if !ansiEnabled {
		return ""
	}
	return "\033[1m"
}

// Dim returns the ANSI dim attribute.
func Dim() string {
	if !ansiEnabled {
		return ""
	}
	return "\033[2m"
}

// Sprint formats text with a single foreground color.
func Sprint(c tcell.Color, text string) string {
	return Color(c) + text + Reset()
}

// SprintBold combines bold + color.
func SprintBold(c tcell.Color, text string) string {
	return Bold() + Color(c) + text + Reset()
}
