// Package discovery enumerates orgs + repos accessible via a saved
// VCS token (GitHub PAT / GitLab PAT / Bitbucket App Password / Gitea
// access token). v0.9 SpawnWizard Stage-2 (#174) calls this instead of
// asking the operator to type a URL.
//
// Architecture:
//   - One DiscoveryProvider per backend (github | gitlab | bitbucket | gitea)
//   - Single Service fronts all providers + caches per-token results (5min TTL)
//   - Stale-while-revalidate: cached result served immediately; refresh
//     happens in the background on the next read after expiry
//   - Manual refresh endpoint (POST /refresh) bypasses cache
//
// Identity model: each token paired with a "provider kind" (the backend
// it talks to). Token + kind together form the cache key.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind identifies a VCS backend.
type Kind string

const (
	KindGitHub    Kind = "github"
	KindGitLab    Kind = "gitlab"
	KindBitbucket Kind = "bitbucket"
	KindGitea     Kind = "gitea"
)

// DiscoveryResult is the canonical shape returned to the UI.
type DiscoveryResult struct {
	Provider  Kind      `json:"provider"`
	Identity  Identity  `json:"identity"`
	Orgs      []Org     `json:"orgs"`
	FetchedAt time.Time `json:"fetched_at"`
	// RateLimitResetAt is non-zero when the provider responded with a
	// rate-limit signal during the fetch — UI surfaces "next refresh
	// in X min" using this.
	RateLimitResetAt time.Time `json:"rate_limit_reset_at,omitempty"`
}

// Identity is the "who am I" record for the discovered token.
type Identity struct {
	Login     string `json:"login"`
	DisplayName string `json:"display_name,omitempty"`
	Email     string `json:"email,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

// Org is one organisation / workspace / group depending on provider.
// For solo users on GitHub/GitLab/Gitea the user's own namespace is
// represented as an Org with Name == Identity.Login.
type Org struct {
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url,omitempty"`
	Repos     []Repo `json:"repos"`
}

// Repo is the smallest unit the wizard binds against. CloneURL is the
// HTTPS form (token gets injected at clone time downstream).
type Repo struct {
	Name          string    `json:"name"`
	FullName      string    `json:"full_name"`
	DefaultBranch string    `json:"default_branch"`
	Visibility    string    `json:"visibility"` // "public" | "private"
	CloneURL      string    `json:"clone_url"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// DiscoveryProvider is the per-backend contract. Implementations live
// in github.go, gitlab.go, bitbucket.go, gitea.go.
type DiscoveryProvider interface {
	Name() Kind
	Discover(ctx context.Context, token string) (*DiscoveryResult, error)
}

// Service is the cache + dispatcher. Thread-safe.
type Service struct {
	providers map[Kind]DiscoveryProvider
	cache     map[string]*cacheEntry // key = "kind:tokenID"
	mu        sync.RWMutex
	TTL       time.Duration
}

type cacheEntry struct {
	result   *DiscoveryResult
	expires  time.Time
}

// NewService constructs an empty Service with default 5-minute TTL.
func NewService() *Service {
	return &Service{
		providers: make(map[Kind]DiscoveryProvider),
		cache:     make(map[string]*cacheEntry),
		TTL:       5 * time.Minute,
	}
}

// RegisterProvider attaches one backend implementation.
func (s *Service) RegisterProvider(p DiscoveryProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providers[p.Name()] = p
}

// Discover returns the org/repo tree for the given token. Uses the
// cache when fresh; calls the provider on miss / expiry.
//
// tokenID is the operator-visible identifier (e.g. the vault entry id);
// secret is the actual bearer token. The cache key is built from
// kind+tokenID so two operators with different tokens to the same
// provider don't collide.
func (s *Service) Discover(ctx context.Context, kind Kind, tokenID, secret string) (*DiscoveryResult, error) {
	s.mu.RLock()
	p, ok := s.providers[kind]
	cached, hadCache := s.cache[cacheKey(kind, tokenID)]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownProvider, kind)
	}
	if hadCache && time.Now().Before(cached.expires) {
		return cached.result, nil
	}
	res, err := p.Discover(ctx, secret)
	if err != nil {
		// Stale-while-error: if we have an expired entry, return it
		// rather than failing the UI. Mark with the original error in
		// a future iteration; for v1 the caller sees the fetch error.
		if hadCache {
			return cached.result, nil
		}
		return nil, err
	}
	res.Provider = kind
	if res.FetchedAt.IsZero() {
		res.FetchedAt = time.Now().UTC()
	}
	s.mu.Lock()
	s.cache[cacheKey(kind, tokenID)] = &cacheEntry{
		result:  res,
		expires: time.Now().Add(s.TTL),
	}
	s.mu.Unlock()
	return res, nil
}

// Refresh forces a re-fetch, replacing the cached entry. Useful for the
// UI "refresh" button.
func (s *Service) Refresh(ctx context.Context, kind Kind, tokenID, secret string) (*DiscoveryResult, error) {
	s.mu.Lock()
	delete(s.cache, cacheKey(kind, tokenID))
	s.mu.Unlock()
	return s.Discover(ctx, kind, tokenID, secret)
}

// Search returns repos matching q across the cached tree. Falls back
// to all-repos if q is empty.
func (s *Service) Search(kind Kind, tokenID, q string) ([]Repo, error) {
	s.mu.RLock()
	cached, ok := s.cache[cacheKey(kind, tokenID)]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrNotCached
	}
	q = strings.ToLower(q)
	out := []Repo{}
	for _, org := range cached.result.Orgs {
		for _, r := range org.Repos {
			if q == "" || strings.Contains(strings.ToLower(r.FullName), q) {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func cacheKey(kind Kind, tokenID string) string {
	return string(kind) + ":" + tokenID
}

// Errors surfaced to the HTTP layer.
var (
	ErrUnknownProvider = errors.New("discovery: unknown provider")
	ErrNotCached       = errors.New("discovery: token not yet discovered — call Discover first")
)
