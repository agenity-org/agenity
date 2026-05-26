// Package runtime — embedded Gitea sidecar (#137).
//
// When the operator picks "Embedded Gitea" in the spawn wizard, chepherd
// boots a gitea/gitea container in the same inner-podman storage as the
// agent containers. The first boot:
//
//   1. starts gitea/gitea with persistent volume at $stateDir/embedded-gitea
//   2. polls /api/v1/version until ready
//   3. creates a single admin user (random password stored in the vault as
//      a gitea-kind provider entry) — credentials persist across restarts
//   4. creates the named repo
//
// Subsequent calls reuse the running container + return the cached
// admin credentials.
package runtime

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	giteaImage         = "docker.io/gitea/gitea:1.21"
	giteaContainerName = "chepherd-gitea"
	giteaInternalPort  = 3000
	giteaAdminUser     = "chepherd-admin"
)

// EmbeddedGiteaInfo describes the live embedded Gitea instance.
type EmbeddedGiteaInfo struct {
	// CloneURLBase is the base URL agents use to clone. Format:
	// http://<user>:<pass>@chepherd-gitea:3000 (network-internal) or
	// http://localhost:3000 (host-side). The runtime picks the one
	// reachable from where the agent will run.
	CloneURLBase string `json:"clone_url_base"`
	AdminUser    string `json:"admin_user"`
	AdminPass    string `json:"admin_pass"`
	// PodmanArgs is the storage-root prefix used when spawning Gitea
	// (e.g. ["--root","/var/lib/chepherd-agents/storage","--runroot",...])
	// — same as how agent containers are spawned. Empty for dev mode.
	PodmanArgs []string `json:"-"`
}

var (
	giteaMu       sync.Mutex
	giteaCached   *EmbeddedGiteaInfo
	giteaStateDir string
)

// EnsureEmbeddedGitea returns a running embedded Gitea, booting it if
// necessary. Thread-safe (sync.Mutex) — concurrent spawn requests share
// a single boot.
//
// repoName: the repo to ensure exists under the admin user. Created
// lazily (re-creation is a no-op if already there).
//
// stateDir: chepherd's state root (e.g. /home/chepherd/.local/state/chepherd).
// Gitea persistence lives under stateDir/embedded-gitea/.
func EnsureEmbeddedGitea(stateDir, repoName string) (*EmbeddedGiteaInfo, error) {
	giteaMu.Lock()
	defer giteaMu.Unlock()

	giteaStateDir = stateDir

	// 1) Pick the podman storage root. Inside the chepherd pod we share
	//    storage with agent containers so Gitea is reachable from them
	//    via the same network.
	podArgs := []string{}
	if _, err := os.Stat(agentStorageRoot); err == nil {
		podArgs = []string{"--root", agentStorageRoot, "--runroot", agentRunRoot}
	}

	// 2) Are we already running? Use podman ps --filter.
	if isGiteaRunning(podArgs) {
		if giteaCached != nil {
			// Touch repo exists — cheap idempotent.
			_ = ensureGiteaRepo(giteaCached, repoName)
			return giteaCached, nil
		}
		// Container is up but we lost the cred cache — try the state file.
		if info, err := loadGiteaCredsFromState(stateDir); err == nil {
			info.PodmanArgs = podArgs
			giteaCached = info
			_ = ensureGiteaRepo(info, repoName)
			return info, nil
		}
		// No cached creds + running container — bail; operator must wipe.
		return nil, fmt.Errorf("embedded-gitea container running but admin credentials lost; remove the container and try again")
	}

	// 2b) Container exists but is stopped — remove it so the run below
	//     doesn't fail with "name already in use".
	if giteaContainerExists(podArgs) {
		rmArgs := append(append([]string{}, podArgs...), "rm", "-f", giteaContainerName)
		_ = exec.Command("podman", rmArgs...).Run()
	}

	// 3) First-boot: pull image if missing, start container.
	dataDir := filepath.Join(stateDir, "embedded-gitea", "data")
	configDir := filepath.Join(stateDir, "embedded-gitea", "config")
	for _, d := range []string{dataDir, configDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	// Pull image (idempotent — podman exits 0 if already present).
	pullArgs := append(append([]string{}, podArgs...), "pull", giteaImage)
	if out, err := exec.Command("podman", pullArgs...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pull %s: %w (%s)", giteaImage, err, strings.TrimSpace(string(out)))
	}

	// Run.
	runArgs := append(append([]string{}, podArgs...),
		"run", "-d",
		"--name", giteaContainerName,
		"--restart", "unless-stopped",
		"--network", "bridge",
		"-v", dataDir+":/data:rw",
		"-v", configDir+":/etc/gitea:rw",
		// Default install-mode env per the Gitea image.
		"-e", "USER_UID=1000",
		"-e", "USER_GID=1000",
		"-e", "GITEA__server__DISABLE_SSH=true",
		"-e", "GITEA__security__INSTALL_LOCK=true",
		"-e", "GITEA__service__DISABLE_REGISTRATION=true",
		"-e", "GITEA__service__REGISTER_EMAIL_CONFIRM=false",
		"-e", "GITEA__database__DB_TYPE=sqlite3",
		giteaImage)
	if out, err := exec.Command("podman", runArgs...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("podman run %s: %w (%s)", giteaImage, err, strings.TrimSpace(string(out)))
	}

	// 4) Resolve container IP (in-pod bridge network) so agent containers
	//    on the same bridge can dial it via http://<ip>:3000.
	giteaIP, err := waitForGiteaIP(podArgs, 20*time.Second)
	if err != nil {
		return nil, fmt.Errorf("gitea ip: %w", err)
	}

	// 5) Wait until /api/v1/version returns 200.
	base := fmt.Sprintf("http://%s:%d", giteaIP, giteaInternalPort)
	if err := waitForGitea(base, 60*time.Second); err != nil {
		return nil, fmt.Errorf("gitea readiness: %w", err)
	}

	// 6) Create admin user. The Gitea image's $USER inside is `git` but
	//    `podman exec` defaults to root and the gitea binary handles
	//    privilege drop itself when called by root with the right
	//    --config flag. We pass --config explicitly so install-mode
	//    settings are picked up.
	pass := randPassword(24)
	// Gitea writes the runtime config at /data/gitea/conf/app.ini (not
	// /etc/gitea/app.ini as one might guess). Pass --config explicitly
	// so `admin user create` finds the right DB config.
	adminCmd := append(append([]string{}, podArgs...),
		"exec", "--user", "git", giteaContainerName,
		"gitea", "--config", "/data/gitea/conf/app.ini", "admin", "user", "create",
		"--username", giteaAdminUser,
		"--password", pass,
		"--email", giteaAdminUser+"@chepherd.local",
		"--admin",
		"--must-change-password=false")
	if out, err := exec.Command("podman", adminCmd...).CombinedOutput(); err != nil {
		// If admin already exists, swallow.
		if !bytes.Contains(out, []byte("already exists")) && !bytes.Contains(out, []byte("user already exists")) {
			return nil, fmt.Errorf("gitea admin user create: %w (%s)", err, strings.TrimSpace(string(out)))
		}
	}

	info := &EmbeddedGiteaInfo{
		CloneURLBase: base,
		AdminUser:    giteaAdminUser,
		AdminPass:    pass,
		PodmanArgs:   podArgs,
	}
	if err := saveGiteaCredsToState(stateDir, info); err != nil {
		return nil, fmt.Errorf("save gitea creds: %w", err)
	}
	giteaCached = info

	// 7) Create the requested repo.
	if err := ensureGiteaRepo(info, repoName); err != nil {
		return nil, fmt.Errorf("create repo: %w", err)
	}
	return info, nil
}

func isGiteaRunning(podArgs []string) bool {
	args := append(append([]string{}, podArgs...), "ps", "--filter", "name="+giteaContainerName, "--format", "{{.Names}}")
	out, err := exec.Command("podman", args...).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == giteaContainerName
}

func giteaContainerExists(podArgs []string) bool {
	args := append(append([]string{}, podArgs...), "ps", "-a", "--filter", "name="+giteaContainerName, "--format", "{{.Names}}")
	out, err := exec.Command("podman", args...).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == giteaContainerName
}

func waitForGiteaIP(podArgs []string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		args := append(append([]string{}, podArgs...), "inspect", giteaContainerName,
			"--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
		out, err := exec.Command("podman", args...).Output()
		if err == nil {
			ip := strings.TrimSpace(string(out))
			if ip != "" && ip != "<nil>" {
				return ip, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("timeout waiting for gitea IP")
}

func waitForGitea(base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/api/v1/version")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(800 * time.Millisecond)
	}
	return fmt.Errorf("timeout")
}

func ensureGiteaRepo(info *EmbeddedGiteaInfo, name string) error {
	if name == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]any{
		"name":           name,
		"auto_init":      true,
		"default_branch": "main",
		"private":        false,
	})
	req, _ := http.NewRequest("POST", info.CloneURLBase+"/api/v1/user/repos", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(info.AdminUser, info.AdminPass)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 201 {
		return nil
	}
	rb, _ := io.ReadAll(resp.Body)
	// 409 = already exists; treat as success.
	if resp.StatusCode == 409 {
		return nil
	}
	return fmt.Errorf("gitea create repo: status %d: %s", resp.StatusCode, string(rb))
}

// CloneURLForRepo builds the embedded-credentials clone URL the agent
// will use. Format: http://<user>:<pass>@<host>:<port>/<user>/<repo>.git
func (g *EmbeddedGiteaInfo) CloneURLForRepo(repoName string) string {
	base := strings.TrimPrefix(g.CloneURLBase, "http://")
	return fmt.Sprintf("http://%s:%s@%s/%s/%s.git", g.AdminUser, g.AdminPass, base, g.AdminUser, repoName)
}

func randPassword(n int) string {
	b := make([]byte, n/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ─── persistent credential store ────────────────────────────────────────────
//
// $stateDir/embedded-gitea/credentials.json — mode 0600, owned by the
// chepherd process. Survives across restarts so the random admin
// password isn't lost.

func saveGiteaCredsToState(stateDir string, info *EmbeddedGiteaInfo) error {
	p := filepath.Join(stateDir, "embedded-gitea", "credentials.json")
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

func loadGiteaCredsFromState(stateDir string) (*EmbeddedGiteaInfo, error) {
	p := filepath.Join(stateDir, "embedded-gitea", "credentials.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var info EmbeddedGiteaInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, err
	}
	return &info, nil
}
