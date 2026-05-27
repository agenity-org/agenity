package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// GitHubProvider hits api.github.com (or a configurable BaseURL for
// GitHub Enterprise). Token = PAT or fine-grained PAT.
type GitHubProvider struct {
	BaseURL string // default "https://api.github.com"
	Client  *http.Client
}

// NewGitHubProvider builds the default provider.
func NewGitHubProvider() *GitHubProvider {
	return &GitHubProvider{
		BaseURL: "https://api.github.com",
		Client:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *GitHubProvider) Name() Kind { return KindGitHub }

func (p *GitHubProvider) Discover(ctx context.Context, token string) (*DiscoveryResult, error) {
	base := p.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	cli := p.Client
	if cli == nil {
		cli = &http.Client{Timeout: 15 * time.Second}
	}
	// 1. Identity
	var who struct {
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	rateReset, err := ghGetJSON(ctx, cli, base+"/user", token, &who)
	if err != nil {
		return nil, err
	}
	id := Identity{Login: who.Login, DisplayName: who.Name, Email: who.Email, AvatarURL: who.AvatarURL}

	// 2. Orgs (the user's own login is also represented as an Org).
	var orgs []struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	}
	if _, err := ghGetJSON(ctx, cli, base+"/user/orgs?per_page=100", token, &orgs); err != nil {
		return nil, err
	}
	orgsByName := map[string]*Org{who.Login: {Name: who.Login, AvatarURL: who.AvatarURL, Repos: []Repo{}}}
	for _, o := range orgs {
		orgsByName[o.Login] = &Org{Name: o.Login, AvatarURL: o.AvatarURL, Repos: []Repo{}}
	}

	// 3. Repos — paginated.
	page := 1
	for {
		var repos []struct {
			Name           string    `json:"name"`
			FullName       string    `json:"full_name"`
			DefaultBranch  string    `json:"default_branch"`
			Private        bool      `json:"private"`
			CloneURL       string    `json:"clone_url"`
			UpdatedAt      time.Time `json:"updated_at"`
			Owner          struct {
				Login string `json:"login"`
			} `json:"owner"`
		}
		reset, err := ghGetJSON(ctx, cli,
			fmt.Sprintf("%s/user/repos?per_page=100&page=%d&affiliation=owner,collaborator,organization_member", base, page),
			token, &repos)
		if err != nil {
			return nil, err
		}
		if !reset.IsZero() {
			rateReset = reset
		}
		if len(repos) == 0 {
			break
		}
		for _, r := range repos {
			vis := "public"
			if r.Private {
				vis = "private"
			}
			owner := r.Owner.Login
			if _, ok := orgsByName[owner]; !ok {
				orgsByName[owner] = &Org{Name: owner, Repos: []Repo{}}
			}
			orgsByName[owner].Repos = append(orgsByName[owner].Repos, Repo{
				Name: r.Name, FullName: r.FullName,
				DefaultBranch: r.DefaultBranch, Visibility: vis,
				CloneURL: r.CloneURL, UpdatedAt: r.UpdatedAt,
			})
		}
		if len(repos) < 100 {
			break
		}
		page++
	}

	out := &DiscoveryResult{
		Identity:         id,
		FetchedAt:        time.Now().UTC(),
		RateLimitResetAt: rateReset,
	}
	// Deterministic order for tests: user's own login first, then sorted.
	out.Orgs = append(out.Orgs, *orgsByName[who.Login])
	delete(orgsByName, who.Login)
	for _, o := range sortedOrgs(orgsByName) {
		out.Orgs = append(out.Orgs, o)
	}
	return out, nil
}

// ghGetJSON GETs url with bearer token, decodes into out, returns the
// X-RateLimit-Reset value (zero if not rate-limited).
func ghGetJSON(ctx context.Context, cli *http.Client, url, token string, out any) (time.Time, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := cli.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		// Rate-limited.
		if s := resp.Header.Get("X-RateLimit-Reset"); s != "" {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return time.Unix(n, 0), fmt.Errorf("github rate-limited; resets at %s", time.Unix(n, 0))
			}
		}
		return time.Time{}, fmt.Errorf("github %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return time.Time{}, fmt.Errorf("github %d on %s", resp.StatusCode, urlPath(url))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return time.Time{}, err
	}
	return time.Time{}, nil
}

func urlPath(s string) string {
	if u, err := url.Parse(s); err == nil {
		return u.Path
	}
	return s
}
