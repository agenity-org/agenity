package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	stylepkg "github.com/chepherd/chepherd/internal/style"
)

var doctorInstall bool

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
}

type checkResult struct {
	name    string
	ok      bool
	version string
	install string
	hint    string
}

func runDoctor(cmd *cobra.Command, args []string) error {
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
