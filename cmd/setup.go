// cmd/setup.go — `chepherd setup` first-run wizard (CLI version).
//
// Walks the operator through provider selection + monitored-mode choice +
// folder picking, then writes ~/.config/chepherd/providers.json + stores
// the API key (if any) in the OS keychain. The web-based version (#58)
// will reuse the same provider abstraction; this CLI version is what
// ships in v0.6 before the web client lands (#57).
//
// Renamed from `init` to `setup` to avoid clashing with the legacy
// `chepherd init` subcommand (which is the tmux-era first-run wizard).
package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/chepherd/chepherd/internal/keychain"
	"github.com/chepherd/chepherd/internal/provider"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "v0.6 first-run wizard (CLI) — picks LLM provider + writes config",
	Long: `Interactive first-run setup for chepherd v0.6+ runtime.

Walks you through:
  1. Pick a project folder (default: current dir)
  2. Choose an LLM provider (Claude OAuth, Anthropic API, OpenRouter,
     OpenAI, OpenOva NewAPI, or Ollama)
  3. Paste your API key (stored in OS keychain — macOS Keychain / Linux
     Secret Service / 0600-mode file fallback)
  4. Choose monitored mode (Chepherd-the-shepherd watches Adam, recommended on)

Writes ~/.config/chepherd/providers.json. Run 'chepherd run' afterward.

The browser-based version of this wizard (#58) ships with the web client
in a later release. The CLI wizard remains as the headless / SSH path.`,
	RunE: runSetupCmd,
}

func init() {
	rootCmd.AddCommand(setupCmd)
}

func runSetupCmd(_ *cobra.Command, _ []string) error {
	in := bufio.NewReader(os.Stdin)
	fmt.Println("\n✻ chepherd setup\n")

	// 1. Folder
	defaultCwd, _ := os.Getwd()
	fmt.Printf("Project folder [%s]: ", defaultCwd)
	folder, _ := in.ReadString('\n')
	folder = strings.TrimSpace(folder)
	if folder == "" {
		folder = defaultCwd
	}
	if _, err := os.Stat(folder); err != nil {
		fmt.Printf("  ⚠ folder doesn't exist: %v (will be created on first use)\n", err)
	}
	fmt.Println()

	// 2. Provider
	fmt.Println("Pick an LLM provider:")
	options := []struct {
		kind  provider.Kind
		label string
		help  string
	}{
		{provider.KindClaudeOAuth, "Claude (subscription)", "Use your Claude Pro/Max account (browser OAuth)"},
		{provider.KindAnthropicAPI, "Anthropic API", "Paste an sk-ant-* API key"},
		{provider.KindOpenRouter, "OpenRouter", "Many models, one bill — paste sk-or-* API key"},
		{provider.KindOpenAI, "OpenAI", "Paste an sk-* API key"},
		{provider.KindOpenOvaNewAPI, "OpenOva NewAPI", "Connect to your Sovereign's NewAPI gateway"},
		{provider.KindOllama, "Ollama (local)", "Free local models on http://localhost:11434"},
	}
	for i, o := range options {
		fmt.Printf("  %d. %s — %s\n", i+1, o.label, o.help)
	}
	fmt.Print("\nYour choice [1-6]: ")
	choiceLine, _ := in.ReadString('\n')
	choiceLine = strings.TrimSpace(choiceLine)
	idx := 0
	fmt.Sscanf(choiceLine, "%d", &idx)
	if idx < 1 || idx > len(options) {
		return fmt.Errorf("invalid choice: %q", choiceLine)
	}
	chosen := options[idx-1]
	fmt.Printf("→ Using %s\n\n", chosen.label)

	cfg := provider.Config{Kind: chosen.kind}
	var secret string

	// 3. Credentials
	switch chosen.kind {
	case provider.KindClaudeOAuth:
		fmt.Println("Claude OAuth — open https://claude.ai in your browser, sign in,")
		fmt.Println("then paste the contents of ~/.claude/credentials.json here:")
		fmt.Print("> ")
		secret, _ = in.ReadString('\n')
		secret = strings.TrimSpace(secret)
	case provider.KindAnthropicAPI:
		fmt.Print("Paste your sk-ant-* API key: ")
		secret, _ = in.ReadString('\n')
		secret = strings.TrimSpace(secret)
		if !strings.HasPrefix(secret, "sk-ant-") {
			fmt.Println("  ⚠ key doesn't start with sk-ant- — saving anyway, but health check may fail")
		}
	case provider.KindOpenRouter:
		fmt.Print("Paste your sk-or-* API key: ")
		secret, _ = in.ReadString('\n')
		secret = strings.TrimSpace(secret)
	case provider.KindOpenAI:
		fmt.Print("Paste your OpenAI API key: ")
		secret, _ = in.ReadString('\n')
		secret = strings.TrimSpace(secret)
	case provider.KindOpenOvaNewAPI:
		fmt.Print("Sovereign FQDN (e.g. mysovereign.io): ")
		fqdn, _ := in.ReadString('\n')
		fqdn = strings.TrimSpace(fqdn)
		if fqdn == "" {
			return fmt.Errorf("FQDN required")
		}
		cfg.BaseURL = "https://newapi." + fqdn
		cfg.Label = fqdn
		fmt.Print("Paste your NewAPI bearer token: ")
		secret, _ = in.ReadString('\n')
		secret = strings.TrimSpace(secret)
	case provider.KindOllama:
		fmt.Print("Ollama URL [http://localhost:11434]: ")
		url, _ := in.ReadString('\n')
		url = strings.TrimSpace(url)
		if url == "" {
			url = "http://localhost:11434"
		}
		cfg.BaseURL = url
	}

	// 4. Healthcheck
	if secret != "" || chosen.kind == provider.KindOllama {
		fmt.Print("\nChecking provider... ")
		p, err := provider.Make(cfg, secret)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			base, status, hcErr := p.Healthcheck(ctx)
			if hcErr != nil {
				fmt.Printf("⚠ %v\n", hcErr)
			} else {
				fmt.Printf("%s (base=%s)\n", status, base)
			}
		}
	}

	// 5. Stash secret in keychain
	if secret != "" {
		key := fmt.Sprintf("chepherd.%s.secret", chosen.kind)
		if cfg.Label != "" {
			key = fmt.Sprintf("chepherd.%s.%s.secret", chosen.kind, cfg.Label)
		}
		if err := keychain.Set(key, secret); err != nil {
			return fmt.Errorf("keychain write: %w", err)
		}
		cfg.SecretRef = key
		fmt.Printf("✓ Secret stored in %s\n", keychain.Active().Name())
	}

	// 6. Monitored mode
	fmt.Println()
	fmt.Println("4-eyes principle — give your agent a shepherd to watch its work?")
	fmt.Println("  This is recommended (catches stuck patterns, quality drift, loops).")
	fmt.Print("  Enable shepherd? [Y/n]: ")
	monitorLine, _ := in.ReadString('\n')
	monitored := !strings.HasPrefix(strings.ToLower(strings.TrimSpace(monitorLine)), "n")

	// 7. Write providers.json
	cfgDir := filepath.Join(homeDirOrTmp(), ".config", "chepherd")
	_ = os.MkdirAll(cfgDir, 0o700)
	cfgPath := filepath.Join(cfgDir, "providers.json")
	all := map[string]any{
		"version":           "0.6",
		"default_folder":    folder,
		"monitored":         monitored,
		"default_provider":  cfg,
		"selected_at":       time.Now().UTC(),
	}
	b, _ := json.MarshalIndent(all, "", "  ")
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	fmt.Printf("✓ Config written to %s\n", cfgPath)

	fmt.Println()
	fmt.Println("Done! Start chepherd with:")
	fmt.Println("  chepherd run")
	if !monitored {
		fmt.Println("  chepherd run --unmonitored")
	}
	return nil
}

func homeDirOrTmp() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return os.TempDir()
	}
	return h
}
