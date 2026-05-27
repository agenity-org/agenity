package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// GiteaProvider hits a configurable Gitea instance (the embedded one
// for chepherd's built-in repo, or any self-hosted instance).
type GiteaProvider struct {
	BaseURL string // required — Gitea is self-hosted by definition
	Client  *http.Client
}

func NewGiteaProvider(baseURL string) *GiteaProvider {
	return &GiteaProvider{
		BaseURL: baseURL,
		Client:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *GiteaProvider) Name() Kind { return KindGitea }

func (p *GiteaProvider) Discover(ctx context.Context, token string) (*DiscoveryResult, error) {
	if p.BaseURL == "" {
		return nil, fmt.Errorf("gitea: BaseURL required")
	}
	cli := p.Client
	if cli == nil {
		cli = &http.Client{Timeout: 15 * time.Second}
	}
	var who struct {
		Login     string `json:"login"`
		FullName  string `json:"full_name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
		ID        int64  `json:"id"`
	}
	if err := giteaGetJSON(ctx, cli, p.BaseURL+"/api/v1/user", token, &who); err != nil {
		return nil, err
	}
	id := Identity{Login: who.Login, DisplayName: who.FullName, Email: who.Email, AvatarURL: who.AvatarURL}

	// Orgs
	var orgs []struct {
		Username  string `json:"username"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := giteaGetJSON(ctx, cli, p.BaseURL+"/api/v1/orgs", token, &orgs); err != nil {
		return nil, err
	}
	orgsByName := map[string]*Org{who.Login: {Name: who.Login, AvatarURL: who.AvatarURL, Repos: []Repo{}}}
	for _, o := range orgs {
		orgsByName[o.Username] = &Org{Name: o.Username, AvatarURL: o.AvatarURL, Repos: []Repo{}}
	}

	// Repos via search?uid=
	page := 1
	for {
		var resp struct {
			Data []struct {
				Name          string    `json:"name"`
				FullName      string    `json:"full_name"`
				DefaultBranch string    `json:"default_branch"`
				Private       bool      `json:"private"`
				CloneURL      string    `json:"clone_url"`
				UpdatedAt     time.Time `json:"updated_at"`
				Owner         struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"data"`
		}
		url := fmt.Sprintf("%s/api/v1/repos/search?uid=%d&limit=50&page=%d", p.BaseURL, who.ID, page)
		if err := giteaGetJSON(ctx, cli, url, token, &resp); err != nil {
			return nil, err
		}
		if len(resp.Data) == 0 {
			break
		}
		for _, r := range resp.Data {
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
		if len(resp.Data) < 50 {
			break
		}
		page++
	}

	out := &DiscoveryResult{Identity: id, FetchedAt: time.Now().UTC()}
	out.Orgs = append(out.Orgs, *orgsByName[who.Login])
	delete(orgsByName, who.Login)
	for _, o := range sortedOrgs(orgsByName) {
		out.Orgs = append(out.Orgs, o)
	}
	return out, nil
}

func giteaGetJSON(ctx context.Context, cli *http.Client, url, token string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", "token "+token)
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("gitea %d on %s", resp.StatusCode, urlPath(url))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// sortedOrgs returns orgs sorted by name for deterministic JSON output.
// Shared by all provider impls — placed here since gitea is the
// alphabetically-first file in the package after discovery.go.
func sortedOrgs(m map[string]*Org) []Org {
	out := make([]Org, 0, len(m))
	for _, o := range m {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
