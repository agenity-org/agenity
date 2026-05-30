// internal/federation/b2b_deliverer_test.go — pins #320 (#225 row E4).
package federation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/chepherd/chepherd/internal/a2a"
)

type fakeInbox struct {
	mu     sync.Mutex
	events []inboxEvent
}
type inboxEvent struct{ from, body string }

func (f *fakeInbox) RecordEvent(from, body string) {
	f.mu.Lock()
	f.events = append(f.events, inboxEvent{from, body})
	f.mu.Unlock()
}
func (f *fakeInbox) Events() []inboxEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]inboxEvent, len(f.events))
	copy(out, f.events)
	return out
}

type fakePeer struct {
	mu       sync.Mutex
	requests []map[string]any
}

func (p *fakePeer) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jsonrpc" {
			t.Errorf("peer received non-/jsonrpc path: %q", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.requests = append(p.requests, body)
		p.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": body["id"],
			"result": map[string]any{
				"task": &a2a.Task{
					ID: "peer-task-1", ContextID: "peer-session", Kind: "task",
					Status: a2a.TaskStatus{State: a2a.TaskStateWorking},
				},
			},
		})
	})
}
func (p *fakePeer) Requests() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]map[string]any, len(p.requests))
	copy(out, p.requests)
	return out
}

func TestInteractiveB2BDeliverer_Deliver_ForwardsAndRecordsInbox(t *testing.T) {
	t.Parallel()
	peer := &fakePeer{}
	srv := httptest.NewServer(peer.handler(t))
	defer srv.Close()
	inbox := &fakeInbox{}
	d := &InteractiveB2BDeliverer{PeerURL: srv.URL, PeerSID: "peer-B", Inbox: inbox}
	task, err := d.Deliver(context.Background(), a2a.Message{
		Role: "user", Kind: "message", ContextID: "ctx-A", MessageID: "msg-1",
		Parts: []a2a.Part{{Kind: "text", Text: "hello peer"}},
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if task == nil || task.ID != "peer-task-1" {
		t.Errorf("Task = %+v, want peer-task-1", task)
	}
	reqs := peer.Requests()
	if len(reqs) != 1 || reqs[0]["method"] != "message/send" {
		t.Errorf("peer reqs = %v, want one message/send", reqs)
	}
	events := inbox.Events()
	if len(events) != 1 || !strings.Contains(events[0].body, "@peer-B") || !strings.Contains(events[0].body, "hello peer") {
		t.Errorf("inbox events = %+v, want '@peer-B' + 'hello peer'", events)
	}
}

func TestInteractiveB2BDeliverer_Deliver_NoInboxNoCrash(t *testing.T) {
	t.Parallel()
	peer := &fakePeer{}
	srv := httptest.NewServer(peer.handler(t))
	defer srv.Close()
	d := &InteractiveB2BDeliverer{PeerURL: srv.URL, PeerSID: "peer-B"}
	if _, err := d.Deliver(context.Background(), a2a.Message{
		Role: "user", Kind: "message", ContextID: "c", MessageID: "m",
		Parts: []a2a.Part{{Kind: "text", Text: "hi"}},
	}); err != nil {
		t.Fatalf("nil-inbox Deliver: %v", err)
	}
}

func TestInteractiveB2BDeliverer_Deliver_Validation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	msg := a2a.Message{Role: "user", Kind: "message", ContextID: "c", MessageID: "m", Parts: []a2a.Part{{Kind: "text", Text: "x"}}}
	if _, err := (&InteractiveB2BDeliverer{PeerSID: "x"}).Deliver(ctx, msg); err == nil {
		t.Error("empty PeerURL accepted")
	}
	if _, err := (&InteractiveB2BDeliverer{PeerURL: "http://x"}).Deliver(ctx, msg); err == nil {
		t.Error("empty PeerSID accepted")
	}
}

func TestInteractiveB2BDeliverer_Deliver_PeerErrorSurfaces(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "peer overloaded", http.StatusInternalServerError)
	}))
	defer srv.Close()
	d := &InteractiveB2BDeliverer{PeerURL: srv.URL, PeerSID: "p"}
	task, err := d.Deliver(context.Background(), a2a.Message{
		Role: "user", Kind: "message", ContextID: "c", MessageID: "m",
		Parts: []a2a.Part{{Kind: "text", Text: "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want HTTP 500 surfaced", err)
	}
	if task == nil || task.Status.State != a2a.TaskStateFailed {
		t.Errorf("Task = %+v, want failed", task)
	}
}

func TestInteractiveB2BDeliverer_Deliver_RejectsNonTextParts(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	d := &InteractiveB2BDeliverer{PeerURL: srv.URL, PeerSID: "p"}
	if _, err := d.Deliver(context.Background(), a2a.Message{
		Role: "user", Kind: "message", ContextID: "c",
		Parts: []a2a.Part{{Kind: "file"}},
	}); err == nil {
		t.Error("non-text Part accepted")
	}
}
