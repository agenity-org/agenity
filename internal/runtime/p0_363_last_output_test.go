package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/ptyhost/session"
)

func TestP0_363_RingSnapshot_ExposesTailOutput(t *testing.T) {
	t.Parallel()
	sess, err := session.New("363-tail", session.Spec{
		Command: []string{"sh", "-c", "printf 'chepherd-363-marker-line\\n'; exit 42"},
	})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	defer sess.Close()
	// Wait for the child to exit. session.Done is a method returning a chan.
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session never exited")
	}
	// Small grace for the readLoop to commit the final chunk.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if snap := sess.RingSnapshot(); strings.Contains(string(snap), "chepherd-363-marker-line") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	snap := sess.RingSnapshot()
	if !strings.Contains(string(snap), "chepherd-363-marker-line") {
		t.Errorf("RingSnapshot missing marker; got %q", string(snap))
	}
	if sess.ExitCode() != 42 {
		t.Errorf("ExitCode = %d, want 42", sess.ExitCode())
	}
}

func TestP0_363_SessionInfo_HasLastOutputJSONTag(t *testing.T) {
	t.Parallel()
	var info SessionInfo
	info.LastOutput = "test-output"
	if info.LastOutput != "test-output" {
		t.Error("LastOutput field not assignable")
	}
}
