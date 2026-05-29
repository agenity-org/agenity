// internal/runtimehttp/oauth_code_sanitize_test.go — pins the #227
// invariants on Claude OAuth code sanitization. The actual claude-code
// CLI accepts a code shaped like `<base64>#<verifier>` and rejects
// anything that doesn't match — so the sanitizer must preserve the `#`
// boundary + the bytes on either side EXACTLY, while stripping the
// known UI-hint suffix + balancing-quote noise + whitespace runs that
// clipboards add.
//
// The synthetic codes used here NEVER pattern-match a real Anthropic
// OAuth token — the prefix `sk-ant-oat01-SYNTH-` is deliberately
// invalid so this fixture can ship in a public repo per #227's
// "synthetic format only" direction.
//
// Refs #227.
package runtimehttp

import (
	"testing"
)

func TestSanitizeOAuthCode_PreservesHashBoundary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "canonical clean code",
			in:   "sk-ant-oat01-SYNTH-PART-A-abc123#sk-ant-oat01-SYNTH-PART-B-xyz789",
			want: "sk-ant-oat01-SYNTH-PART-A-abc123#sk-ant-oat01-SYNTH-PART-B-xyz789",
		},
		{
			name: "leading/trailing whitespace stripped",
			in:   "   sk-ant-oat01-SYNTH-PART-A-abc#sk-ant-oat01-SYNTH-PART-B-xyz   ",
			want: "sk-ant-oat01-SYNTH-PART-A-abc#sk-ant-oat01-SYNTH-PART-B-xyz",
		},
		{
			name: "leading/trailing newlines stripped",
			in:   "\nsk-ant-oat01-SYNTH-PART-A-abc#sk-ant-oat01-SYNTH-PART-B-xyz\r\n",
			want: "sk-ant-oat01-SYNTH-PART-A-abc#sk-ant-oat01-SYNTH-PART-B-xyz",
		},
		{
			name: "internal whitespace dropped (word-wrap line break)",
			in:   "sk-ant-oat01-SYNTH-PART-A-abc\n#\nsk-ant-oat01-SYNTH-PART-B-xyz",
			want: "sk-ant-oat01-SYNTH-PART-A-abc#sk-ant-oat01-SYNTH-PART-B-xyz",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeOAuthCode(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeOAuthCode(%q)\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeOAuthCode_StripsUIHintSuffix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "lowercase hint at end",
			in:   "sk-ant-oat01-SYNTH-PART-A#sk-ant-oat01-SYNTH-PART-Bpastecodehereifprompted",
			want: "sk-ant-oat01-SYNTH-PART-A#sk-ant-oat01-SYNTH-PART-B",
		},
		{
			name: "mixed-case hint at end",
			in:   "sk-ant-oat01-SYNTH-PART-A#sk-ant-oat01-SYNTH-PART-BPasteCodeHereIfPrompted",
			want: "sk-ant-oat01-SYNTH-PART-A#sk-ant-oat01-SYNTH-PART-B",
		},
		{
			name: "hint with trailing whitespace",
			in:   "sk-ant-oat01-SYNTH-PART-A#sk-ant-oat01-SYNTH-PART-Bpastecodehereifprompted   ",
			want: "sk-ant-oat01-SYNTH-PART-A#sk-ant-oat01-SYNTH-PART-B",
		},
		{
			name: "hint NOT stripped from middle of code (would corrupt verifier)",
			in:   "sk-ant-pastecodehereifprompted-real-code#part-B",
			want: "sk-ant-pastecodehereifprompted-real-code#part-B",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeOAuthCode(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeOAuthCode(%q)\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeOAuthCode_StripsBalancedQuotes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "double quotes",
			in:   `"sk-ant-oat01-SYNTH-PART-A#sk-ant-oat01-SYNTH-PART-B"`,
			want: "sk-ant-oat01-SYNTH-PART-A#sk-ant-oat01-SYNTH-PART-B",
		},
		{
			name: "single quotes",
			in:   `'sk-ant-oat01-SYNTH-PART-A#sk-ant-oat01-SYNTH-PART-B'`,
			want: "sk-ant-oat01-SYNTH-PART-A#sk-ant-oat01-SYNTH-PART-B",
		},
		{
			name: "unbalanced quote at start NOT stripped",
			in:   `"sk-ant-oat01-SYNTH-PART-A#sk-ant-oat01-SYNTH-PART-B`,
			want: `"sk-ant-oat01-SYNTH-PART-A#sk-ant-oat01-SYNTH-PART-B`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeOAuthCode(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeOAuthCode(%q)\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeOAuthCode_EmptyAfterStrip(t *testing.T) {
	t.Parallel()
	// Pure whitespace + suffix → empty after sanitize → handler returns 400.
	cases := []string{
		"",
		"   ",
		"\n\r\t",
		"pastecodehereifprompted",
		"  PasteCodeHereIfPrompted  ",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := sanitizeOAuthCode(in); got != "" {
				t.Errorf("sanitizeOAuthCode(%q) = %q, want empty", in, got)
			}
		})
	}
}

// TestSanitizeOAuthCode_Idempotent — sanitizing an already-clean code
// returns the same value. Guards against future regex-style logic that
// could trim too greedily on a second pass.
func TestSanitizeOAuthCode_Idempotent(t *testing.T) {
	t.Parallel()
	clean := "sk-ant-oat01-SYNTH-PART-A-abc123#sk-ant-oat01-SYNTH-PART-B-xyz789"
	once := sanitizeOAuthCode(clean)
	twice := sanitizeOAuthCode(once)
	if once != clean {
		t.Errorf("first pass changed clean input: %q → %q", clean, once)
	}
	if twice != once {
		t.Errorf("second pass changed once-sanitized: %q → %q", once, twice)
	}
}
