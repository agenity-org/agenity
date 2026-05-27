package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// BitbucketProvider hits api.bitbucket.org. Token = App Password
// (basic-auth username:password OR Bearer token via OAuth — we use
// the App Password "username:apppassword" form which the operator
// pastes as one string).
type BitbucketProvider struct {
	BaseURL string // default "https://api.bitbucket.org/2.0"
	Client  *http.Client
}

func NewBitbucketProvider() *BitbucketProvider {
	return &BitbucketProvider{
		BaseURL: "https://api.bitbucket.org/2.0",
		Client:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *BitbucketProvider) Name() Kind { return KindBitbucket }

func (p *BitbucketProvider) Discover(ctx context.Context, token string) (*DiscoveryResult, error) {
	base := p.BaseURL
	if base == "" {
		base = "https://api.bitbucket.org/2.0"
	}
	cli := p.Client
	if cli == nil {
		cli = &http.Client{Timeout: 15 * time.Second}
	}
	// Identity
	var who struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Links       struct {
			Avatar struct {
				Href string `json:"href"`
			} `json:"avatar"`
		} `json:"links"`
	}
	if err := bbGetJSON(ctx, cli, base+"/user", token, &who); err != nil {
		return nil, err
	}
	id := Identity{Login: who.Username, DisplayName: who.DisplayName, AvatarURL: who.Links.Avatar.Href}

	// Workspaces
	var wsResp struct {
		Values []struct {
			Slug  string `json:"slug"`
			Name  string `json:"name"`
			Links struct {
				Avatar struct {
					Href string `json:"href"`
				} `json:"avatar"`
			} `json:"links"`
		} `json:"values"`
	}
	if err := bbGetJSON(ctx, cli, base+"/workspaces?role=member&pagelen=100", token, &wsResp); err != nil {
		return nil, err
	}
	orgsByName := map[string]*Org{}
	for _, ws := range wsResp.Values {
		orgsByName[ws.Slug] = &Org{Name: ws.Slug, AvatarURL: ws.Links.Avatar.Href, Repos: []Repo{}}
	}

	// Repos per workspace.
	for _, ws := range wsResp.Values {
		url := fmt.Sprintf("%s/repositories/%s?pagelen=100", base, ws.Slug)
		for url != "" {
			var page struct {
				Values []struct {
					Name      string    `json:"name"`
					Slug      string    `json:"slug"`
					FullName  string    `json:"full_name"`
					Mainbranch struct {
						Name string `json:"name"`
					} `json:"mainbranch"`
					IsPrivate bool      `json:"is_private"`
					UpdatedOn time.Time `json:"updated_on"`
					Links     struct {
						Clone []struct {
							Name string `json:"name"`
							Href string `json:"href"`
						} `json:"clone"`
					} `json:"links"`
				} `json:"values"`
				Next string `json:"next"`
			}
			if err := bbGetJSON(ctx, cli, url, token, &page); err != nil {
				return nil, err
			}
			for _, r := range page.Values {
				vis := "public"
				if r.IsPrivate {
					vis = "private"
				}
				cloneURL := ""
				for _, c := range r.Links.Clone {
					if c.Name == "https" {
						cloneURL = c.Href
						break
					}
				}
				orgsByName[ws.Slug].Repos = append(orgsByName[ws.Slug].Repos, Repo{
					Name: r.Slug, FullName: r.FullName,
					DefaultBranch: r.Mainbranch.Name, Visibility: vis,
					CloneURL: cloneURL, UpdatedAt: r.UpdatedOn,
				})
			}
			url = page.Next
		}
	}

	out := &DiscoveryResult{Identity: id, FetchedAt: time.Now().UTC()}
	for _, o := range sortedOrgs(orgsByName) {
		out.Orgs = append(out.Orgs, o)
	}
	return out, nil
}

func bbGetJSON(ctx context.Context, cli *http.Client, url, token string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	// App Password format: "username:apppassword". If colon present,
	// use Basic auth; else assume bearer token.
	if strings.Contains(token, ":") {
		req.Header.Set("Authorization", "Basic "+basicAuthEncode(token))
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("bitbucket %d on %s", resp.StatusCode, urlPath(url))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func basicAuthEncode(usrPass string) string {
	// stdlib base64; avoid pulling extra imports — write inline.
	const tab = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	src := []byte(usrPass)
	var sb strings.Builder
	for i := 0; i < len(src); i += 3 {
		var b1, b2, b3 byte
		b1 = src[i]
		if i+1 < len(src) {
			b2 = src[i+1]
		}
		if i+2 < len(src) {
			b3 = src[i+2]
		}
		sb.WriteByte(tab[b1>>2])
		sb.WriteByte(tab[((b1&0x03)<<4)|(b2>>4)])
		if i+1 < len(src) {
			sb.WriteByte(tab[((b2&0x0f)<<2)|(b3>>6)])
		} else {
			sb.WriteByte('=')
		}
		if i+2 < len(src) {
			sb.WriteByte(tab[b3&0x3f])
		} else {
			sb.WriteByte('=')
		}
	}
	return sb.String()
}
