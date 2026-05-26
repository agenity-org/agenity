package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// GitProviderKind identifies the type of git hosting service.
type GitProviderKind string

const (
	GitProviderGitHub    GitProviderKind = "github"
	GitProviderGitLab    GitProviderKind = "gitlab"
	GitProviderGitea     GitProviderKind = "gitea"
	GitProviderBitbucket GitProviderKind = "bitbucket"
	GitProviderEmbedded  GitProviderKind = "embedded" // internal Gitea pod
)

// GitProvider is a registered git repository + token, persisted in state.
type GitProvider struct {
	ID          string          `json:"id"`           // stable slug derived from URL
	Kind        GitProviderKind `json:"kind"`
	RepoURL     string          `json:"repo_url"`     // e.g. https://github.com/org/repo
	Token       string          `json:"token"`        // access token — stored as-is (state dir is 0700)
	DisplayName string          `json:"display_name"` // user-facing label
	RegisteredAt time.Time      `json:"registered_at"`
}

func gitProvidersPath(stateDir string) string {
	return filepath.Join(stateDir, "git-providers.json")
}

// LoadGitProviders reads all registered providers from state.
func LoadGitProviders(stateDir string) ([]*GitProvider, error) {
	b, err := os.ReadFile(gitProvidersPath(stateDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var providers []*GitProvider
	if err := json.Unmarshal(b, &providers); err != nil {
		return nil, err
	}
	return providers, nil
}

// SaveGitProviders writes the full provider list to state.
func SaveGitProviders(stateDir string, providers []*GitProvider) error {
	b, err := json.MarshalIndent(providers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(gitProvidersPath(stateDir), b, 0o600)
}

// UpsertGitProvider adds or replaces a provider by ID.
func UpsertGitProvider(stateDir string, p *GitProvider) error {
	providers, err := LoadGitProviders(stateDir)
	if err != nil {
		return err
	}
	for i, existing := range providers {
		if existing.ID == p.ID {
			providers[i] = p
			return SaveGitProviders(stateDir, providers)
		}
	}
	providers = append(providers, p)
	return SaveGitProviders(stateDir, providers)
}

// DeleteGitProvider removes a provider by ID.
func DeleteGitProvider(stateDir string, id string) error {
	providers, err := LoadGitProviders(stateDir)
	if err != nil {
		return err
	}
	filtered := providers[:0]
	for _, p := range providers {
		if p.ID != id {
			filtered = append(filtered, p)
		}
	}
	return SaveGitProviders(stateDir, filtered)
}
