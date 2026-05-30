// internal/runtime/d1_subtasks_test.go — pins #349 D1.2-D1.7 batch.
// Each subtask gets ≥1 case verifying real behaviour against an
// httptest fake apiserver.
//
// Refs #349 D1.2 D1.3 D1.4 D1.5 D1.6 D1.7.
package runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAPIServerD1 is a richer kube-apiserver fake for D1.2-D1.7:
// supports POST/GET/DELETE/PATCH + listPods (label selector).
type fakeAPIServerD1 struct {
	pods    map[string]map[string]any
	patches []map[string]any
}

func newFakeAPIServerD1() *fakeAPIServerD1 {
	return &fakeAPIServerD1{pods: map[string]map[string]any{}}
}

func (s *fakeAPIServerD1) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/namespaces/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/")
		parts := strings.Split(path, "/")
		if len(parts) < 2 || parts[1] != "pods" {
			http.NotFound(w, r)
			return
		}
		// /pods or /pods/<name>
		switch {
		case len(parts) == 2 && r.Method == http.MethodGet:
			// List with label selector
			selector := r.URL.Query().Get("labelSelector")
			items := []map[string]any{}
			for _, pod := range s.pods {
				if selector == "" {
					items = append(items, pod)
					continue
				}
				meta, _ := pod["metadata"].(map[string]any)
				labels := stringMap(meta["labels"])
				if matchesSelector(labels, selector) {
					items = append(items, pod)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
			return
		case len(parts) == 2 && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			meta, _ := body["metadata"].(map[string]any)
			name, _ := meta["name"].(string)
			if meta["creationTimestamp"] == nil {
				meta["creationTimestamp"] = time.Now().UTC().Format(time.RFC3339)
			}
			s.pods[name] = body
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(body)
			return
		case len(parts) >= 3 && parts[2] != "":
			name := parts[2]
			pod, ok := s.pods[name]
			if !ok && r.Method != http.MethodDelete {
				http.NotFound(w, r)
				return
			}
			switch r.Method {
			case http.MethodGet:
				_ = json.NewEncoder(w).Encode(pod)
			case http.MethodDelete:
				delete(s.pods, name)
				if !ok {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(http.StatusOK)
			case http.MethodPatch:
				var patch map[string]any
				_ = json.NewDecoder(r.Body).Decode(&patch)
				s.patches = append(s.patches, patch)
				// Apply annotation patch
				if pmeta, ok := patch["metadata"].(map[string]any); ok {
					if anns, ok := pmeta["annotations"].(map[string]any); ok {
						meta, _ := pod["metadata"].(map[string]any)
						existing, _ := meta["annotations"].(map[string]string)
						if existing == nil {
							existing = map[string]string{}
						}
						for k, v := range anns {
							if str, ok := v.(string); ok {
								existing[k] = str
							}
						}
						meta["annotations"] = existing
					}
				}
				_ = json.NewEncoder(w).Encode(pod)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		}
	})
	return mux
}

func matchesSelector(labels map[string]string, selector string) bool {
	// Simple equality matcher for "k=v" form (the only one PodRunner.List uses).
	parts := strings.SplitN(selector, "=", 2)
	if len(parts) != 2 {
		return false
	}
	return labels[parts[0]] == parts[1]
}

func newD1TestRunner(t *testing.T, srv *httptest.Server) *PodRunner {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &PodRunner{
		token: "tok", namespace: "chepherd", caPool: pool, apiHost: srv.URL,
		httpC: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		},
	}
}

func TestPodRunner_List_D1_3_FiltersChepherdLabels(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(newFakeAPIServerD1().handler(t))
	defer srv.Close()
	r := newD1TestRunner(t, srv)
	for _, name := range []string{"agent-a", "agent-b"} {
		_, err := r.Spawn(context.Background(), SpawnSpec{
			Name: name, AgentSlug: "claude-code", Team: "default", Role: RoleWorker,
		})
		if err != nil {
			t.Fatalf("Spawn %q: %v", name, err)
		}
	}
	list, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("List len = %d, want 2", len(list))
	}
}

func TestPodRunner_Pause_D1_4_SetsAnnotation(t *testing.T) {
	t.Parallel()
	fake := newFakeAPIServerD1()
	srv := httptest.NewTLSServer(fake.handler(t))
	defer srv.Close()
	r := newD1TestRunner(t, srv)
	if _, err := r.Spawn(context.Background(), SpawnSpec{
		Name: "agent-p", AgentSlug: "claude-code", Role: RoleWorker,
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := r.Pause(context.Background(), "agent-p", true); err != nil {
		t.Fatalf("Pause(true): %v", err)
	}
	if len(fake.patches) != 1 {
		t.Fatalf("patches = %d, want 1", len(fake.patches))
	}
	patch := fake.patches[0]
	pmeta, _ := patch["metadata"].(map[string]any)
	anns, _ := pmeta["annotations"].(map[string]any)
	if got := anns["chepherd.io/paused"]; got != "true" {
		t.Errorf("annotation = %v, want 'true'", got)
	}
	// Pause(false) clears
	if err := r.Pause(context.Background(), "agent-p", false); err != nil {
		t.Fatalf("Pause(false): %v", err)
	}
	if len(fake.patches) != 2 {
		t.Fatalf("patches after 2nd Pause = %d, want 2", len(fake.patches))
	}
	patch2 := fake.patches[1]
	pmeta2, _ := patch2["metadata"].(map[string]any)
	anns2, _ := pmeta2["annotations"].(map[string]any)
	if got := anns2["chepherd.io/paused"]; got != "false" {
		t.Errorf("annotation 2nd = %v, want 'false'", got)
	}
}

func TestPodRunner_Restart_D1_5_DeleteAndRespawn(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(newFakeAPIServerD1().handler(t))
	defer srv.Close()
	r := newD1TestRunner(t, srv)
	if _, err := r.Spawn(context.Background(), SpawnSpec{
		Name: "agent-r", AgentSlug: "claude-code", Team: "t", Role: RoleWorker, Cwd: "/w",
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := r.Restart(context.Background(), "agent-r"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	got, err := r.Get(context.Background(), "agent-r")
	if err != nil {
		t.Fatalf("Get after Restart: %v", err)
	}
	if got.AgentSlug != "claude-code" || got.Team != "t" {
		t.Errorf("post-restart identity lost: %+v", got)
	}
}

func TestPodRunner_Rename_D1_6_StopAndSpawnUnderNewName(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(newFakeAPIServerD1().handler(t))
	defer srv.Close()
	r := newD1TestRunner(t, srv)
	if _, err := r.Spawn(context.Background(), SpawnSpec{
		Name: "old-name", AgentSlug: "claude-code", Team: "t", Role: RoleWorker,
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := r.Rename(context.Background(), "old-name", "new-name"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := r.Get(context.Background(), "old-name"); err != ErrSessionNotFound {
		t.Errorf("old-name still exists: err = %v", err)
	}
	got, err := r.Get(context.Background(), "new-name")
	if err != nil {
		t.Fatalf("Get new-name: %v", err)
	}
	if got.AgentSlug != "claude-code" {
		t.Errorf("identity lost: %+v", got)
	}
}

func TestPodRunner_Rename_D1_6_RejectsEmptyNewName(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(newFakeAPIServerD1().handler(t))
	defer srv.Close()
	r := newD1TestRunner(t, srv)
	if err := r.Rename(context.Background(), "x", ""); err == nil {
		t.Error("empty newName accepted")
	}
}

func TestPodRunner_Kubeconfig_D1_7_Parses(t *testing.T) {
	t.Parallel()
	yaml := `current-context: test-ctx
contexts:
  - name: test-ctx
    context:
      cluster: test-cluster
      user: test-user
      namespace: testns
clusters:
  - name: test-cluster
    cluster:
      server: https://api.example.com:6443
      insecure-skip-tls-verify: true
users:
  - name: test-user
    user:
      token: my-token-abc
`
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, err := newPodRunner(RunnerConfig{
		Kind: RunnerKindPod, Store: nil, StateDir: dir,
		KubeconfigPath: path,
	})
	if err != nil {
		t.Fatalf("newPodRunner: %v", err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.token != "my-token-abc" {
		t.Errorf("token = %q, want my-token-abc", r.token)
	}
	if r.namespace != "testns" {
		t.Errorf("namespace = %q, want testns", r.namespace)
	}
	if r.apiHost != "https://api.example.com:6443" {
		t.Errorf("apiHost = %q", r.apiHost)
	}
}

func TestPodRunner_Kubeconfig_D1_7_TokenFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("token-from-file\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	yaml := `current-context: c
contexts:
  - name: c
    context: {cluster: cl, user: u, namespace: ns}
clusters:
  - name: cl
    cluster: {server: https://api.x, insecure-skip-tls-verify: true}
users:
  - name: u
    user:
      tokenFile: ` + tokenPath + `
`
	kpath := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kpath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	r, err := newPodRunner(RunnerConfig{
		Kind: RunnerKindPod, StateDir: dir, KubeconfigPath: kpath,
	})
	if err != nil {
		t.Fatalf("newPodRunner: %v", err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.token != "token-from-file" {
		t.Errorf("token = %q, want token-from-file", r.token)
	}
}

// AttachIO (D1.2) is exercised by a smoke test that confirms the
// dial attempt happens against the right URL + the request carries
// the Bearer token. Full bi-directional WS multiplexing is out of
// scope for unit tests (needs a real exec backend).
func TestPodRunner_AttachIO_D1_2_DialsExecWithBearer(t *testing.T) {
	t.Parallel()
	got := make(chan string, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/exec") {
			got <- r.Header.Get("Authorization")
		}
		// Pre-exec, the Get is called — return a minimal pod.
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pods/agent-1") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"metadata": map[string]any{
					"name": "agent-1",
					"labels": map[string]string{
						"chepherd.io/agent-slug": "claude-code",
					},
				},
			})
			return
		}
		http.Error(w, "not implemented", http.StatusNotImplemented)
	}))
	defer srv.Close()
	r := newD1TestRunner(t, srv)
	// AttachIO will fail at the WS upgrade, but we want to assert the
	// dial reached the exec subresource with the Bearer header.
	_, _ = r.AttachIO(context.Background(), "agent-1")
	select {
	case auth := <-got:
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("Auth = %q, want Bearer prefix", auth)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("apiserver never received /exec request")
	}
}


// stringMap normalizes a map value that may be either map[string]string
// (pre-roundtrip) or map[string]any (post-JSON-roundtrip) into a flat
// map[string]string for selector matching.
func stringMap(v any) map[string]string {
	out := map[string]string{}
	switch m := v.(type) {
	case map[string]string:
		for k, val := range m {
			out[k] = val
		}
	case map[string]any:
		for k, val := range m {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}
