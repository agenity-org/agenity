package runtimehttp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Composite IDs in URL paths are unsafe in Go's net/http stack.
//
// The /api-v08/ → /api/ rewrite middleware uses r.URL.Path (decoded)
// to rewrite the path; the inner ServeMux then sees the decoded "//"
// in the path and issues a 301 redirect to the path-cleaned form,
// collapsing "https://" → "https:/" and breaking any composite-ID
// lookup downstream. Reproduced live as:
//
//   GET /api-v08/v1/discovery/github%3Ahttps%3A%2F%2Fgithub.com%2F...
//   → 301 Moved Permanently
//   Location: /api/v1/discovery/github:https:/github.com/...
//
// FIX RULE (locked by TestQueryParamSurvivesMuxRedirect below):
//
//   Any endpoint that accepts a composite ID (token-id / id with
//   embedded ':' or '/') MUST accept it as a QUERY PARAMETER, not
//   as a URL path segment. Query parameters are NOT subject to the
//   mux's path-cleaner.
//
// Endpoints currently following this rule:
//   - /api/v1/discovery/?token-id=<composite>
//   - /api/v1/git-providers/?id=<composite>     (DELETE)
//
// Endpoints that take ONLY opaque UUIDs / simple slugs (no ':' or
// '/' ever) MAY safely use path segments:
//   - /api/v1/agents/{uuid}
//   - /api/v1/roles/{slug}
//   - /api/v1/skills/{slug}
//   - /api/v1/team-templates/{slug}
//   - /api/v1/vault/{uuid}
//   - /api/v1/sessions/{name}     (names enforced alphanumeric+`-`)
//
// If a future endpoint starts accepting composite values, switch
// to query-param form BEFORE landing the change.

// TestQueryParamSurvivesMuxRedirect locks the chosen fix path:
// composite IDs in query params reach the handler intact.
func TestQueryParamSurvivesMuxRedirect(t *testing.T) {
	mux := http.NewServeMux()
	var got string
	mux.HandleFunc("/api/v1/discovery/", func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("token-id")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	composite := "github:https://github.com/org/repo"
	encoded := url.QueryEscape(composite)

	resp, err := http.Get(srv.URL + "/api/v1/discovery/?token-id=" + encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200; got %d", resp.StatusCode)
	}
	if got != composite {
		t.Errorf("query-param composite ID did not survive: got %q, want %q", got, composite)
	}
}
