// internal/runtime/knock/knock_test.go — #472 Wave K1 unit tests.
//
// Named assertions K1.U1-U6:
//
//	U1 — FormatKnock returns exact wire format per §10 step 12
//	U2 — ParseKnock round-trips Format output
//	U3 — ParseKnock anchors substring (handles leading ANSI noise)
//	U4 — ParseKnock rejects malformed markers (no false-positive)
//	U5 — ContainsKnock fast-path matches MarkerPrefix
//	U6 — Marker constant is operator-locked (don't accidentally
//	     change the format string)
//
// Refs #472.
package knock

import (
	"strings"
	"testing"
)

func TestK1_U1_FormatExactWireFormat(t *testing.T) {
	got := FormatKnock("019e7e2d-74fb-7b47-8dca-f35a2228ac10", "alpha")
	want := "[chepherd-knock taskID=019e7e2d-74fb-7b47-8dca-f35a2228ac10 from=alpha]\n"
	if got != want {
		t.Errorf("U1 FAIL: FormatKnock = %q, want %q", got, want)
	}
}

func TestK1_U2_RoundTrip(t *testing.T) {
	const taskID = "019e7e2d-74fb-7b47-8dca-f35a2228ac10"
	const from = "iogrid-1"
	line := FormatKnock(taskID, from)
	gotID, gotFrom, ok := ParseKnock([]byte(line))
	if !ok {
		t.Fatalf("U2 FAIL: ParseKnock returned ok=false on FormatKnock output: %q", line)
	}
	if gotID != taskID {
		t.Errorf("U2 FAIL: taskID = %q, want %q", gotID, taskID)
	}
	if gotFrom != from {
		t.Errorf("U2 FAIL: from = %q, want %q", gotFrom, from)
	}
}

func TestK1_U3_SubstringAnchoredAgainstANSI(t *testing.T) {
	// Real claude-TUI noise: cursor moves, color escapes, prompt
	// glyph, then a knock interleaved into a log line.
	noise := "\x1b[2J\x1b[H\x1b[33mℹ\x1b[0m daemon idle 30s "
	knock := FormatKnock("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "scrum-master")
	combined := noise + knock + "\x1b[34m▌\x1b[0m"
	id, from, ok := ParseKnock([]byte(combined))
	if !ok {
		t.Fatalf("U3 FAIL: ParseKnock missed knock embedded in ANSI noise: %q", combined)
	}
	if id != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" || from != "scrum-master" {
		t.Errorf("U3 FAIL: id=%q from=%q", id, from)
	}
}

func TestK1_U4_RejectsMalformed(t *testing.T) {
	for _, bad := range [][]byte{
		[]byte(""),
		[]byte("just a log line"),
		[]byte("[chepherd-knock taskID= from=alpha]"), // empty taskID
		[]byte("[chepherd-knock taskID=abc from=]"),    // empty from
		[]byte("[chepherd-knock from=alpha taskID=abc]"), // field order swapped
		[]byte("[chepherd-knock taskID=abc from=bob;drop table]"), // illegal char in from
	} {
		_, _, ok := ParseKnock(bad)
		if ok {
			t.Errorf("U4 FAIL: ParseKnock accepted malformed input: %q", bad)
		}
	}
}

func TestK1_U5_ContainsKnockFastPath(t *testing.T) {
	cases := map[string]bool{
		"":                                  false,
		"[chepherd-knock taskID=x from=y]\n": true,
		"prefix [chepherd-knock taskID=x from=y] suffix": true,
		"[chepherd-knock-something-else]": false, // missing trailing space — not the prefix
		"[chepherd-knock other-field=x]": true,   // has the prefix space — fast path accepts (regex will reject downstream)
		"not a knock":                     false,
	}
	for input, want := range cases {
		got := ContainsKnock([]byte(input))
		if got != want {
			t.Errorf("U5 FAIL: ContainsKnock(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestK1_U6_MarkerConstantLocked(t *testing.T) {
	// The literal string is operator-locked — claude-code's pattern
	// detector + future federation knock receivers depend on these
	// exact bytes. If this test fails, you renamed the marker
	// format — file an ADR + bump K1's wire-version label.
	const want = "[chepherd-knock taskID=%s from=%s]\n"
	if Marker != want {
		t.Errorf("U6 FAIL: Marker = %q, want %q (wire-locked per §10 step 12)", Marker, want)
	}
	if !strings.HasPrefix(Marker, MarkerPrefix) {
		t.Errorf("U6 FAIL: MarkerPrefix %q does not prefix Marker %q", MarkerPrefix, Marker)
	}
}
