package cmd

import (
	"net"
	"net/http"
	"time"
	"github.com/gorilla/websocket"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	stylepkg "github.com/agenity-org/agenity/internal/style"
)

var (
	doctorInstall bool
	doctorMCP     bool
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Verify prerequisites and surface install hints (or auto-install with --install)",
	Long: `Checks that the binaries chepherd depends on are present and working:

  - tmux (terminal multiplexer — session backbone)
  - git  (version control + repo discovery)
  - gh   (GitHub CLI — issue + label operations)
  - claude (Claude Code CLI + subscription credentials at ~/.claude/.credentials.json)

Without --install: shows missing tools + the platform-appropriate install command.
With --install: prompts for confirmation per missing tool, then runs the install.

Never auto-elevates with sudo silently; user-space installs are preferred where
possible.`,
	RunE: runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&doctorInstall, "install", false,
		"Auto-install missing tools with per-tool confirmation")
	doctorCmd.Flags().BoolVar(&doctorMCP, "mcp", false,
		"Diagnose MCP transport from inside an agent container (#414): env vars, DNS, TCP, WS handshake, initialize roundtrip")
}

type checkResult struct {
	name    string
	ok      bool
	version string
	install string
	hint    string
}

func runDoctor(cmd *cobra.Command, args []string) error {
	if doctorMCP {
		return runDoctorMCP()
	}
	checks := []checkResult{
		checkBinary("tmux", "--version", installCmdFor("tmux")),
		checkBinary("git", "--version", installCmdFor("git")),
		checkBinary("gh", "--version", installCmdFor("gh")),
		checkClaudeAndCreds(),
	}

	// Print human-readable summary.
	fmt.Println()
	for _, c := range checks {
		mark := stylepkg.Sprint(stylepkg.BandTrusted, "✓")
		if !c.ok {
			mark = stylepkg.Sprint(stylepkg.BandCrisis, "✗")
		}
		ver := c.version
		if ver == "" && c.ok {
			ver = "(ok)"
		}
		fmt.Printf("  %s %-30s  %s\n", mark, c.name,
			stylepkg.Sprint(stylepkg.Ambient, ver))
		if !c.ok {
			if c.install != "" {
				fmt.Printf("       %s  %s\n",
					stylepkg.Sprint(stylepkg.Primary, "install:"),
					stylepkg.Sprint(stylepkg.IssueRef, c.install))
			}
			if c.hint != "" {
				fmt.Printf("       %s  %s\n",
					stylepkg.Sprint(stylepkg.Primary, "hint:"),
					stylepkg.Sprint(stylepkg.Ambient, c.hint))
			}
		}
	}
	fmt.Println()

	// Count + summary.
	missing := 0
	for _, c := range checks {
		if !c.ok {
			missing++
		}
	}
	if missing == 0 {
		fmt.Println(stylepkg.Sprint(stylepkg.BandTrusted, "  all checks passed."))
		return nil
	}
	fmt.Printf("  %s\n",
		stylepkg.Sprint(stylepkg.BandCrisis,
			fmt.Sprintf("%d missing dependencies.", missing)))

	if !doctorInstall {
		fmt.Printf("\n  %s  %s\n\n",
			stylepkg.Sprint(stylepkg.Primary, "run with --install to auto-install (with per-tool confirmation):"),
			stylepkg.Sprint(stylepkg.IssueRef, "chepherd doctor --install"))
		return nil
	}

	// --install: prompt + run.
	for _, c := range checks {
		if c.ok || c.install == "" {
			continue
		}
		fmt.Printf("\n%s  %s ?  [y/N]: ", c.install,
			stylepkg.Sprint(stylepkg.Primary, "run this command now"))
		var answer string
		_, _ = fmt.Scanln(&answer)
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println(stylepkg.Sprint(stylepkg.Ambient, "  skipped."))
			continue
		}
		if err := runInstallCmd(c.install); err != nil {
			fmt.Printf("  %s %v\n", stylepkg.Sprint(stylepkg.BandCrisis, "install failed:"), err)
		} else {
			fmt.Println(stylepkg.Sprint(stylepkg.BandTrusted, "  installed."))
		}
	}
	return nil
}

func checkBinary(name, versionArg, install string) checkResult {
	path, err := exec.LookPath(name)
	if err != nil {
		return checkResult{name: name, ok: false, install: install}
	}
	out, _ := exec.Command(path, versionArg).CombinedOutput()
	ver := strings.SplitN(string(out), "\n", 2)[0]
	return checkResult{name: name, ok: true, version: ver}
}

func checkClaudeAndCreds() checkResult {
	bin := checkBinary("claude", "--version", installCmdFor("claude"))
	if !bin.ok {
		return bin
	}
	home, _ := os.UserHomeDir()
	credPath := filepath.Join(home, ".claude", ".credentials.json")
	if st, err := os.Stat(credPath); err == nil {
		return checkResult{
			name:    "claude + credentials",
			ok:      true,
			version: fmt.Sprintf("%s · creds %db at %s", bin.version, st.Size(),
				st.ModTime().Format("2006-01-02 15:04")),
		}
	}
	return checkResult{
		name:    "claude + credentials",
		ok:      false,
		hint:    "run `claude auth login` to authenticate (subscription, not API-key)",
	}
}

// installCmdFor returns a platform-appropriate install command for `name`.
func installCmdFor(name string) string {
	switch runtime.GOOS {
	case "darwin":
		switch name {
		case "tmux", "git", "gh":
			return fmt.Sprintf("brew install %s", name)
		case "claude":
			return "curl -fsSL https://claude.ai/install.sh | sh"
		}
	case "linux":
		switch name {
		case "tmux", "git":
			return fmt.Sprintf("sudo apt-get install -y %s  # or dnf/pacman per distro", name)
		case "gh":
			return "see https://github.com/cli/cli#installation"
		case "claude":
			return "npm install -g @anthropic-ai/claude-code  # or `curl -fsSL https://claude.ai/install.sh | sh`"
		}
	}
	return ""
}

func runInstallCmd(cmdStr string) error {
	c := exec.Command("sh", "-c", cmdStr)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// runDoctorMCP diagnoses MCP transport from inside an agent container.
// Captures the data needed to root-cause #414 -32000 without operator
// having to grep podman logs manually. Walks each layer of the
// connection chain + reports pass/fail with detail at each step.
func runDoctorMCP() error {
	fmt.Println()
	fmt.Println(stylepkg.Sprint(stylepkg.Primary, "chepherd doctor --mcp — MCP transport diagnostic (#414)"))
	fmt.Println()

	// Step 1: env vars
	url := os.Getenv("CHEPHERD_MCP_URL")
	if url == "" {
		url = "ws://127.0.0.1:9090/mcp/ws"
	}
	tok := os.Getenv("CHEPHERD_TOKEN")
	netMode := os.Getenv("CHEPHERD_CONTAINER_NETWORK")
	fmt.Printf("  env CHEPHERD_MCP_URL          = %s\n", url)
	fmt.Printf("  env CHEPHERD_TOKEN length     = %d %s\n", len(tok), tokSummary(tok))
	fmt.Printf("  env CHEPHERD_CONTAINER_NETWORK = %s\n", netMode)
	fmt.Println()

	// Step 2: parse URL → host + port
	host, port, scheme := parseWSURL(url)
	fmt.Printf("  parsed: scheme=%s host=%s port=%s\n", scheme, host, port)
	fmt.Println()

	// Step 3: DNS resolve
	addrs, err := net.LookupHost(host)
	if err != nil {
		fmt.Printf("  ✗ DNS resolve %s FAILED: %v\n", host, err)
		fmt.Println()
		fmt.Println("    HINT: host name doesn't resolve. Check chepherd container is on same podman network as agent.")
		fmt.Println("    HINT: if CHEPHERD_MCP_URL uses 'chepherd' but you're on slirp4netns, switch to 'host.containers.internal'.")
		return fmt.Errorf("DNS lookup failed")
	}
	fmt.Printf("  ✓ DNS %s → %v\n", host, addrs)
	fmt.Println()

	// Step 4: TCP connect
	addr := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		fmt.Printf("  ✗ TCP dial %s FAILED: %v\n", addr, err)
		fmt.Println()
		fmt.Println("    HINT: chepherd MCP server isn't listening on that port from the agent's network namespace.")
		fmt.Println("    HINT: on slirp4netns + Podman 3.x, kernel-isolation blocks back-connect to host loopback — switch to chepherd-net (Podman 4.x netavark or CNI plugins).")
		return fmt.Errorf("TCP dial failed")
	}
	conn.Close()
	fmt.Printf("  ✓ TCP dial %s OK\n", addr)
	fmt.Println()

	// Step 5: WS upgrade + initialize
	hdr := http.Header{}
	if tok != "" {
		hdr.Set("Authorization", "Bearer "+tok)
	}
	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 5 * time.Second
	c, resp, err := dialer.Dial(url, hdr)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		fmt.Printf("  ✗ WS handshake FAILED: HTTP %d %v\n", code, err)
		fmt.Println()
		if code == http.StatusUnauthorized {
			fmt.Println("    HINT: HTTP 401 — chepherd MCP server rejected CHEPHERD_TOKEN.")
			fmt.Println("    HINT: token may be stale (chepherd container regenerated since agent spawn) OR token wasn't injected into agent env.")
		} else {
			fmt.Println("    HINT: WS upgrade rejected. Check chepherd MCP server logs for [chepherd-mcp] auth REJECTED entries.")
		}
		return fmt.Errorf("WS dial failed")
	}
	defer c.Close()
	fmt.Printf("  ✓ WS handshake OK\n")
	fmt.Println()

	// Step 6: initialize roundtrip
	initReq := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2024-11-05"},
	}
	if err := c.WriteJSON(initReq); err != nil {
		fmt.Printf("  ✗ initialize WRITE FAILED: %v\n", err)
		return err
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var initResp map[string]any
	if err := c.ReadJSON(&initResp); err != nil {
		fmt.Printf("  ✗ initialize READ FAILED: %v\n", err)
		return err
	}
	if errObj, ok := initResp["error"]; ok {
		fmt.Printf("  ✗ initialize returned ERROR: %v\n", errObj)
		fmt.Println("    HINT: chepherd MCP server received the request but returned an error — likely a bug in the handler.")
		return fmt.Errorf("initialize returned error")
	}
	fmt.Printf("  ✓ initialize OK → %v\n", initResp["result"])
	fmt.Println()
	fmt.Println(stylepkg.Sprint(stylepkg.BandTrusted, "  ALL CHECKS PASS — MCP transport is healthy."))
	fmt.Println("  If claude-code's /mcp still shows ✘ failed, the issue is in claude-code's MCP client, not chepherd.")
	return nil
}

func tokSummary(tok string) string {
	if tok == "" {
		return "(EMPTY — agent's chepherd-spawn env wasn't injected)"
	}
	if len(tok) < 20 {
		return "(suspiciously short — check chepherd token generation)"
	}
	return "(looks well-formed)"
}

func parseWSURL(url string) (host, port, scheme string) {
	scheme = "ws"
	if strings.HasPrefix(url, "wss://") {
		scheme = "wss"
		url = strings.TrimPrefix(url, "wss://")
	} else if strings.HasPrefix(url, "ws://") {
		url = strings.TrimPrefix(url, "ws://")
	}
	if i := strings.Index(url, "/"); i >= 0 {
		url = url[:i]
	}
	host = url
	port = "9090"
	if i := strings.LastIndex(url, ":"); i >= 0 {
		host = url[:i]
		port = url[i+1:]
	}
	return
}
