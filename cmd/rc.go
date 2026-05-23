package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	stylepkg "github.com/chepherd/chepherd/internal/style"
)

var rcCmd = &cobra.Command{
	Use:   "rc",
	Short: "chepherd remote-control — drive the dashboard from web/mobile",
	Long: `chepherd rc enables the remote-control subsystem: the local daemon
registers with a relay (default: rc.openova.io) and accepts incoming peer
connections from authorized web/iOS/Android clients.

Two transport modes:

  PRIVACY (default) — WebRTC DataChannel. Client and daemon establish a
                       DTLS-encrypted peer-to-peer channel via the relay's
                       signaling endpoints. The relay sees SDP + ICE for
                       NAT traversal but NEVER decrypts your payload.

  RELAYED (opt-in)  — WebSocket through the relay. Simpler, but the relay
                       sees plaintext-at-app over its TLS connection.
                       Choose this only if WebRTC NAT traversal fails AND
                       you accept that trade-off.

Subcommands:
  chepherd rc enable    register this bastion with the relay + start listening
  chepherd rc disable   remove registration + stop listening
  chepherd rc status    show current rc connection state + peer count
`,
}

var rcEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Register this bastion with the relay + start accepting peer connections",
	RunE:  runRCEnable,
}

var rcDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Remove rc registration + stop listening",
	RunE:  runRCDisable,
}

var rcStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show rc connection state",
	RunE:  runRCStatus,
}

var (
	rcRelayURL     string
	rcMode         string
	rcSelfSignal   bool
	rcSTUNServers  []string
	rcTURNServer   string
)

func init() {
	rootCmd.AddCommand(rcCmd)
	rcCmd.AddCommand(rcEnableCmd, rcDisableCmd, rcStatusCmd)

	rcEnableCmd.Flags().StringVar(&rcRelayURL, "relay",
		"https://rc.openova.io", "relay endpoint (override for self-hosted)")
	rcEnableCmd.Flags().StringVar(&rcMode, "mode",
		"privacy", "transport mode: privacy (WebRTC) or relayed (WebSocket)")
	rcEnableCmd.Flags().BoolVar(&rcSelfSignal, "signaling-self", false,
		"use self-hosted signaling (advanced — bypass openova relay entirely)")
	rcEnableCmd.Flags().StringSliceVar(&rcSTUNServers, "stun", nil,
		"override default STUN server list (default: google + cloudflare public)")
	rcEnableCmd.Flags().StringVar(&rcTURNServer, "turn", "",
		"TURN server URL for symmetric-NAT fallback (empty ⇒ P2P-or-fail)")
}

func runRCEnable(cmd *cobra.Command, args []string) error {
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".config", "chepherd")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("mkdir config: %w", err)
	}

	mode := rcMode
	if mode != "privacy" && mode != "relayed" {
		return fmt.Errorf("--mode must be 'privacy' or 'relayed', got %q", mode)
	}

	fmt.Println()
	fmt.Printf("  %s\n",
		stylepkg.SprintBold(stylepkg.Title, "chepherd rc enable"))
	fmt.Printf("  %s\n",
		stylepkg.Sprint(stylepkg.TitleRule, "──────────────────"))
	fmt.Println()
	fmt.Printf("  %s     %s\n",
		stylepkg.Sprint(stylepkg.Primary, "relay:"),
		stylepkg.Sprint(stylepkg.IssueRef, rcRelayURL))
	fmt.Printf("  %s      %s\n",
		stylepkg.Sprint(stylepkg.Primary, "mode:"),
		stylepkg.Sprint(stylepkg.BandTrusted, mode))
	if mode == "privacy" {
		fmt.Printf("    %s\n",
			stylepkg.Sprint(stylepkg.Ambient,
				"DataChannel data plane — your data NEVER leaves your peers"))
	} else {
		fmt.Printf("    %s\n",
			stylepkg.Sprint(stylepkg.BandConcerned,
				"WebSocket relay — relay sees your payloads (plaintext-at-app over TLS)"))
	}
	fmt.Println()

	// v0.2.0 wires the actual transports here. v0.1 (this binary) prints the
	// config + records intent so chepherd init can verify everything is set
	// up correctly. The daemon-side rc client is being built incrementally;
	// see github.com/chepherd/chepherd/issues/31.
	intentFile := filepath.Join(configDir, "rc.toml")
	intent := fmt.Sprintf(`# Written by 'chepherd rc enable' — do not edit by hand.
# Driven by chepherd's daemon rc client (see #31).

relay       = %q
mode        = %q
self_signal = %v
`, rcRelayURL, mode, rcSelfSignal)
	if err := os.WriteFile(intentFile, []byte(intent), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", intentFile, err)
	}
	fmt.Printf("  %s %s\n",
		stylepkg.Sprint(stylepkg.BandTrusted, "✓ intent written:"),
		stylepkg.Sprint(stylepkg.Ambient, intentFile))
	fmt.Println()
	fmt.Println(stylepkg.Sprint(stylepkg.Primary,
		"  WebRTC + WS transports are implemented (internal/daemon/rc/transport/)."))
	fmt.Println(stylepkg.Sprint(stylepkg.Primary,
		"  Daemon-side listener loop wiring is the next deliverable — tracked in #31."))
	fmt.Println()
	return nil
}

func runRCDisable(cmd *cobra.Command, args []string) error {
	home, _ := os.UserHomeDir()
	intentFile := filepath.Join(home, ".config", "chepherd", "rc.toml")
	if err := os.Remove(intentFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println(stylepkg.Sprint(stylepkg.BandTrusted, "  ✓ chepherd rc disabled"))
	return nil
}

func runRCStatus(cmd *cobra.Command, args []string) error {
	home, _ := os.UserHomeDir()
	intentFile := filepath.Join(home, ".config", "chepherd", "rc.toml")
	if _, err := os.Stat(intentFile); err != nil {
		fmt.Println(stylepkg.Sprint(stylepkg.Ambient,
			"  rc is disabled (no ~/.config/chepherd/rc.toml found)"))
		fmt.Println(stylepkg.Sprint(stylepkg.Ambient,
			"  enable with: chepherd rc enable"))
		return nil
	}
	body, _ := os.ReadFile(intentFile)
	fmt.Println(stylepkg.Sprint(stylepkg.BandTrusted, "  rc is enabled:"))
	fmt.Println()
	fmt.Println(string(body))
	return nil
}
