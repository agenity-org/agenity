// internal/a2a/p0_482_webhook_test.go pins the v0.9.4 §16 + A2A
// v1.0 push-notification webhook delivery (#482 Wave A3). The CRUD
// surface already exists (#225 row A5); A3 turns the persisted
// registrations into actual outbound POSTs whenever Publish fires.
//
// Asserts:
//   - StreamBroker.Publish with PushConfigStore wired fires a POST
//     to each registered config URL.
//   - The POST body is the StreamEvent JSON envelope.
//   - SigningKey on the config produces Authorization: Bearer header.
//   - Empty Filters fire on every event; non-empty Filters scope
//     delivery to matching state values.
//   - 5xx responses trigger retries up to 3 attempts; 4xx does not.
//   - Webhook fan-out runs WITHOUT subscriber registration (push is
//     for clients that can receive but not maintain SSE).
//   - PushConfigStore=nil disables delivery (back-compat).
//
// Refs #482 V0.9.2-ARCHITECTURE.md §16 #225 row A5.
package a2a

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// fakePushStore is an in-memory PushConfigLister for tests.
type fakePushStore struct {
	mu      sync.Mutex
	configs map[string][]*persistence.PushNotificationConfig
}

func (f *fakePushStore) seed(taskID string, cfg *persistence.PushNotificationConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.configs == nil {
		f.configs = map[string][]*persistence.PushNotificationConfig{}
	}
	f.configs[taskID] = append(f.configs[taskID], cfg)
}

func (f *fakePushStore) List(ctx context.Context, taskID string) ([]*persistence.PushNotificationConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*persistence.PushNotificationConfig(nil), f.configs[taskID]...), nil
}

// webhookCapture is a test sink that records every received POST.
type webhookCapture struct {
	server *httptest.Server
	status int32 // atomic
	calls  int32 // atomic
	bodies chan []byte
	auths  chan string
}

func newWebhookCapture(t *testing.T, initialStatus int) *webhookCapture {
	t.Helper()
	c := &webhookCapture{
		bodies: make(chan []byte, 8),
		auths:  make(chan string, 8),
	}
	atomic.StoreInt32(&c.status, int32(initialStatus))
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&c.calls, 1)
		body, _ := io.ReadAll(r.Body)
		select {
		case c.bodies <- body:
		default:
		}
		select {
		case c.auths <- r.Header.Get("Authorization"):
		default:
		}
		w.WriteHeader(int(atomic.LoadInt32(&c.status)))
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *webhookCapture) setStatus(s int) { atomic.StoreInt32(&c.status, int32(s)) }
func (c *webhookCapture) callCount() int  { return int(atomic.LoadInt32(&c.calls)) }

func TestWaveA3_Publish_FiresWebhookWithEventBody(t *testing.T) {
	t.Parallel()
	cap := newWebhookCapture(t, http.StatusOK)
	store := &fakePushStore{}
	store.seed("task-A", &persistence.PushNotificationConfig{
		ID: "cfg-A", TaskID: "task-A", URL: cap.server.URL,
	})
	b := NewStreamBroker()
	b.PushConfigStore = store

	b.Publish("task-A", StreamEvent{
		Type: "status",
		Task: &Task{ID: "task-A", Status: TaskStatus{State: TaskStateWorking}},
	})

	select {
	case body := <-cap.bodies:
		var ev StreamEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if ev.Task == nil || ev.Task.ID != "task-A" || ev.Task.Status.State != TaskStateWorking {
			t.Errorf("event body wrong: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook never received POST")
	}
}

func TestWaveA3_Publish_FiresWithoutSubscribers(t *testing.T) {
	t.Parallel()
	cap := newWebhookCapture(t, http.StatusOK)
	store := &fakePushStore{}
	store.seed("task-no-sub", &persistence.PushNotificationConfig{
		ID: "c", TaskID: "task-no-sub", URL: cap.server.URL,
	})
	b := NewStreamBroker()
	b.PushConfigStore = store

	// No Subscribe call — webhook should still fire.
	b.Publish("task-no-sub", StreamEvent{Type: "done", Task: &Task{ID: "task-no-sub", Status: TaskStatus{State: TaskStateCompleted}}})

	select {
	case <-cap.bodies:
	case <-time.After(2 * time.Second):
		t.Fatal("webhook should fire on no-subscriber task (push is for SSE-less clients)")
	}
}

func TestWaveA3_Publish_BearerAuthFromSigningKey(t *testing.T) {
	t.Parallel()
	cap := newWebhookCapture(t, http.StatusOK)
	store := &fakePushStore{}
	store.seed("task-auth", &persistence.PushNotificationConfig{
		ID: "c", TaskID: "task-auth", URL: cap.server.URL,
		SigningKey: []byte("super-secret-token"),
	})
	b := NewStreamBroker()
	b.PushConfigStore = store

	b.Publish("task-auth", StreamEvent{Type: "status", Task: &Task{ID: "task-auth"}})

	select {
	case auth := <-cap.auths:
		if auth != "Bearer super-secret-token" {
			t.Errorf("Authorization = %q, want Bearer super-secret-token", auth)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook never received POST")
	}
}

func TestWaveA3_Filters_OnlyDeliverMatchingState(t *testing.T) {
	t.Parallel()
	cap := newWebhookCapture(t, http.StatusOK)
	store := &fakePushStore{}
	store.seed("task-filt", &persistence.PushNotificationConfig{
		ID: "c", TaskID: "task-filt", URL: cap.server.URL,
		Filters: []string{"state:completed"},
	})
	b := NewStreamBroker()
	b.PushConfigStore = store

	// WORKING event — must NOT match the COMPLETED-only filter.
	b.Publish("task-filt", StreamEvent{Type: "status", Task: &Task{ID: "task-filt", Status: TaskStatus{State: TaskStateWorking}}})
	time.Sleep(100 * time.Millisecond)
	if cap.callCount() != 0 {
		t.Errorf("filtered event still delivered: calls=%d", cap.callCount())
	}
	// COMPLETED event — must match.
	b.Publish("task-filt", StreamEvent{Type: "done", Task: &Task{ID: "task-filt", Status: TaskStatus{State: TaskStateCompleted}}})
	select {
	case <-cap.bodies:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("filter-matching event was not delivered")
	}
}

func TestWaveA3_Retry_OnTransient5xx(t *testing.T) {
	t.Parallel()
	cap := newWebhookCapture(t, http.StatusInternalServerError)
	store := &fakePushStore{}
	store.seed("task-retry", &persistence.PushNotificationConfig{
		ID: "c", TaskID: "task-retry", URL: cap.server.URL,
	})
	b := NewStreamBroker()
	b.PushConfigStore = store
	// Shrink the backoff schedule so the test stays fast.
	b.HTTPClient = &http.Client{Timeout: 500 * time.Millisecond}

	b.Publish("task-retry", StreamEvent{Type: "status", Task: &Task{ID: "task-retry"}})

	// Wait long enough for 3 attempts with the production backoff (~750ms).
	time.Sleep(3 * time.Second)
	if got := cap.callCount(); got != 3 {
		t.Errorf("retry attempts = %d, want 3 on 5xx", got)
	}
}

func TestWaveA3_NoRetry_On4xx(t *testing.T) {
	t.Parallel()
	cap := newWebhookCapture(t, http.StatusBadRequest)
	store := &fakePushStore{}
	store.seed("task-4xx", &persistence.PushNotificationConfig{
		ID: "c", TaskID: "task-4xx", URL: cap.server.URL,
	})
	b := NewStreamBroker()
	b.PushConfigStore = store

	b.Publish("task-4xx", StreamEvent{Type: "status", Task: &Task{ID: "task-4xx"}})
	time.Sleep(1 * time.Second)
	if got := cap.callCount(); got != 1 {
		t.Errorf("4xx triggered %d retries, want 1 (no retry)", got)
	}
}

func TestWaveA3_NilStore_DisablesDelivery(t *testing.T) {
	t.Parallel()
	b := NewStreamBroker()
	// PushConfigStore deliberately nil.
	// Subscribe so the publish path actually has somewhere to land.
	streamID, _ := b.Subscribe("task-nil")
	if streamID == "" {
		t.Fatal("subscribe failed")
	}
	// Just calling Publish with nil store must not panic + must
	// not block.
	done := make(chan struct{})
	go func() {
		b.Publish("task-nil", StreamEvent{Type: "status", Task: &Task{ID: "task-nil"}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Publish blocked with nil PushConfigStore")
	}
}
