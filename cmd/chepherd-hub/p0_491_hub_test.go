// cmd/chepherd-hub/p0_491_hub_test.go pins the v0.9.4 §5 #46 +
// §10 Pattern 2 chepherd-hub scaffold contract (#491 Wave F1).
//
// Unit-fast tests assert each endpoint:
//
//   - Returns the expected HTTP status (200 healthz, 501 stubs)
//   - Carries the correct TODO ref so future operators bisecting
//     "why does my hub return 501" land on the right backlog issue
//   - The /healthz envelope advertises the binary identity + version
//     + per-endpoint TODO map (so dashboards can render the hub's
//     stub status without a separate manifest)
//
// LIVE WALK boots the real binary + curls each endpoint per
// [[feedback_dont_recommend_prompts_without_walking_them]].
//
// Refs #491 V0.9.2-ARCHITECTURE.md §5 #46 §10 Pattern 2.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─── Endpoint contract (unit-fast) ────────────────────────────────

func TestWaveF1_Healthz_ReportsBinaryAndVersion(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newServer(&config{}).mux())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["binary"] != "chepherd-hub" {
		t.Errorf("binary = %v, want chepherd-hub", body["binary"])
	}
	if body["version"] != hubVersion {
		t.Errorf("version = %v, want %s", body["version"], hubVersion)
	}
	stubs, _ := body["stubs"].(map[string]any)
	if stubs == nil {
		t.Fatalf("body.stubs missing: %v", body)
	}
	expected := map[string]string{
		"cards":     "F5",
		"signaling": "F5",
		"stun":      "F3",
		"turn":      "F6",
		"relay":     "F7+F8",
	}
	for k, want := range expected {
		if stubs[k] != want {
			t.Errorf("stubs[%q] = %v, want %s", k, stubs[k], want)
		}
	}
}

func TestWaveF1_Cards_Returns501WithF5TODORef(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newServer(&config{}).mux())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/cards")
	if err != nil {
		t.Fatalf("GET /v1/cards: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["todo_ref"] != "F5 #495" {
		t.Errorf("todo_ref = %v, want F5 #495", body["todo_ref"])
	}
	if !strings.Contains(fmt.Sprint(body["detail"]), "directory aggregator") {
		t.Errorf("detail = %v, want to mention directory aggregator", body["detail"])
	}
}

func TestWaveF1_SignalingRoutes_Each_Returns501WithF5TODORef(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newServer(&config{}).mux())
	defer srv.Close()
	for _, sub := range []string{"offer", "answer", "ice"} {
		path := "/v1/signaling/" + sub
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Errorf("POST %s: %v", path, err)
			continue
		}
		if resp.StatusCode != http.StatusNotImplemented {
			t.Errorf("%s status = %d, want 501", path, resp.StatusCode)
		}
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if body["todo_ref"] != "F5 #495" {
			t.Errorf("%s todo_ref = %v, want F5 #495", path, body["todo_ref"])
		}
	}
}

func TestWaveF1_Relay_Returns501WithF7F8TODORef(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newServer(&config{}).mux())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/relay/some/proxied/path")
	if err != nil {
		t.Fatalf("GET /v1/relay/...: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if got := fmt.Sprint(body["todo_ref"]); got != "F7 #497 + F8 #498" {
		t.Errorf("todo_ref = %q, want F7 #497 + F8 #498", got)
	}
}

// ─── Flag + env parsing ───────────────────────────────────────────

func TestWaveF1_ParseFlags_FlagsBeatEnv(t *testing.T) {
	t.Setenv("CHEPHERD_HUB_LISTEN", ":9999")
	cfg := parseFlags([]string{"--listen", ":4444"})
	if cfg.listen != ":4444" {
		t.Errorf("listen = %q, want :4444 (flag beats env)", cfg.listen)
	}
}

func TestWaveF1_ParseFlags_EnvUsedWhenNoFlag(t *testing.T) {
	t.Setenv("CHEPHERD_HUB_LISTEN", ":7777")
	t.Setenv("CHEPHERD_HUB_TURN_SECRET", "from-env")
	cfg := parseFlags(nil)
	if cfg.listen != ":7777" {
		t.Errorf("listen = %q, want :7777 from env", cfg.listen)
	}
	if cfg.turnSecret != "from-env" {
		t.Errorf("turnSecret = %q, want from-env", cfg.turnSecret)
	}
}

// ─── LIVE WALK: real binary + curl-equivalent each endpoint ───────

func TestV094Walk_F1_BinaryRespondsOnEveryRoute(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live walk in -short")
	}
	gomodOut, _ := exec.Command("go", "env", "GOMOD").Output()
	repoRoot := filepath.Dir(strings.TrimSpace(string(gomodOut)))
	tmpDir := t.TempDir()
	bin := filepath.Join(tmpDir, "chepherd-hub")
	build := exec.Command("go", "build", "-o", bin, "./cmd/chepherd-hub")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build chepherd-hub: %v\n%s", err, out)
	}
	port := freePort(t)
	cmd := exec.Command(bin,
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--stun-listen", "", // disable UDP listeners in this walk
		"--turn-listen", "",
	)
	logFile, _ := os.CreateTemp("", "hub-live-*.log")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_, _ = cmd.Process.Wait()
		if t.Failed() && logFile != nil {
			if b, err := os.ReadFile(logFile.Name()); err == nil {
				t.Logf("hub log:\n%s", b)
			}
		}
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			break
		}
		if err == nil {
			resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Walk every route + assert the status + TODO ref.
	cases := []struct {
		method  string
		path    string
		want    int
		todoRef string
	}{
		{"GET", "/healthz", 200, ""},
		{"GET", "/v1/cards", 501, "F5 #495"},
		{"POST", "/v1/signaling/offer", 501, "F5 #495"},
		{"POST", "/v1/signaling/answer", 501, "F5 #495"},
		{"POST", "/v1/signaling/ice", 501, "F5 #495"},
		{"GET", "/v1/relay/anything", 501, "F7 #497 + F8 #498"},
	}
	for _, c := range cases {
		var resp *http.Response
		var err error
		switch c.method {
		case "GET":
			resp, err = http.Get(baseURL + c.path)
		case "POST":
			resp, err = http.Post(baseURL+c.path, "application/json", strings.NewReader(`{}`))
		}
		if err != nil {
			t.Errorf("%s %s: %v", c.method, c.path, err)
			continue
		}
		if resp.StatusCode != c.want {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Errorf("%s %s = %d, want %d\n%s", c.method, c.path, resp.StatusCode, c.want, b)
			continue
		}
		if c.todoRef != "" {
			var body map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&body)
			if fmt.Sprint(body["todo_ref"]) != c.todoRef {
				t.Errorf("%s %s todo_ref = %v, want %s", c.method, c.path, body["todo_ref"], c.todoRef)
			}
		}
		resp.Body.Close()
	}
	t.Logf("F1 live walk: every route on real chepherd-hub binary responded with expected status + TODO ref")
}

// freePort picks a random TCP port for the live-walk binary boot.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := newListener()
	if err != nil {
		t.Fatalf("listen :0: %v", err)
	}
	defer l.Close()
	return l.port()
}

// tiny helper hiding the listener type so we don't pull in net here.
type freePortListener interface {
	port() int
	Close() error
}

func newListener() (freePortListener, error) { return newTCPZeroListener() }
