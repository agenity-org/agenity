// cmd/runner/test_fakes_r4_test.go — test seams shared across R4
// unit tests (file is _test.go so it's excluded from production
// builds; symbols available to all _test.go files in this package).
//
// Refs #465.
package main

import (
	"sync"

	"github.com/agenity-org/agenity/internal/a2a"
	"github.com/agenity-org/agenity/internal/ptyhost/session"
)

// fakeBroker captures published events. Satisfies
// runtime.BrokerPublisher (which is the unexported brokerPublisher
// interface accepting Publish(taskID, ev)).
type fakeBroker struct {
	mu     sync.Mutex
	events []brokerEntry
}

type brokerEntry struct {
	TaskID string
	Event  a2a.StreamEvent
}

func (b *fakeBroker) Publish(taskID string, ev a2a.StreamEvent) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, brokerEntry{TaskID: taskID, Event: ev})
	return 1
}

func (b *fakeBroker) Events() []brokerEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]brokerEntry, len(b.events))
	copy(out, b.events)
	return out
}

// r4FakeSubscriberSource mimics *session.Session's
// Subscribe/Unsubscribe shape but lets the test drive the
// underlying channel directly.
type r4FakeSubscriberSource struct {
	sub *session.Subscriber
}

func newR4FakeSubscriberSource(bufferDepth int) *r4FakeSubscriberSource {
	return &r4FakeSubscriberSource{
		sub: &session.Subscriber{
			Ch:   make(chan []byte, bufferDepth),
			Done: make(chan struct{}),
		},
	}
}

func (s *r4FakeSubscriberSource) Subscribe(buf int) (*session.Subscriber, []byte, error) {
	return s.sub, nil, nil
}

func (s *r4FakeSubscriberSource) Unsubscribe(_ *session.Subscriber) {}

func (s *r4FakeSubscriberSource) PushChunk(b []byte) { s.sub.Ch <- b }

func (s *r4FakeSubscriberSource) Close() { close(s.sub.Done) }
