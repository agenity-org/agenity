package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/chepherd/chepherd/internal/shepherd"
	"github.com/chepherd/chepherd/internal/lightsignals"
	stylepkg "github.com/chepherd/chepherd/internal/style"
)

var (
	liveStateDir       string
	livePollingOnly    bool
)

var liveCmd = &cobra.Command{
	Use:   "live",
	Short: "Run the light-signal refresher (cheap polling — no LLM calls)",
	Long: `Continuously refreshes each watched session's local + GitHub state at
a fast cadence (~5 sec) WITHOUT triggering the judge. Updates the
'live_signals' field of each session's state file so the dashboard reflects
near-real-time issue counts, commit activity, TRACKER mtime, and JSONL
last-event age.

Designed to run alongside the judge daemon: judge handles divergence
detection (expensive, infrequent), live handles dashboard freshness (cheap,
constant).

State dir defaults to ~/.local/state/chepherd-shadow/sessions/ — the same
dir 'chepherd shadow' writes to.`,
	RunE: runLive,
}

func init() {
	rootCmd.AddCommand(liveCmd)
	liveCmd.Flags().StringVar(&liveStateDir, "state-dir",
		shepherd.DefaultStateDir(), "where to write live_signals JSON")
	liveCmd.Flags().BoolVar(&livePollingOnly, "polling-only", false,
		"use 5-sec polling instead of fsnotify (fallback for systems "+
			"where fsnotify isn't reliable)")
}

func runLive(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal handling — graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		cancel()
	}()

	fmt.Printf("%s state-dir=%s interval=%s\n",
		stylepkg.Sprint(stylepkg.Logo, "chepherd live"),
		liveStateDir, lightsignals.Interval)

	// Spawn one refresher goroutine per discovered session.
	known := map[string]context.CancelFunc{}
	rediscover := time.NewTicker(30 * time.Second)
	defer rediscover.Stop()

	refreshSessions := func() {
		sessions, err := shepherd.DiscoverSessions()
		if err != nil {
			fmt.Fprintln(os.Stderr, "discovery:", err)
			return
		}
		seenNow := map[string]bool{}
		for _, s := range sessions {
			seenNow[s.UUID] = true
			if _, alive := known[s.UUID]; alive {
				continue
			}
			subCtx, cancel := context.WithCancel(ctx)
			known[s.UUID] = cancel

			if !livePollingOnly {
				edr, err := lightsignals.NewEventDriven(s, liveStateDir)
				if err == nil {
					fmt.Printf("  start refresher: %s (%s) mode=fsnotify\n",
						s.TmuxName, s.UUID[:8])
					go edr.Loop(subCtx)
					continue
				}
				fmt.Printf("  fsnotify init failed for %s (%v) — falling back to polling\n",
					s.TmuxName, err)
			}
			ref := &lightsignals.Refresher{
				Session:  s,
				StateDir: liveStateDir,
			}
			fmt.Printf("  start refresher: %s (%s) mode=polling\n",
				s.TmuxName, s.UUID[:8])
			go ref.Loop(subCtx)
		}
		// Stop refreshers for sessions that vanished.
		for uuid, cancel := range known {
			if !seenNow[uuid] {
				fmt.Printf("  stop refresher: %s (session vanished)\n", uuid[:8])
				cancel()
				delete(known, uuid)
			}
		}
	}

	refreshSessions() // initial spawn
	for {
		select {
		case <-ctx.Done():
			fmt.Println(stylepkg.Sprint(stylepkg.Ambient, "live: stopping"))
			return nil
		case <-rediscover.C:
			refreshSessions()
		}
	}
}
