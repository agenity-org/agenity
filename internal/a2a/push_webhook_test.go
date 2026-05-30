// internal/a2a/push_webhook_test.go — pins #308 (#225 row A5).
package a2a

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

func hmacSHA256New(key []byte) hash.Hash { return hmac.New(sha256.New, key) }
func hexEncode(b []byte) string         { return hex.EncodeToString(b) }

type fakeConfigStore struct {
	configs []*persistence.PushNotificationConfig
	err     error
}

func (s *fakeConfigStore) List(ctx context.Context, taskID string) ([]*persistence.PushNotificationConfig, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.configs, nil
}

func TestWebhookDispatcher_DispatchTaskUpdate_SignsAndPOSTs(t *testing.T) {
	t.Parallel()
	got := make(chan map[string]string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- map[string]string{
			"sig":  r.Header.Get("X-Chepherd-Signature"),
			"ct":   r.Header.Get("Content-Type"),
			"body": string(body),
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	key := []byte("secret-signing-key")
	d := &WebhookDispatcher{
		Store: &fakeConfigStore{configs: []*persistence.PushNotificationConfig{
			{ID: "cfg-1", TaskID: "task-1", URL: srv.URL, SigningKey: key},
		}},
	}
	d.DispatchTaskUpdate(context.Background(), TaskUpdate{
		TaskID: "task-1", State: "completed", UpdatedAt: time.Now().UTC(),
	})

	select {
	case rec := <-got:
		if rec["ct"] != "application/json" {
			t.Errorf("Content-Type = %q", rec["ct"])
		}
		if err := VerifySignature([]byte(rec["body"]), key, rec["sig"]); err != nil {
			t.Errorf("signature verify: %v", err)
		}
		var payload TaskUpdate
		_ = json.Unmarshal([]byte(rec["body"]), &payload)
		if payload.TaskID != "task-1" || payload.State != "completed" {
			t.Errorf("payload = %+v, want task-1/completed", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook never delivered")
	}
}

func TestWebhookDispatcher_DispatchTaskUpdate_FansOutToAllConfigs(t *testing.T) {
	t.Parallel()
	var (
		mu   sync.Mutex
		hits = 0
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &WebhookDispatcher{
		Store: &fakeConfigStore{configs: []*persistence.PushNotificationConfig{
			{ID: "c1", TaskID: "t", URL: srv.URL, SigningKey: []byte("k1")},
			{ID: "c2", TaskID: "t", URL: srv.URL, SigningKey: []byte("k2")},
			{ID: "c3", TaskID: "t", URL: srv.URL, SigningKey: []byte("k3")},
		}},
	}
	d.DispatchTaskUpdate(context.Background(), TaskUpdate{TaskID: "t", State: "completed"})
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if hits != 3 {
		t.Errorf("hits = %d, want 3", hits)
	}
}

func TestWebhookDispatcher_BestEffortOnNon2xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	var (
		mu       sync.Mutex
		reported error
	)
	d := &WebhookDispatcher{
		Store: &fakeConfigStore{configs: []*persistence.PushNotificationConfig{
			{ID: "c", TaskID: "t", URL: srv.URL, SigningKey: []byte("k")},
		}},
		ErrorSink: func(taskID, url string, err error) {
			mu.Lock()
			reported = err
			mu.Unlock()
		},
	}
	d.DispatchTaskUpdate(context.Background(), TaskUpdate{TaskID: "t", State: "failed"})
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if reported == nil {
		t.Error("ErrorSink not called on 500")
	}
}

func TestWebhookDispatcher_NoConfigsNoop(t *testing.T) {
	t.Parallel()
	d := &WebhookDispatcher{Store: &fakeConfigStore{}}
	d.DispatchTaskUpdate(context.Background(), TaskUpdate{TaskID: "t", State: "completed"})
}

func TestWebhookDispatcher_EmptyTaskIDNoop(t *testing.T) {
	t.Parallel()
	d := &WebhookDispatcher{Store: &fakeConfigStore{configs: []*persistence.PushNotificationConfig{
		{ID: "c", URL: "http://example", SigningKey: []byte("k")},
	}}}
	// Should NOT load configs (empty TaskID short-circuits).
	d.DispatchTaskUpdate(context.Background(), TaskUpdate{State: "completed"})
}

func TestVerifySignature_ValidAndInvalid(t *testing.T) {
	t.Parallel()
	body := []byte(`{"taskId":"t","state":"completed"}`)
	key := []byte("the-key")
	// Compute expected signature
	d := &WebhookDispatcher{}
	// reuse dispatcher's marshal logic via direct hex
	want := computeExpectedSig(key, body)
	if err := VerifySignature(body, key, want); err != nil {
		t.Errorf("valid sig rejected: %v", err)
	}
	if err := VerifySignature(body, key, "sha256=deadbeef"); err == nil {
		t.Error("invalid sig accepted")
	}
	if err := VerifySignature(body, key, "md5=abcd"); err == nil {
		t.Error("non-sha256 prefix accepted")
	}
	_ = d
}

func computeExpectedSig(key, body []byte) string {
	mac := hmacSHA256New(key)
	mac.Write(body)
	return "sha256=" + hexEncode(mac.Sum(nil))
}

