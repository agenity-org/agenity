package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	stylepkg "github.com/chepherd/chepherd/internal/style"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "First-run setup wizard — config, systemd unit, optional CLAUDE.md integration",
	Long: `Walks a new chepherd user through one-time setup:

  1. Run 'chepherd doctor' (verify prereqs)
  2. Create ~/.config/chepherd/ (config + state dirs)
  3. Show detected tmux sessions + ask which to watch
  4. Detect VCS adapter (github via gh, fallback to local)
  5. Optionally append a "## chepherd integration" section to
     ~/.claude/CLAUDE.md so sessions recognise [SUPERVISOR — …]
     injections (with backup of the original first)
  6. Generate + install systemd --user (Linux) / launchd plist (macOS) unit
  7. Print next-step hints

Non-interactive flags exist for scripted setup (--yes accepts all prompts,
--no-claude-md skips step 5, etc.).`,
	RunE: runInit,
}

var (
	initYes        bool
	initNoClaudeMD bool
	initNoSystemd  bool
)

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVar(&initYes, "yes", false, "auto-accept all prompts")
	initCmd.Flags().BoolVar(&initNoClaudeMD, "no-claude-md", false,
		"skip ~/.claude/CLAUDE.md append step")
	initCmd.Flags().BoolVar(&initNoSystemd, "no-systemd", false,
		"skip systemd unit installation")
}

func runInit(cmd *cobra.Command, args []string) error {
	header("chepherd init — first-run setup")

	// Step 1: doctor
	header("step 1/5 · prerequisites")
	if err := runDoctor(cmd, args); err != nil {
		return err
	}

	// Step 2: config dirs
	header("step 2/5 · config + state directories")
	home, _ := os.UserHomeDir()
	dirs := []string{
		filepath.Join(home, ".config", "chepherd"),
		filepath.Join(home, ".local", "state", "chepherd", "sessions"),
		filepath.Join(home, ".local", "state", "chepherd"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
		fmt.Printf("  %s %s\n",
			stylepkg.Sprint(stylepkg.BandTrusted, "✓"),
			stylepkg.Sprint(stylepkg.Ambient, d))
	}

	// Step 3: detect tmux sessions
	header("step 3/5 · tmux session discovery")
	out, _ := exec.Command("tmux", "ls").CombinedOutput()
	tmuxLines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(tmuxLines) == 1 && tmuxLines[0] == "" {
		fmt.Println(stylepkg.Sprint(stylepkg.Ambient,
			"  no tmux sessions running yet. Start some claude sessions in tmux, then run 'chepherd' to watch them."))
	} else {
		fmt.Println(stylepkg.Sprint(stylepkg.Ambient,
			"  tmux sessions visible to chepherd:"))
		for _, l := range tmuxLines {
			fmt.Println("   ", stylepkg.Sprint(stylepkg.Primary, l))
		}
		fmt.Println(stylepkg.Sprint(stylepkg.Ambient,
			"  chepherd auto-watches sessions matching ^<repo>-<idx>$ "+
				"(e.g. myapp-1). Other names are ignored."))
	}

	// Step 4: CLAUDE.md integration
	header("step 4/5 · ~/.claude/CLAUDE.md integration")
	if initNoClaudeMD {
		fmt.Println(stylepkg.Sprint(stylepkg.Ambient, "  --no-claude-md flag — skipping."))
	} else {
		claudeMD := filepath.Join(home, ".claude", "CLAUDE.md")
		if !confirm(fmt.Sprintf("  append chepherd integration section to %s ? "+
			"(backup will be created)", claudeMD)) {
			fmt.Println(stylepkg.Sprint(stylepkg.Ambient, "  skipped."))
		} else {
			if err := appendChepherdSectionToClaudeMD(claudeMD); err != nil {
				fmt.Printf("  %s %v\n",
					stylepkg.Sprint(stylepkg.APIError, "✗ failed:"), err)
			} else {
				fmt.Println(stylepkg.Sprint(stylepkg.BandTrusted,
					"  ✓ appended (with backup) — new sessions will recognise [SUPERVISOR — …] injections"))
			}
		}
	}

	// Step 5: systemd unit
	header("step 5/5 · daemon lifecycle (systemd --user)")
	if initNoSystemd {
		fmt.Println(stylepkg.Sprint(stylepkg.Ambient, "  --no-systemd flag — skipping."))
	} else if !confirm("  install systemd --user unit for the supervisor daemon ?") {
		fmt.Println(stylepkg.Sprint(stylepkg.Ambient, "  skipped."))
	} else {
		if err := installSystemdUnit(home); err != nil {
			fmt.Printf("  %s %v\n",
				stylepkg.Sprint(stylepkg.APIError, "✗ failed:"), err)
		}
	}

	// Done
	header("done")
	fmt.Println(stylepkg.Sprint(stylepkg.Primary, "  open the dashboard with:"))
	fmt.Println(stylepkg.Sprint(stylepkg.IssueRef, "    chepherd"))
	fmt.Println()
	fmt.Println(stylepkg.Sprint(stylepkg.Primary, "  or create a new watched session:"))
	fmt.Println(stylepkg.Sprint(stylepkg.IssueRef, "    chepherd new --repo ~/path/to/repo"))
	fmt.Println()
	return nil
}

func header(label string) {
	fmt.Println()
	fmt.Println(stylepkg.SprintBold(stylepkg.Title, label))
	fmt.Println(stylepkg.Sprint(stylepkg.TitleRule, strings.Repeat("─", len(label))))
}

func confirm(prompt string) bool {
	if initYes {
		fmt.Println(prompt, "[y/N]:", stylepkg.Sprint(stylepkg.BandTrusted, "y (--yes)"))
		return true
	}
	fmt.Print(prompt, " [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y")
}

func appendChepherdSectionToClaudeMD(path string) error {
	// Read current content.
	current, _ := os.ReadFile(path) // tolerates missing — creates if needed

	// Idempotent: if our marker is already present, no-op.
	if strings.Contains(string(current), "<!-- chepherd:integration:start -->") {
		fmt.Println(stylepkg.Sprint(stylepkg.Ambient,
			"  chepherd section already present — no-op."))
		return nil
	}

	// Backup.
	if len(current) > 0 {
		backup := path + ".chepherd-bak"
		if err := os.WriteFile(backup, current, 0o600); err != nil {
			return fmt.Errorf("create backup: %w", err)
		}
		fmt.Printf("  %s %s\n",
			stylepkg.Sprint(stylepkg.BandTrusted, "✓ backup:"),
			stylepkg.Sprint(stylepkg.Ambient, backup))
	}

	addition := `

<!-- chepherd:integration:start -->
## chepherd integration

This Claude Code session may be watched by chepherd (https://chepherd.org) — a TUI supervisor that scores parallel sessions on goal/velocity/focus/end-state-proximity and coaches them when they drift.

When you receive a user message starting with ` + "`[SUPERVISOR — <refs> | G/V/F/E=<scores>] …`" + `:

1. **Read it.** chepherd has read this same CLAUDE.md as the rubric. The refs (P21, D22, etc.) point to your own principles above.
2. **Acknowledge in 2-4 sentences before your next tool call:**
   - Sentence 1: state the SPECIFIC divergence chepherd caught (prove you read it, not pattern-matched).
   - Sentence 2-3: state your immediate 1-2 concrete next actions (file:line / command / tool call — not aspirations).
   - Then ship the first tool call.
3. **Pushback is legitimate.** If the scorecard is wrong (e.g. you have evidence chepherd missed), say so in the ack with evidence. chepherd updates its read on the next tick.
4. **The G/V/F/E scorecard is a mirror.**
   - G (Goal clarity): does this session know its mission right now?
   - V (Velocity): real delivery last hour (commits, PRs, closures — not typing)
   - F (Focus): on the highest-ICE blocker, or scattered?
   - E (End-state proximity): close to user-visible deliverable?
   - Optimize for high V + E. High G + F without delivery is theater.
5. **Anti-theater:** when chepherd names a SPECIFIC blocker, your next tool calls must touch THAT blocker. Shipping cover-fire commits to deflect IS the anti-pattern.

chepherd does NOT see issue bodies, dependency graphs, or your internal reasoning — only title-level signals. So you decide priority. chepherd nudges; you call.
<!-- chepherd:integration:end -->
`

	// Append (or create if file didn't exist).
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(addition)
	return err
}

func installSystemdUnit(home string) error {
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	binPath, _ := exec.LookPath("chepherd")
	if binPath == "" {
		binPath = filepath.Join(home, ".local", "bin", "chepherd")
	}
	unitPath := filepath.Join(unitDir, "chepherd.service")

	unit := fmt.Sprintf(`[Unit]
Description=chepherd — TUI supervisor for parallel AI coding agents
After=default.target

[Service]
Type=simple
ExecStart=%s shadow
Restart=on-failure
RestartSec=5
StandardOutput=append:%s/.local/state/chepherd/chepherd.log
StandardError=append:%s/.local/state/chepherd/chepherd.log

[Install]
WantedBy=default.target
`, binPath, home, home)

	if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
		return err
	}
	fmt.Printf("  %s %s\n",
		stylepkg.Sprint(stylepkg.BandTrusted, "✓ wrote unit:"),
		stylepkg.Sprint(stylepkg.Ambient, unitPath))

	fmt.Println(stylepkg.Sprint(stylepkg.Primary,
		"  enable with:"))
	for _, c := range []string{
		"    systemctl --user daemon-reload",
		"    systemctl --user enable --now chepherd",
		"    systemctl --user status chepherd",
	} {
		fmt.Println(stylepkg.Sprint(stylepkg.IssueRef, c))
	}
	return nil
}
