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
	time.Sleep(6 * time.Second)
	// Accept trust ("Yes, I trust this folder" — Enter).
	_, _ = sess.Write([]byte("\r"))
	time.Sleep(5 * time.Second)
	const kickoff = "Begin the tick loop from your system brief. For every non-paused worker in your team(s), call chepherd.list then chepherd.read_pane(name, 60), then chepherd.set_scorecard(name, G, V, F, E, D, note) with the 5-axis evaluation AND chepherd.record_verdict(name, verdict, message). Use baseline scores of 5/5/5/5/5 with note 'first observation; baseline scores' for any worker you haven't observed before. Each tick poke means: re-list, re-read, re-score, re-verdict every worker."
	r.pokeAgent(sess, kickoff)

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
		r.pokeAgent(sess, "Tick: chepherd.list + read_pane each non-paused worker. Then chepherd.set_scorecard + chepherd.record_verdict for each — update scores based on what changed since last tick. Stay quiet unless alert_human is needed.")
	}
}

// pokeAgent writes body + a separate \r to the agent's PTY (the kitty-kbd
// fix from #76: Enter must arrive as a distinct PTY chunk).
func (r *Runtime) pokeAgent(sess *session.Session, body string) {
	_, _ = sess.Write([]byte(body))
	time.Sleep(120 * time.Millisecond)
	_, _ = sess.Write([]byte("\r"))
}
