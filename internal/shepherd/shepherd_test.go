package shepherd

import (
	"context"
	"testing"
)

func TestNew_ReturnsShepherd(t *testing.T) {
	t.Parallel()
	s := New(JudgeConfig{})
	if s == nil {
		t.Fatal("New returned nil")
	}
}

func TestShepherdImpl_ObserveNoOps(t *testing.T) {
	t.Parallel()
	s := New(JudgeConfig{})
	// Scaffold Observe drops events on the floor without panicking.
	s.Observe(context.Background(), "any event payload")
	s.Observe(context.Background(), nil)
}

func TestShepherdImpl_JudgeReturnsNilNil(t *testing.T) {
	t.Parallel()
	s := New(JudgeConfig{})
	v, err := s.Judge(context.Background(), "sess-1", []byte("recent output"))
	if err != nil {
		t.Errorf("Judge err = %v, want nil (scaffold)", err)
	}
	if v != nil {
		t.Errorf("Judge verdict = %+v, want nil (scaffold)", v)
	}
}

func TestShepherdImpl_AlertReturnsNil(t *testing.T) {
	t.Parallel()
	s := New(JudgeConfig{})
	if err := s.Alert(context.Background(), &Verdict{}); err != nil {
		t.Errorf("Alert err = %v, want nil (scaffold)", err)
	}
}
