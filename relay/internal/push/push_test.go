package push

import (
	"context"
	"errors"
	"testing"
)

func TestMultiDispatcher_NoBackends_ErrDisabled(t *testing.T) {
	m := NewMulti()
	err := m.Send(context.Background(), Token{Platform: PlatformiOS, Value: "tok"}, Notification{
		Title: "x", Body: "y",
	})
	if !errors.Is(err, ErrBackendDisabled) {
		t.Fatalf("want ErrBackendDisabled, got %v", err)
	}
}

type stubDispatcher struct {
	kind     string
	sent     int
	lastTok  Token
	lastNote Notification
	err      error
}

func (s *stubDispatcher) Kind() string { return s.kind }
func (s *stubDispatcher) Send(_ context.Context, tok Token, n Notification) error {
	s.sent++
	s.lastTok = tok
	s.lastNote = n
	return s.err
}

func TestMultiDispatcher_RoutesByPlatform(t *testing.T) {
	apns := &stubDispatcher{kind: "apns"}
	fcm := &stubDispatcher{kind: "fcm"}
	m := NewMulti(apns, fcm)

	cases := []struct {
		plat Platform
		want *stubDispatcher
	}{
		{PlatformiOS, apns},
		{PlatformAndroid, fcm},
		{PlatformWebPush, fcm},
	}
	for _, c := range cases {
		err := m.Send(context.Background(), Token{Platform: c.plat, Value: "t"}, Notification{Title: "a", Body: "b"})
		if err != nil {
			t.Fatalf("platform %v: %v", c.plat, err)
		}
		if c.want.sent != 1 {
			t.Errorf("platform %v: want sent=1, got %d", c.plat, c.want.sent)
		}
		c.want.sent = 0
	}
}

func TestMultiDispatcher_PropagatesBackendError(t *testing.T) {
	stubErr := errors.New("backend down")
	apns := &stubDispatcher{kind: "apns", err: stubErr}
	m := NewMulti(apns)
	err := m.Send(context.Background(), Token{Platform: PlatformiOS, Value: "t"}, Notification{Title: "a"})
	if !errors.Is(err, stubErr) {
		t.Fatalf("want stubErr, got %v", err)
	}
}

func TestRedactToken(t *testing.T) {
	if got := redactToken("0123456789abcdef"); got != "0123…cdef" {
		t.Errorf("redactToken: got %q", got)
	}
	if got := redactToken("short"); got != "***" {
		t.Errorf("redactToken short: got %q", got)
	}
}
