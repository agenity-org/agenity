package mcpserver

import "testing"

// TestApplyAutoEnvelope locks the deterministic #203 contract:
//   - body starting with "[@"        → unchanged
//   - empty caller                   → unchanged
//   - any other caller + bare body   → "[@<caller>] " + body
//
// Architect's #203 brief integration-test acceptance: peer-to-peer
// receives "[@<sender>] <body>"; shepherd → adam with explicit
// "[@shepherd] ..." does NOT double-prepend.
func TestApplyAutoEnvelope(t *testing.T) {
	cases := []struct {
		name   string
		caller string
		body   string
		want   string
	}{
		{
			name:   "peer worker prepends caller",
			caller: "iogrid-1",
			body:   "hello peer",
			want:   "[@iogrid-1] hello peer",
		},
		{
			name:   "shepherd self-envelope is preserved (no double-prepend)",
			caller: "shepherd",
			body:   "[@shepherd] focus on the task",
			want:   "[@shepherd] focus on the task",
		},
		{
			name:   "shepherd plain body gets prepended (caller defaulted)",
			caller: "shepherd",
			body:   "regression check",
			want:   "[@shepherd] regression check",
		},
		{
			name:   "any body already starting with [@ is untouched",
			caller: "iogrid-1",
			body:   "[@other-peer] forwarded note",
			want:   "[@other-peer] forwarded note",
		},
		{
			name:   "empty caller leaves body untouched (early-boot edge)",
			caller: "",
			body:   "no prefix should be added",
			want:   "no prefix should be added",
		},
		{
			name:   "body that merely contains [@ in the middle still prepends",
			caller: "alice",
			body:   "see [@bob] for context",
			want:   "[@alice] see [@bob] for context",
		},
		{
			name:   "empty body still gets envelope so receiver sees the source",
			caller: "alice",
			body:   "",
			want:   "[@alice] ",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyAutoEnvelope(tc.caller, tc.body)
			if got != tc.want {
				t.Errorf("applyAutoEnvelope(%q, %q) = %q, want %q",
					tc.caller, tc.body, got, tc.want)
			}
		})
	}
}
