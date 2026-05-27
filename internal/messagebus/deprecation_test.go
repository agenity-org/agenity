package messagebus

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/chepherd/chepherd/internal/ptyhost/session"
)

// stubRegistry is the minimal SessionRegistry stub for testing the
// deprecation-log behaviour without spinning up real ptyhost sessions.
// The relay calls SessionByName to resolve the target; we return
// (nil, "", false) so the dispatch path returns early after the
// deprecation log fires.
type stubRegistry struct{}

func (stubRegistry) SessionByName(string) (*session.Session, string, bool) {
	return nil, "", false
}
func (stubRegistry) SessionsByTribe(string) []*session.Session       { return nil }
func (stubRegistry) HumanInbox(string, string)                       {}
func (stubRegistry) IsCrossTribeGranted(string, string) bool         { return false }
func (stubRegistry) IsSessionPaused(s *session.Session) bool         { return false }

// TestDeprecationLogFiresOnEveryMatch verifies #203 acceptance criterion:
// "Regex relay logs deprecation warning on every match". The body of the
// deprecation log mentions both the sender and the target so operators
// reading the log can identify which agent + which target to update.
func TestDeprecationLogFiresOnEveryMatch(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	r := New(stubRegistry{})
	r.processLine("worker-a", "@worker-b: hello peer")

	out := buf.String()
	if !strings.Contains(out, "deprecated:") {
		t.Errorf("expected 'deprecated:' in log, got %q", out)
	}
	if !strings.Contains(out, "worker-a") {
		t.Errorf("expected sender 'worker-a' in log, got %q", out)
	}
	if !strings.Contains(out, "worker-b") {
		t.Errorf("expected target 'worker-b' in log, got %q", out)
	}
	if !strings.Contains(out, "send_to_session") {
		t.Errorf("expected guidance pointer 'send_to_session' in log, got %q", out)
	}
	if !strings.Contains(out, "#203") {
		t.Errorf("expected '#203' issue ref in log, got %q", out)
	}
}

// TestNoLogOnNonMatch confirms the deprecation log is not noisy — it
// only fires when the line actually parsed as an @target message.
func TestNoLogOnNonMatch(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	r := New(stubRegistry{})
	r.processLine("worker-a", "just a regular line of output")
	r.processLine("worker-a", "user@host:~/repo$ git status") // shell prompt false-positive territory

	if strings.Contains(buf.String(), "deprecated:") {
		t.Errorf("deprecation log fired on non-@-line: %q", buf.String())
	}
}
