// internal/keychain/openbao_backend_test.go — pins #322 (H6).
package keychain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeBao is an httptest substrate matching the minimal KV-v2 surface
// OpenBaoBackend uses.
type fakeBao struct {
	data map[string]string
}

func newFakeBao() *fakeBao { return &fakeBao{data: map[string]string{}} }

func (f *fakeBao) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sys/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/secret/data/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") == "" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/v1/secret/data/")
		switch r.Method {
		case http.MethodPost:
			var body struct {
				Data struct {
					Value string `json:"value"`
				} `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.data[key] = body.Data.Value
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			val, ok := f.data[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"data": map[string]string{"value": val},
				},
			})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/secret/metadata/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") == "" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/v1/secret/metadata/")
		if r.Method == http.MethodDelete {
			_, ok := f.data[key]
			delete(f.data, key)
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	return mux
}

func TestOpenBao_SetGetDelete_Roundtrip(t *testing.T) {
	t.Parallel()
	bao := newFakeBao()
	srv := httptest.NewServer(bao.handler(t))
	defer srv.Close()
	b, err := NewOpenBaoBackend(srv.URL, "test-token", "", "secret")
	if err != nil {
		t.Fatalf("NewOpenBaoBackend: %v", err)
	}
	if err := b.Set("api-key", "shhh-it-is-secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := b.Get("api-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "shhh-it-is-secret" {
		t.Errorf("Get = %q, want shhh-it-is-secret", got)
	}
	if err := b.Delete("api-key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := b.Get("api-key"); err != ErrNotFound {
		t.Errorf("Get after Delete: err = %v, want ErrNotFound", err)
	}
}

func TestOpenBao_Available_AcceptsHealthEndpoint(t *testing.T) {
	t.Parallel()
	bao := newFakeBao()
	srv := httptest.NewServer(bao.handler(t))
	defer srv.Close()
	b, _ := NewOpenBaoBackend(srv.URL, "tok", "", "secret")
	if !b.Available() {
		t.Error("Available() = false, want true (fake reports healthy)")
	}
}

func TestOpenBao_Available_FalseOnUnreachable(t *testing.T) {
	t.Parallel()
	b, _ := NewOpenBaoBackend("http://127.0.0.1:1", "tok", "", "secret")
	if b.Available() {
		t.Error("Available() = true on unreachable host, want false")
	}
}

func TestOpenBao_Delete_IdempotentOn404(t *testing.T) {
	t.Parallel()
	bao := newFakeBao()
	srv := httptest.NewServer(bao.handler(t))
	defer srv.Close()
	b, _ := NewOpenBaoBackend(srv.URL, "tok", "", "secret")
	if err := b.Delete("nonexistent"); err != nil {
		t.Errorf("Delete: %v (want nil idempotent)", err)
	}
}

func TestOpenBao_NewBackend_Validation(t *testing.T) {
	t.Parallel()
	if _, err := NewOpenBaoBackend("", "x", "", ""); err == nil {
		t.Error("empty addr accepted")
	}
	if _, err := NewOpenBaoBackend("http://x", "", "", ""); err == nil {
		t.Error("empty token + empty tokenpath accepted")
	}
}

func TestOpenBao_Backend_Interface(t *testing.T) {
	t.Parallel()
	var _ Backend = &OpenBaoBackend{}
}
