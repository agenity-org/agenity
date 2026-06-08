// internal/runtime/repo_url_test.go — pins the lossless-repo-URL contract.
//
// Root cause (operator-reported): sessions returned github_url/repo_url = null,
// so the dashboard decoded the repo from the cwd-encoded path — lossy, because
// the encoding flattens `/ . :` → `-` and (pre-fix) didn't strip `.git`. The
// fix stores the spawn's clone_url ON the session (SessionInfo.RepoURL,
// json:"repo_url,omitempty") BEFORE it's lost to cwd-encoding.
//
// These tests assert (1) the SpawnSpec.RepoURL → SessionInfo.RepoURL
// normalization (ssh→https, .git stripped) and (2) that the field marshals to
// the `repo_url` JSON key the dashboard reads.
package runtime

import (
	"encoding/json"
	"testing"
)

// TestRepoURL_Marshal confirms RepoURL surfaces under the `repo_url` JSON key
// (and is omitted when empty) — the exact key CalmInspector.svelte prefers.
func TestRepoURL_Marshal(t *testing.T) {
	t.Parallel()

	withURL := SessionInfo{Name: "w1", RepoURL: "https://github.com/ping-cash/ping-cash"}
	b, err := json.Marshal(withURL)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["repo_url"] != "https://github.com/ping-cash/ping-cash" {
		t.Errorf("repo_url = %v, want https://github.com/ping-cash/ping-cash", got["repo_url"])
	}

	// Empty RepoURL must be omitted (omitempty) so we never emit repo_url:"".
	empty := SessionInfo{Name: "w2"}
	be, _ := json.Marshal(empty)
	var gotEmpty map[string]any
	_ = json.Unmarshal(be, &gotEmpty)
	if _, present := gotEmpty["repo_url"]; present {
		t.Errorf("repo_url present on empty SessionInfo = %v, want omitted", gotEmpty["repo_url"])
	}
}

// TestRepoURL_NormalizesCloneURL pins the spawn-time normalization that Spawn
// applies to SpawnSpec.RepoURL (githubFromGitURL): ssh/https variants collapse
// to the canonical https form and the trailing .git is stripped. This is the
// transform that makes the dashboard REPO link exact (not .../git, not x/y).
func TestRepoURL_NormalizesCloneURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"https://github.com/ping-cash/ping-cash.git", "https://github.com/ping-cash/ping-cash"},
		{"https://github.com/ping-cash/ping-cash", "https://github.com/ping-cash/ping-cash"},
		{"git@github.com:ping-cash/ping-cash.git", "https://github.com/ping-cash/ping-cash"},
		{"ssh://git@github.com/ping-cash/ping-cash.git", "https://github.com/ping-cash/ping-cash"},
		// Non-github remote (gitea/gitlab): returned raw (minus .git) so the
		// dashboard link still works.
		{"https://gitea.example.com/team/repo.git", "https://gitea.example.com/team/repo"},
	}
	for _, c := range cases {
		if got := githubFromGitURL(c.in); got != c.want {
			t.Errorf("githubFromGitURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
