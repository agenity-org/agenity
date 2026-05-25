// Package runtime — BootstrapShepherd: drive a shepherd session into
// its watch loop. Lives in the runtime package (instead of cmd/) so
// the HTTP template-apply path can invoke it for shepherds spawned
// dynamically (not just the cmd/run.go startup shepherd).
package runtime

import (
	"fmt"
	"time"

	"github.com/chepherd/chepherd/internal/ptyhost/session"
)

// BootstrapShepherd starts the shepherd's watch + tick loop on the given
// session. Returns immediately; the loop runs in a goroutine. Calling
// this on a non-shepherd session is a no-op (in case operator confusion).
//
// The hardcoded name "shepherd" in the original cmd/run.go version is
// replaced by the actual agent name so multiple shepherds (one per team)
// can each run their own loop.
func (r *Runtime) BootstrapShepherd(sess *session.Session, agentName string) {
	go r.shepherdLoop(sess, agentName)
}

func (r *Runtime) shepherdLoop(sess *session.Session, name string) {
	// Wait for the Claude TUI to render the trust prompt + welcome.
	// 8s gives the splash animation time to settle before we Enter.
	time.Sleep(8 * time.Second)
	// Accept trust ("Yes, I trust this folder" — Enter).
	_, _ = sess.Write([]byte("\r"))
	// 15s post-trust gives Claude time to: read settings, discover MCP
	// via --mcp-config, render the input box, and become ready to accept
	// typed prompts. 5s (the previous value) was too short — kickoff text
	// arrived mid-splash + got eaten by the renderer (issue #88).
	time.Sleep(15 * time.Second)
	kickoff := fmt.Sprintf(`You are the shepherd named %q. Each tick, do this in order:

1. chepherd.list_memberships(agent=%q) → find your team(s)
2. For each team, chepherd.list_memberships(team=<team>) → find workers (members with role 'worker' or anything non-shepherd)
3. For each worker:
   a. chepherd.read_pane(name=<worker>, lines=60) → observe its state
   b. Council composition: if reviewers in the same team have recorded per-axis assessments, you can read them by calling /api/v1/reviews/<worker>. Use the lowest score per axis (most conservative judgment) when reviewers disagree. If no reviewers exist, use your own judgment from the pane.
   c. chepherd.set_scorecard(name=<worker>, G, V, F, E, D, note=<short evidence>)
   d. chepherd.record_verdict(name=<worker>, verdict='silent'|'praise'|'coach'|'intervene', message=<brief>)
4. Only call chepherd.alert_human when something is HIGH SIGNAL: kind='accomplishment' (PR merged, walk shipped), 'failure' (build broke, security issue), 'stuck' (worker stuck 3+ ticks despite intervention), or 'question' (operator decision needed). Routine observations go to chepherd.note(target=<worker>, body=<obs>) or chepherd.record_event.

For your first observation of a worker, use baseline scores 5/5/5/5/5 with note 'first observation; baseline scores'. From the second tick onward, scores reflect real evidence.`, name, name)
	r.pokeAgent(sess, kickoff)

	// Sentinel: if shepherd doesn't call any MCP tool within 90s, the
	// kickoff was probably eaten by the splash. Re-send it once.
	go func() {
		time.Sleep(90 * time.Second)
		live, _ := r.Get(name)
		if live == nil || live != sess {
			return
		}
		// Check recent events for this shepherd as actor
		events := r.Events(50)
		seen := false
		for _, e := range events {
			if e.Actor == name || (e.Actor == "shepherd" && e.Meta != nil && e.Meta["reviewer"] == name) {
				seen = true
				break
			}
		}
		if !seen {
			r.RecordEvent(Event{
				Kind: "shepherd_kickoff_retry", Actor: "runtime",
				Body: fmt.Sprintf("shepherd %q didn't tool-call in 90s; resending kickoff", name),
			})
			r.pokeAgent(sess, kickoff)
		}
	}()

	// Event-driven: every new spawn (other than this shepherd) triggers an
	// immediate sweep so the operator sees the shepherd react in real time.
	r.AddSpawnHook(func(_ *session.Session, n string) {
		if n == name {
			return
		}
		go func(target string) {
			time.Sleep(3 * time.Second)
			live, _ := r.Get(name)
			if live == nil || live != sess {
				return
			}
			r.pokeAgent(sess, fmt.Sprintf("A new session was just spawned: %q. Do an immediate chepherd.list + chepherd.read_pane(%q, 40) to assess what it's doing, then update its scorecard.", target, target))
		}(n)
	})

	const maxTicksBeforeRefresh = 50
	tickCount := 0
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	for range tick.C {
		live, _ := r.Get(name)
		if live == nil || live != sess {
			return
		}
		tickCount++
		if tickCount >= maxTicksBeforeRefresh {
			r.RecordEvent(Event{
				Kind: "shepherd_refresh", Actor: "runtime",
				Body: fmt.Sprintf("shepherd %q hit tick limit (%d); refreshing for anti-rot", name, maxTicksBeforeRefresh),
			})
			r.pokeAgent(sess, "FINAL TICK before refresh: write a 5-line summary of the current state of your watch via chepherd.record_event(kind='shepherd_handoff', body='<summary>'). I'll spawn a replacement in 10s with this summary as its boot context.")
			return // upstream replaces the shepherd
		}
		r.pokeAgent(sess, fmt.Sprintf("Tick (shepherd %q): chepherd.list_memberships(agent=%q) to find your teams, then for each worker in those teams call chepherd.read_pane → chepherd.set_scorecard → chepherd.record_verdict. Update scores based on what changed since last tick. Stay quiet unless alert_human is needed.", name, name))
	}
}

// pokeAgent writes body + a separate \r to the agent's PTY (the kitty-kbd
// fix from #76: Enter must arrive as a distinct PTY chunk).
func (r *Runtime) pokeAgent(sess *session.Session, body string) {
	_, _ = sess.Write([]byte(body))
	time.Sleep(120 * time.Millisecond)
	_, _ = sess.Write([]byte("\r"))
}
