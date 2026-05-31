// internal/e2e/p0_527_two_daemon_mtls_test.go is the live-walk
// acceptance gate for #527 Wave T3.1 — boots TWO real chepherd
// binaries, configures cross-pinned CAs out-of-band (by reading
// each daemon's federation cert PEM from its AuthSecrets +
// injecting into the other's pinned-CAs row via AddPinnedCA),
// then drives a federation HTTP call across the mTLS listener +
// asserts the handshake succeeds.
//
// This is the canonical T3.1 acceptance gate the T3 dispatch
// originally deferred. The two-daemon spawn pattern matches the
// existing v092 + 463 + 467 e2e suites — same exec.Command +
// log + cleanup scaffolding.
//
// Refs #527 #487 V0.9.2-ARCHITECTURE.md §15.1 §22.
package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/federation"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

func TestV094Walk_TwoDaemonsCrossPinnedMTLSHandshake(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}

	gomodOut, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(gomodOut))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("repo go.mod not found")
	}
	repoRoot := filepath.Dir(gomod)

	binPath := filepath.Join(t.TempDir(), "chepherd-e2e-t31")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// ─── Daemon A ───────────────────────────────────────────────────
	daemonA := launchDaemon(t, binPath, "org-A")
	// ─── Daemon B ───────────────────────────────────────────────────
	daemonB := launchDaemon(t, binPath, "org-B")

	// Cross-pin: read each daemon's federation cert from its
	// AuthSecrets DB + inject into the OTHER's pinned-CAs row.
	// This emulates the operator-driven cert exchange that
	// production grant-acceptance will eventually wire.
	crossPinCAs(t, daemonA, daemonB)

	// Restart both daemons so the just-injected pinned CAs are
	// loaded into the federation MTLSConfig. The original launch
	// loaded an empty pinned-CA pool; we need fresh LoadOrCreate
	// calls now that the rows are populated.
	daemonA.restart(t)
	daemonB.restart(t)

	// ─── Live cross-org handshake ───────────────────────────────────
	// Daemon B's client (using B's cert + A's pinned CA) dials
	// daemon A's federation listener (presenting A's cert with B's
	// pinned CA verifying B's client cert).
	clientCfg := tls.Config{
		Certificates: []tls.Certificate{daemonB.mtls.Certificate},
		RootCAs:      daemonB.mtls.PinnedCAs,
		MinVersion:   tls.VersionTLS13,
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &clientCfg},
	}
	url := "https://" + daemonA.fedAddr + "/healthz"
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("cross-org mTLS handshake failed: %v\nfedAddr A=%s B=%s", err, daemonA.fedAddr, daemonB.fedAddr)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (handshake OK but app returned non-200)", resp.StatusCode)
	}
}

type daemonHandle struct {
	binPath  string
	orgID    string
	stateDir string
	httpAddr string
	fedAddr  string
	mcpAddr  string
	cmd      *exec.Cmd
	logFile  *os.File
	mtls     *federation.MTLSConfig
}

func launchDaemon(t *testing.T, binPath, orgID string) *daemonHandle {
	t.Helper()
	h := &daemonHandle{
		binPath:  binPath,
		orgID:    orgID,
		stateDir: t.TempDir(),
		httpAddr: fmt.Sprintf("127.0.0.1:%d", freeTCPPort(t)),
		fedAddr:  fmt.Sprintf("127.0.0.1:%d", freeTCPPort(t)),
		mcpAddr:  fmt.Sprintf("127.0.0.1:%d", freeTCPPort(t)),
	}
	h.start(t)
	if err := waitForHTTPOK(h.httpAddr, "/healthz", 10*time.Second); err != nil {
		t.Fatalf("daemon %s /healthz never came up: %v", orgID, err)
	}
	// Wait for the federation listener too (best-effort — it may
	// not be up on the FIRST launch if no pinned CAs yet make the
	// LoadOrCreateMTLS path fail silently).
	t.Cleanup(func() { h.stop(t) })
	return h
}

func (h *daemonHandle) start(t *testing.T) {
	h.cmd = exec.Command(h.binPath,
		"run",
		"--headless",
		"--no-shepherd=true",
		"--listen", h.httpAddr,
		"--mcp-listen", h.mcpAddr,
		"--state-dir", h.stateDir,
		"--federation-mtls=true",
		"--federation-org-id", h.orgID,
		"--federation-listen", h.fedAddr,
		"--federation-registry-url", "http://example.invalid/registry",
	)
	logFile, _ := os.CreateTemp("", fmt.Sprintf("chepherd-e2e-t31-%s-*.log", h.orgID))
	h.logFile = logFile
	h.cmd.Stdout = logFile
	h.cmd.Stderr = logFile
	h.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := h.cmd.Start(); err != nil {
		t.Fatalf("start daemon %s: %v", h.orgID, err)
	}
}

func (h *daemonHandle) stop(t *testing.T) {
	if h.cmd == nil || h.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = h.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
	if t.Failed() && h.logFile != nil {
		if b, err := os.ReadFile(h.logFile.Name()); err == nil {
			t.Logf("daemon %s log:\n%s", h.orgID, b)
		}
	}
}

func (h *daemonHandle) restart(t *testing.T) {
	t.Helper()
	h.stop(t)
	h.start(t)
	if err := waitForHTTPOK(h.httpAddr, "/healthz", 10*time.Second); err != nil {
		t.Fatalf("daemon %s restart /healthz never came up: %v", h.orgID, err)
	}
	// Reload mTLS material so the test client uses the post-restart
	// pinned-CA pool.
	store, err := sqlite.NewStore(context.Background(), filepath.Join(h.stateDir, "chepherd.db"))
	if err != nil {
		t.Fatalf("re-open store %s: %v", h.orgID, err)
	}
	defer store.Close()
	mtls, err := federation.LoadOrCreateMTLS(context.Background(), store.AuthSecrets(), h.orgID)
	if err != nil {
		t.Fatalf("LoadOrCreateMTLS %s: %v", h.orgID, err)
	}
	h.mtls = mtls
}

func crossPinCAs(t *testing.T, a, b *daemonHandle) {
	t.Helper()
	// Pull each daemon's cert from its AuthSecrets DB and inject
	// the OTHER's into its pinned-CAs row. Open each store
	// directly — same code path runtime uses.
	for _, pair := range []struct {
		self *daemonHandle
		peer *daemonHandle
	}{{a, b}, {b, a}} {
		selfStore, err := sqlite.NewStore(context.Background(), filepath.Join(pair.self.stateDir, "chepherd.db"))
		if err != nil {
			t.Fatalf("open %s store: %v", pair.self.orgID, err)
		}
		selfMTLS, err := federation.LoadOrCreateMTLS(context.Background(), selfStore.AuthSecrets(), pair.self.orgID)
		if err != nil {
			selfStore.Close()
			t.Fatalf("LoadOrCreate %s: %v", pair.self.orgID, err)
		}
		_ = selfStore.Close()
		selfCertPEM := pemEncode(selfMTLS.Certificate.Certificate[0])

		// Inject selfCertPEM into peer's pinned-CAs row.
		peerStore, err := sqlite.NewStore(context.Background(), filepath.Join(pair.peer.stateDir, "chepherd.db"))
		if err != nil {
			t.Fatalf("open %s store: %v", pair.peer.orgID, err)
		}
		if err := federation.AddPinnedCA(context.Background(), peerStore.AuthSecrets(), selfCertPEM); err != nil {
			peerStore.Close()
			t.Fatalf("AddPinnedCA %s→%s: %v", pair.self.orgID, pair.peer.orgID, err)
		}
		_ = peerStore.Close()
	}
}

func pemEncode(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// silence unused-import linter when test layout shifts.
var _ = x509.ParseCertificate
