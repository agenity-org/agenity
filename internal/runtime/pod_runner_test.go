package runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAPIServer struct {
	mu   sync.Mutex
	pods map[string]map[string]any
}

func newFakeAPIServer() *fakeAPIServer {
	return &fakeAPIServer{pods: map[string]map[string]any{}}
}

func (s *fakeAPIServer) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/namespaces/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/"), "/")
		if len(parts) < 2 || parts[1] != "pods" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			meta, _ := body["metadata"].(map[string]any)
			name, _ := meta["name"].(string)
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			s.pods[name] = body
			s.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(body)
		case http.MethodGet:
			if len(parts) < 3 {
				http.Error(w, "list NI", http.StatusNotImplemented)
				return
			}
			s.mu.Lock()
			pod, ok := s.pods[parts[2]]
			s.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			meta, _ := pod["metadata"].(map[string]any)
			if _, ok := meta["creationTimestamp"]; !ok {
				meta["creationTimestamp"] = time.Now().UTC().Format(time.RFC3339)
			}
			_ = json.NewEncoder(w).Encode(pod)
		case http.MethodDelete:
			if len(parts) < 3 {
				http.Error(w, "list-del NI", http.StatusNotImplemented)
				return
			}
			s.mu.Lock()
			_, ok := s.pods[parts[2]]
			delete(s.pods, parts[2])
			s.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method NA", http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func newTestPodRunner(t *testing.T, srv *httptest.Server, namespace, token string) *PodRunner {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &PodRunner{
		token: token, namespace: namespace, caPool: pool, apiHost: srv.URL,
		httpC: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		},
	}
}

func TestPodRunner_SpawnGetStop_Roundtrip(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(newFakeAPIServer().handler(t))
	defer srv.Close()
	r := newTestPodRunner(t, srv, "chepherd", "test-token")
	info, err := r.Spawn(context.Background(), SpawnSpec{
		Name: "agent-1", AgentSlug: "claude-code", Team: "default",
		Role: RoleWorker, Cwd: "/workspace",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if info.Name != "agent-1" {
		t.Errorf("Name = %q", info.Name)
	}
	got, err := r.Get(context.Background(), "agent-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AgentSlug != "claude-code" {
		t.Errorf("AgentSlug = %q", got.AgentSlug)
	}
	if err := r.Stop(context.Background(), "agent-1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := r.Get(context.Background(), "agent-1"); err != ErrSessionNotFound {
		t.Errorf("Get after Stop: %v, want ErrSessionNotFound", err)
	}
}

func TestPodRunner_Stop_IdempotentOn404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(newFakeAPIServer().handler(t))
	defer srv.Close()
	r := newTestPodRunner(t, srv, "chepherd", "tok")
	if err := r.Stop(context.Background(), "nonexistent"); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func TestPodRunner_AuthHeaderCarried(t *testing.T) {
	t.Parallel()
	got := make(chan string, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "x"}})
	}))
	defer srv.Close()
	r := newTestPodRunner(t, srv, "ns", "my-token")
	_, _ = r.Spawn(context.Background(), SpawnSpec{Name: "x", AgentSlug: "y"})
	select {
	case auth := <-got:
		if auth != "Bearer my-token" {
			t.Errorf("Auth = %q", auth)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no request")
	}
}

func TestPodRunner_ScaffoldStubsReturnError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(newFakeAPIServer().handler(t))
	defer srv.Close()
	r := newTestPodRunner(t, srv, "ns", "tok")
	ctx := context.Background()
	if _, err := r.List(ctx); err == nil {
		t.Error("List want err")
	}
	if err := r.Pause(ctx, "x", true); err == nil {
		t.Error("Pause want err")
	}
	if err := r.Restart(ctx, "x"); err == nil {
		t.Error("Restart want err")
	}
	if err := r.Rename(ctx, "x", "y"); err == nil {
		t.Error("Rename want err")
	}
	if _, err := r.AttachIO(ctx, "x"); err == nil {
		t.Error("AttachIO want err")
	}
}
