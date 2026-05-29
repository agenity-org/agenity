package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// GitLabProvider hits gitlab.com (or self-hosted via BaseURL).
type GitLabProvider struct {
	BaseURL string // default "https://gitlab.com"
	Client  *http.Client
}

func NewGitLabProvider() *GitLabProvider {
	return &GitLabProvider{
		BaseURL: "https://gitlab.com",
		Client:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *GitLabProvider) Name() Kind { return KindGitLab }

func (p *GitLabProvider) Discover(ctx context.Context, token string) (*DiscoveryResult, error) {
	base := p.BaseURL
	if base == "" {
		base = "https://gitlab.com"
	}
	cli := p.Client
	if cli == nil {
		cli = &http.Client{Timeout: 15 * time.Second}
	}

	var who struct {
		Username  string `json:"username"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if _, err := glGetJSON(ctx, cli, base+"/api/v4/user", token, &who); err != nil {
		return nil, err
	}
	id := Identity{Login: who.Username, DisplayName: who.Name, Email: who.Email, AvatarURL: who.AvatarURL}

	// Groups
	var groups []struct {
		Path      string `json:"path"`
		AvatarURL string `json:"avatar_url"`
	}
	if _, err := glGetJSON(ctx, cli, base+"/api/v4/groups?membership=true&per_page=100", token, &groups); err != nil {
		return nil, err
	}
	orgsByName := map[string]*Org{who.Username: {Name: who.Username, AvatarURL: who.AvatarURL, Repos: []Repo{}}}
	for _, g := range groups {
		orgsByName[g.Path] = &Org{Name: g.Path, AvatarURL: g.AvatarURL, Repos: []Repo{}}
	}

	// Projects — paginated.
	page := 1
	var rateReset time.Time
	for {
		var projects []struct {
			Path              string    `json:"path"`
			PathWithNamespace string    `json:"path_with_namespace"`
			DefaultBranch     string    `json:"default_branch"`
			Visibility        string    `json:"visibility"`
			HTTPURLToRepo     string    `json:"http_url_to_repo"`
			LastActivityAt    time.Time `json:"last_activity_at"`
			Namespace         struct {
				Path string `json:"path"`
			} `json:"namespace"`
		}
		reset, err := glGetJSON(ctx, cli,
			fmt.Sprintf("%s/api/v4/projects?membership=true&per_page=100&page=%d", base, page),
			token, &projects)
		if err != nil {
			return nil, err
		}
		if !reset.IsZero() {
			rateReset = reset
		}
		if len(projects) == 0 {
			break
		}
		for _, pr := range projects {
			ns := pr.Namespace.Path
			if _, ok := orgsByName[ns]; !ok {
				orgsByName[ns] = &Org{Name: ns, Repos: []Repo{}}
			}
			orgsByName[ns].Repos = append(orgsByName[ns].Repos, Repo{
				Name: pr.Path, FullName: pr.PathWithNamespace,
				DefaultBranch: pr.DefaultBranch, Visibility: pr.Visibility,
				CloneURL: pr.HTTPURLToRepo, UpdatedAt: pr.LastActivityAt,
			})
		}
		if len(projects) < 100 {
			break
		}
		page++
	}

	out := &DiscoveryResult{Identity: id, FetchedAt: time.Now().UTC(), RateLimitResetAt: rateReset}
	out.Orgs = append(out.Orgs, *orgsByName[who.Username])
	delete(orgsByName, who.Username)
	for _, o := range sortedOrgs(orgsByName) {
		out.Orgs = append(out.Orgs, o)
	}
	return out, nil
}

func glGetJSON(ctx context.Context, cli *http.Client, url, token string, out any) (time.Time, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := cli.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		if s := resp.Header.Get("RateLimit-Reset"); s != "" {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return time.Unix(n, 0), fmt.Errorf("gitlab rate-limited")
			}
		}
		return time.Time{}, fmt.Errorf("gitlab 429")
	}
	if resp.StatusCode >= 400 {
		return time.Time{}, fmt.Errorf("gitlab %d on %s", resp.StatusCode, urlPath(url))
	}
	return time.Time{}, json.NewDecoder(resp.Body).Decode(out)
}
