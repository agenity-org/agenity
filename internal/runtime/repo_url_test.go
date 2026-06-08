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

// TestNormalizeRepoURL pins the #1 serve-time normalization: ssh→https for
// ANY host, strip .git + trailing slash, and — critically — preserve the
// EXACT owner/repo for dashed names (ping-cash/ping-cash) where cwd-decode
// would mis-split to ping-cash-ping/cash. normalizeRepoURL never touches
// cwd; it normalizes the on-disk remote so the exact repo survives.
func TestNormalizeRepoURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		// The exact reported failure: dashed owner AND repo, lossless.
		{"https://github.com/ping-cash/ping-cash.git", "https://github.com/ping-cash/ping-cash"},
		{"git@github.com:ping-cash/ping-cash.git", "https://github.com/ping-cash/ping-cash"},
		{"ssh://git@github.com/ping-cash/ping-cash.git", "https://github.com/ping-cash/ping-cash"},
		// Trailing slash stripped.
		{"https://github.com/ping-cash/ping-cash/", "https://github.com/ping-cash/ping-cash"},
		{"https://github.com/ping-cash/ping-cash.git/", "https://github.com/ping-cash/ping-cash"},
		// Non-github hosts normalize too (not dropped like githubFromGitURL).
		{"git@gitea.example.com:team/repo.git", "https://gitea.example.com/team/repo"},
		{"ssh://git@gitlab.com/group/sub/repo.git", "https://gitlab.com/group/sub/repo"},
		{"https://gitea.example.com/team/repo.git", "https://gitea.example.com/team/repo"},
		// Empty in → empty out.
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := normalizeRepoURL(c.in); got != c.want {
			t.Errorf("normalizeRepoURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDeriveRepoURL_ReadsOnDiskRemote confirms the serve-time fallback (#1)
// reads `git -C <cwd> config remote.origin.url` and returns the normalized,
// EXACT repo — proving sessions with an empty RepoURL recover the real repo
// from disk instead of guessing from the ambiguous cwd path. Also asserts
// the per-cwd cache avoids a second git exec.
func TestDeriveRepoURL_ReadsOnDiskRemote(t *testing.T) {
	// Not parallel: swaps the package-level execCommand stub.
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })

	var calls int
	var gotArgs []string
	execCommand = func(name string, args ...string) (string, error) {
		calls++
		gotArgs = args
		// Verify we're reading the on-disk remote, not decoding cwd.
		if name != "git" {
			t.Fatalf("expected git exec, got %q", name)
		}
		return "git@github.com:ping-cash/ping-cash.git\n", nil
	}

	r := &Runtime{repoURLCache: make(map[string]string)}
	const cwd = "/home/agent/p0-ping-cash-ping-cash-abc123"
	got := r.deriveRepoURL(cwd)
	if got != "https://github.com/ping-cash/ping-cash" {
		t.Errorf("deriveRepoURL = %q, want https://github.com/ping-cash/ping-cash", got)
	}
	// Second call must hit the cache (no extra git exec).
	if got2 := r.deriveRepoURL(cwd); got2 != got {
		t.Errorf("cached deriveRepoURL = %q, want %q", got2, got)
	}
	if calls != 1 {
		t.Errorf("git exec called %d times, want 1 (cache miss + hit)", calls)
	}
	// The git invocation MUST carry `-c safe.directory=<cwd>` — without it,
	// agent repos owned by a uid-shifted user trip git's "dubious ownership"
	// guard and `config --get` returns empty, silently defeating the fix
	// (verified against the live ping-cash workspace, 2026-06-08).
	if !containsArg(gotArgs, "-c") || !containsArg(gotArgs, "safe.directory="+cwd) {
		t.Errorf("git args = %v, want -c safe.directory=%s present", gotArgs, cwd)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
