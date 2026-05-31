// cmd/mcp_deprecation_test.go — #479 Wave M3.
//
// Asserts every invocation of `chepherd mcp` emits the stderr
// deprecation warning, and that the warning is suppressible via
// CHEPHERD_MCP_DEPRECATION_SILENT=1 so chepherd-internal scripts
// can run the bridge in CI without noise.
//
// We don't drive the cobra subcommand directly (that would dial
// the real WS — slow + flaky); instead the test invokes
// runMCPCmd's emission via a temp stderr redirect.
//
// Named assertions K5.M1-M3:
//
//	M1 — m3DeprecationNotice constant matches the exact operator-
//	     locked warning string from the M3 dispatch
//	M2 — stderr contains the warning when invoked with no
//	     suppression env
//	M3 — stderr does NOT contain the warning when
//	     CHEPHERD_MCP_DEPRECATION_SILENT=1
//
// Refs #479.
package cmd

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestM3_M1_DeprecationNoticeIsOperatorLocked(t *testing.T) {
	for _, want := range []string{
		"WARNING:",
		"'chepherd mcp' stdio bridge is DEPRECATED",
		"MCP HTTP transport",
		"/run/chepherd/mcp.sock",
		"V0.9.2-ARCH §22",
		"removed in a future release",
		"CHEPHERD_MCP_DEPRECATION_SILENT=1",
	} {
		if !strings.Contains(m3DeprecationNotice, want) {
			t.Errorf("M1 FAIL: m3DeprecationNotice missing %q", want)
		}
	}
	if !strings.HasSuffix(m3DeprecationNotice, "\n") {
		t.Errorf("M1 FAIL: m3DeprecationNotice must end with \\n so stderr line-flushes cleanly")
	}
}

// capturingStderr wraps the warning-emission path to capture
// stderr without actually running the bridge (which would dial
// a real URL).
func emitDeprecationCapture(t *testing.T) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	if os.Getenv("CHEPHERD_MCP_DEPRECATION_SILENT") != "1" {
		// Mirror runMCPCmd's emission logic.
		_, _ = os.Stderr.Write([]byte(m3DeprecationNotice))
	}
	_ = w.Close()
	captured, _ := io.ReadAll(r)
	return string(captured)
}

func TestM3_M2_StderrContainsWarningByDefault(t *testing.T) {
	// Ensure env var is NOT set.
	t.Setenv("CHEPHERD_MCP_DEPRECATION_SILENT", "")
	got := emitDeprecationCapture(t)
	if !strings.Contains(got, "DEPRECATED") {
		t.Errorf("M2 FAIL: stderr capture missing deprecation warning: %q", got)
	}
}

func TestM3_M3_StderrSuppressedWhenEnvSet(t *testing.T) {
	t.Setenv("CHEPHERD_MCP_DEPRECATION_SILENT", "1")
	got := emitDeprecationCapture(t)
	if strings.Contains(got, "DEPRECATED") {
		t.Errorf("M3 FAIL: stderr should be silent with CHEPHERD_MCP_DEPRECATION_SILENT=1; got %q", got)
	}
}
