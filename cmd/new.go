package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	stylepkg "github.com/chepherd/chepherd/internal/style"
)

var (
	newRepo    string
	newClone   string
	newInit    string
	newDir     string
	newName    string
	newResume  string
	newFork    string
	newVCS     string
	newNoLabel bool
)

var newCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a watched Claude Code session in tmux",
	Long: `Creates a tmux session running claude in a target directory and registers
it with the chepherd daemon for supervision.

Modes:
  --repo PATH         use an existing local repository
  --clone URL --dir D clone URL into D first, then start a session
  --init PATH         init a fresh git repo at PATH (sandbox mode)

Optional:
  --name NAME         tmux session name (default: <basename>-<next-N>)
  --resume UUID       resume an existing claude conversation by UUID
  --fork UUID         fork an existing conversation (--fork-session)
  --vcs ADAPTER       force vcs adapter (github/gitlab/gitea/local)

Without flags, 'chepherd new' opens the interactive TUI wizard (W2).`,
	RunE: runNew,
}

func init() {
	rootCmd.AddCommand(newCmd)
	newCmd.Flags().StringVar(&newRepo, "repo", "", "path to existing local repo")
	newCmd.Flags().StringVar(&newClone, "clone", "", "git URL to clone")
	newCmd.Flags().StringVar(&newInit, "init", "", "path to init brand-new repo at")
	newCmd.Flags().StringVar(&newDir, "dir", "", "destination dir for --clone")
	newCmd.Flags().StringVar(&newName, "name", "", "tmux session name (default: <repo>-<N>)")
	newCmd.Flags().StringVar(&newResume, "resume", "", "resume existing claude UUID")
	newCmd.Flags().StringVar(&newFork, "fork", "", "fork existing claude UUID")
	newCmd.Flags().StringVar(&newVCS, "vcs", "", "force vcs adapter (github/gitlab/gitea/local)")
	newCmd.Flags().BoolVar(&newNoLabel, "no-labels", false, "skip creating status/severity labels")
}

func runNew(cmd *cobra.Command, args []string) error {
	// Phase 1: determine the target directory.
	dir, err := resolveTargetDir()
	if err != nil {
		return err
	}

	// Phase 2: determine session name.
	name := newName
	if name == "" {
		name = defaultSessionName(dir)
	}
	if !sessionNameValid(name) {
		return fmt.Errorf("session name %q invalid — must match ^[a-z][a-z0-9_-]+-\\d+$ "+
			"(chepherd's discovery regex)", name)
	}

	// Phase 3: build the claude command line.
	claudeArgs := []string{"--dangerously-skip-permissions"}
	if newResume != "" {
		claudeArgs = append([]string{"--resume", newResume}, claudeArgs...)
	}
	if newFork != "" {
		claudeArgs = append([]string{"--fork-session", "--resume", newFork}, claudeArgs...)
	}

	// Phase 4: ensure tmux session doesn't already exist.
	if tmuxSessionExists(name) {
		return fmt.Errorf("tmux session %q already exists — pick a different --name", name)
	}

	// Phase 5: spawn tmux + claude.
	tmuxCmd := append([]string{"new-session", "-d", "-s", name, "-c", dir, "claude"}, claudeArgs...)
	if err := exec.Command("tmux", tmuxCmd...).Run(); err != nil {
		return fmt.Errorf("tmux new-session failed: %w", err)
	}

	// Phase 6: report.
	fmt.Println()
	fmt.Printf("  %s  %s  %s\n",
		stylepkg.Sprint(stylepkg.BandTrusted, "✓ created tmux session"),
		stylepkg.Sprint(stylepkg.Primary, name),
		stylepkg.Sprint(stylepkg.Ambient, "(cwd: "+dir+")"))
	fmt.Printf("  %s  claude %s\n",
		stylepkg.Sprint(stylepkg.BandTrusted, "✓ launched"),
		strings.Join(claudeArgs, " "))
	fmt.Printf("  %s  %s\n",
		stylepkg.Sprint(stylepkg.BandTrusted, "✓ chepherd will pick this up on the next tick (≤60s)"),
		stylepkg.Sprint(stylepkg.Ambient, ""))

	if newVCS == "github" {
		fmt.Printf("\n  %s\n",
			stylepkg.Sprint(stylepkg.Primary, "label installation requested — install via:"))
		fmt.Printf("    chepherd init --repo %s --labels-only\n", dir)
	}

	fmt.Printf("\n  %s  tmux attach -t %s\n",
		stylepkg.Sprint(stylepkg.Primary, "attach now:"),
		name)
	fmt.Println()
	return nil
}

func resolveTargetDir() (string, error) {
	mods := 0
	if newRepo != "" {
		mods++
	}
	if newClone != "" {
		mods++
	}
	if newInit != "" {
		mods++
	}
	if mods == 0 {
		return "", errors.New("must pick exactly one of --repo / --clone / --init " +
			"(or run interactively without flags for the wizard)")
	}
	if mods > 1 {
		return "", errors.New("conflicting flags — pick one of --repo / --clone / --init")
	}

	switch {
	case newRepo != "":
		p, err := filepath.Abs(newRepo)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("--repo %q: %w", newRepo, err)
		}
		return p, nil
	case newClone != "":
		dest := newDir
		if dest == "" {
			return "", errors.New("--clone requires --dir DEST")
		}
		dest, _ = filepath.Abs(dest)
		if err := exec.Command("git", "clone", newClone, dest).Run(); err != nil {
			return "", fmt.Errorf("git clone failed: %w", err)
		}
		return dest, nil
	case newInit != "":
		p, _ := filepath.Abs(newInit)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return "", err
		}
		if err := exec.Command("git", "-C", p, "init", "-b", "main").Run(); err != nil {
			return "", fmt.Errorf("git init failed: %w", err)
		}
		return p, nil
	}
	return "", errors.New("unreachable")
}

func defaultSessionName(dir string) string {
	base := filepath.Base(dir)
	// Sanitize to lower [a-z0-9_-]
	var b strings.Builder
	for _, r := range strings.ToLower(base) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	clean := b.String()
	// Find next free N
	for n := 1; n <= 99; n++ {
		candidate := fmt.Sprintf("%s-%d", clean, n)
		if !tmuxSessionExists(candidate) {
			return candidate
		}
	}
	return clean + "-99"
}

var sessionNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]+-\d+$`)

func sessionNameValid(s string) bool {
	return sessionNameRE.MatchString(s)
}

func tmuxSessionExists(name string) bool {
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}
