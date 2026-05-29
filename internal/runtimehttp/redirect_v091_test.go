// internal/runtimehttp/redirect_v091_test.go — pins #223's one-release
// 301 redirect from /v0.9.1/ → /v0.9.2/ so bookmark holders who still
// type the prior URL land on the active dashboard. Removed in v0.9.3.
//
// Refs #223.
package runtimehttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedirect_V091ToV092(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	cases := []struct {
		path     string
		wantPath string
	}{
		{"/v0.9.1/", "/v0.9.2/"},
		{"/v0.9.1/foo", "/v0.9.2/foo"},
		{"/v0.9.1/sub/dir/path", "/v0.9.2/sub/dir/path"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := client.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tc.path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusMovedPermanently {
				t.Errorf("status = %d, want 301", resp.StatusCode)
			}
			if got := resp.Header.Get("Location"); got != tc.wantPath {
				t.Errorf("Location = %q, want %q", got, tc.wantPath)
			}
		})
	}
}

func TestRedirect_V091PreservesQueryString(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(srv.URL + "/v0.9.1/?token=abc&debug=1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("status = %d, want 301", resp.StatusCode)
	}
	want := "/v0.9.2/?token=abc&debug=1"
	if got := resp.Header.Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}
