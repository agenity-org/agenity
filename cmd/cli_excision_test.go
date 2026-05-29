package cmd

import (
	"strings"
	"testing"
)

// TestCLIExcision_DaemonAndShadowAreUnknown — `chepherd daemon` +
// `chepherd shadow` cobra verbs were retired in v0.9.2 (#208) per
// the architect's single-canonical-entry-point ruling. rootCmd.Find
// returns an error when either verb is invoked. This test pins the
// retirement against accidental re-introduction (a stray init() that
// re-registers daemon/shadow would fail here immediately).
//
// Refs #208.
func TestCLIExcision_DaemonAndShadowAreUnknown(t *testing.T) {
	// rootCmd is package-level mutable state; cobra.Find walks it.
	// Don't t.Parallel — siblings touching rootCmd would race.
	for _, verb := range []string{"daemon", "shadow"} {
		cmd, _, err := rootCmd.Find([]string{verb})
		// cobra.Command.Find behavior: when the verb is unknown,
		// returns (rootCmd, []string{verb}, error) with the error
		// mentioning "unknown command". When the verb maps to a real
		// subcommand, returns (subCmd, ..., nil). We assert the
		// error path.
		if err == nil && cmd != rootCmd {
			t.Errorf("rootCmd.Find(%q) returned a registered subcommand %q; verb should be retired",
				verb, cmd.Use)
			continue
		}
		if err != nil {
			if !strings.Contains(err.Error(), "unknown") &&
				!strings.Contains(err.Error(), verb) {
				t.Errorf("rootCmd.Find(%q) err = %q; want 'unknown' shape", verb, err)
			}
		}
		// When err is nil AND cmd == rootCmd, cobra resolved to root
		// with the verb as an arg — also an acceptable "verb not
		// found" signal because no subcommand matched.
	}
}

// TestCLIExcision_RunIsCanonical — `chepherd run` IS registered (the
// single canonical entry per v0.9.2 architecture). This test is the
// positive-case complement to the daemon/shadow excision test: if a
// future refactor accidentally removes runCmd registration, this
// fails. Together with the excision test, the pair pins the
// single-entry-point model.
//
// Refs #208.
func TestCLIExcision_RunIsCanonical(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd.Find(\"run\") returned err = %v; run is the canonical entry", err)
	}
	if cmd == nil || cmd == rootCmd {
		t.Fatal("rootCmd.Find(\"run\") didn't resolve to a registered subcommand")
	}
	if cmd.Use != "run" {
		t.Errorf("cmd.Use = %q, want %q", cmd.Use, "run")
	}
}
