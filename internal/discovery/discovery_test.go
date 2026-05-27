package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Spin up a fake GitHub API and verify GitHubProvider parses it correctly.
func TestGitHubDiscoverEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghp_test" {
			http.Error(w, "bad token", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"login":      "alice",
				"name":       "Alice",
				"email":      "a@b",
				"avatar_url": "https://x",
			})
		case r.URL.Path == "/user/orgs":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"login": "acmecorp", "avatar_url": "https://acme"},
			})
		case strings.HasPrefix(r.URL.Path, "/user/repos"):
			if r.URL.Query().Get("page") != "1" {
				_ = json.NewEncoder(w).Encode([]map[string]any{})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"name": "infra", "full_name": "acmecorp/infra",
					"default_branch": "main", "private": true,
					"clone_url": "https://github.com/acmecorp/infra.git",
					"updated_at": "2026-01-01T00:00:00Z",
					"owner":      map[string]any{"login": "acmecorp"},
				},
				{
					"name": "dotfiles", "full_name": "alice/dotfiles",
					"default_branch": "main", "private": false,
					"clone_url": "https://github.com/alice/dotfiles.git",
					"updated_at": "2026-01-02T00:00:00Z",
					"owner":      map[string]any{"login": "alice"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := &GitHubProvider{BaseURL: srv.URL, Client: srv.Client()}
	res, err := p.Discover(context.Background(), "ghp_test")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if res.Identity.Login != "alice" {
		t.Fatalf("identity wrong: %+v", res.Identity)
	}
	if len(res.Orgs) != 2 {
		t.Fatalf("expected 2 orgs (alice + acmecorp), got %d", len(res.Orgs))
	}
	// alice's own login comes first by convention
	if res.Orgs[0].Name != "alice" {
		t.Fatalf("first org should be self (alice), got %s", res.Orgs[0].Name)
	}
	gotRepos := 0
	for _, o := range res.Orgs {
		gotRepos += len(o.Repos)
	}
	if gotRepos != 2 {
		t.Fatalf("expected 2 repos total, got %d", gotRepos)
	}
}

// Verify the Service caches results.
func TestServiceCacheHits(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"login": "bob"})
		case "/user/orgs":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "/user/repos":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		}
	}))
	defer srv.Close()

	s := NewService()
	s.TTL = 50 * time.Millisecond
	s.RegisterProvider(&GitHubProvider{BaseURL: srv.URL, Client: srv.Client()})

	_, err := s.Discover(context.Background(), KindGitHub, "tok1", "ghp_")
	if err != nil {
		t.Fatalf("first Discover: %v", err)
	}
	calls1 := calls

	_, _ = s.Discover(context.Background(), KindGitHub, "tok1", "ghp_")
	if calls != calls1 {
		t.Fatalf("second Discover within TTL should hit cache; calls went from %d → %d", calls1, calls)
	}

	time.Sleep(80 * time.Millisecond)
	_, _ = s.Discover(context.Background(), KindGitHub, "tok1", "ghp_")
	if calls == calls1 {
		t.Fatalf("after TTL expiry, Discover should re-fetch; calls stayed at %d", calls)
	}
}

// Manual Refresh bypasses cache.
func TestServiceRefreshBypassesCache(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"login": "carol"})
		case "/user/orgs":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "/user/repos":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		}
	}))
	defer srv.Close()
	s := NewService()
	s.RegisterProvider(&GitHubProvider{BaseURL: srv.URL, Client: srv.Client()})
	_, _ = s.Discover(context.Background(), KindGitHub, "tok", "ghp_")
	c1 := calls
	_, _ = s.Refresh(context.Background(), KindGitHub, "tok", "ghp_")
	if calls <= c1 {
		t.Fatalf("Refresh should re-fetch; calls %d → %d", c1, calls)
	}
}

// Search filters across cached repos.
func TestServiceSearch(t *testing.T) {
	s := NewService()
	// Pre-seed cache directly.
	s.cache[cacheKey(KindGitHub, "tok")] = &cacheEntry{
		result: &DiscoveryResult{
			Provider: KindGitHub,
			Orgs: []Org{
				{Name: "alice", Repos: []Repo{
					{FullName: "alice/dotfiles", UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
					{FullName: "alice/scripts", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
				}},
				{Name: "acme", Repos: []Repo{
					{FullName: "acme/infra", UpdatedAt: time.Date(2025, 12, 30, 0, 0, 0, 0, time.UTC)},
				}},
			},
		},
		expires: time.Now().Add(time.Hour),
	}
	all, err := s.Search(KindGitHub, "tok", "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 repos, got %d", len(all))
	}
	if all[0].FullName != "alice/dotfiles" {
		t.Fatalf("expected newest-first; got %+v", all[0].FullName)
	}
	filtered, _ := s.Search(KindGitHub, "tok", "infra")
	if len(filtered) != 1 || filtered[0].FullName != "acme/infra" {
		t.Fatalf("filter broken: %+v", filtered)
	}
}

// Unknown provider → ErrUnknownProvider.
func TestServiceUnknownProvider(t *testing.T) {
	s := NewService()
	_, err := s.Discover(context.Background(), Kind("nope"), "tok", "secret")
	if err == nil || err.Error() == "" {
		t.Fatalf("expected error for unknown provider, got nil")
	}
}
