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
	_, _ = sess.Inject([]byte("\r"))
	// 15s post-trust gives Claude time to: read settings, discover MCP
	// via --mcp-config, render the input box, and become ready to accept
	// typed prompts. 5s (the previous value) was too short — kickoff text
	// arrived mid-splash + got eaten by the renderer (issue #88).
	time.Sleep(15 * time.Second)

	// Anti-rot context handoff: if a prior shepherd of the same name
	// recorded a shepherd_handoff event before exiting, surface its
	// summary into the new shepherd's kickoff so it picks up where the
	// retired one left off.
	handoff := r.findShepherdHandoff(name)
	if handoff != "" {
		r.pokeAgent(sess, "Inherited handoff from your retired predecessor (you replaced an older shepherd of the same name as part of anti-rot rotation): "+handoff)
		time.Sleep(2 * time.Second)
	}
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

	// Adaptive tick loop: interval is derived from the worst trust band of any
	// worker in the shepherd's team(s). Trusted=30m, Standard=10m,
	// Concerned=5m, Crisis=2m. Fresh shepherds start at Standard (10m) and
	// adapt after the first scorecard lands.
	const maxTicksBeforeRefresh = 50
	tickCount := 0

	// Look up the shepherd's team(s) to scope the band query.
	shepherdTeams := func() []string {
		r.mu.Lock()
		defer r.mu.Unlock()
		id, ok := r.byName[name]
		if !ok {
			return nil
		}
		return r.info[id].Shepherding
	}

	nextInterval := func() time.Duration {
		band := r.TeamWorstBand(shepherdTeams())
		return BandTickInterval(band)
	}

	for {
		interval := nextInterval()
		select {
		case <-time.After(interval):
		}
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
			r.pokeAgent(sess, "FINAL TICK before refresh: write a 5-line summary of the current state of your watch via chepherd.record_event(kind='shepherd_handoff', body='<summary>'). I'll spawn a replacement in 15s with this summary as its boot context. Use kind='shepherd_handoff' EXACTLY — your successor's bootstrap looks it up by kind+actor.")
			go r.respawnShepherd(name, sess)
			return
		}

		// Context-size triggered rotation: compact at 70%, respawn at 90%.
		if _, si := r.Get(name); si != nil {
			if si.ContextSize > 0 && si.ContextTokens > 0 {
				pct := float64(si.ContextTokens) / float64(si.ContextSize)
				if pct >= 0.90 {
					r.RecordEvent(Event{
						Kind: "shepherd_context_respawn", Actor: "runtime",
						Body: fmt.Sprintf("shepherd %q at %.0f%% context; respawning", name, pct*100),
					})
					r.pokeAgent(sess, "CONTEXT LIMIT IMMINENT (≥90%): write a 5-line handoff via chepherd.record_event(kind='shepherd_handoff', body='<summary>') NOW before I respawn you.")
					go r.respawnShepherd(name, sess)
					return
				} else if pct >= 0.70 {
					r.RecordEvent(Event{
						Kind: "shepherd_context_compact", Actor: "runtime",
						Body: fmt.Sprintf("shepherd %q at %.0f%% context; requesting compact", name, pct*100),
					})
					r.pokeAgent(sess, "Context window at 70%+. Run /compact now to summarise and free space before the next tick.")
				}
			}
		}

		band := r.TeamWorstBand(shepherdTeams())
		r.pokeAgent(sess, fmt.Sprintf("Tick (shepherd %q, band=%s): chepherd.list_memberships(agent=%q) to find your teams, then for each worker in those teams call chepherd.read_pane → chepherd.set_scorecard → chepherd.record_verdict. Update scores based on what changed since last tick. Stay quiet unless alert_human is needed.", name, band, name))
	}
}

// pokeAgent injects body + a separate \r to the agent's PTY using Inject
// (not Write) so it doesn't bump lastOperatorWrite and respects writeMu
// against concurrent operator keystrokes (kitty-kbd fix #76).
func (r *Runtime) pokeAgent(sess *session.Session, body string) {
	_, _ = sess.Inject([]byte(body))
	time.Sleep(120 * time.Millisecond)
	_, _ = sess.Inject([]byte("\r"))
}

// respawnShepherd retires the current shepherd session + spawns a
// replacement with the same name + role + team. Called from the tick
// loop when the tick counter hits the rotation limit (anti-rot).
// The replacement reads the prior shepherd's handoff via findShepherdHandoff.
func (r *Runtime) respawnShepherd(name string, oldSess *session.Session) {
	time.Sleep(15 * time.Second) // let the dying shepherd finish writing handoff

	// Get the old shepherd's spawn context so the replacement gets the
	// same team + cwd + system prompt.
	r.mu.Lock()
	id, ok := r.byName[name]
	if !ok {
		r.mu.Unlock()
		return
	}
	info := r.info[id]
	team := info.Team
	cwd := info.Cwd
	agentSlug := info.AgentSlug
	r.mu.Unlock()

	// Stop the retired shepherd (this also unmaps it from byName so the
	// new one can claim the same name).
	_ = r.Stop(name)
	time.Sleep(2 * time.Second)

	// Spawn replacement — same name, same team, same role.
	// SystemPrompt is left empty so the runtime's default prompts.Shepherd
	// (set by HTTP / cmd path) gets re-applied. For programmatic respawn
	// we don't have direct access to the prompts.Shepherd here, so leave
	// it as agent-default and let bootstrapShepherd's kickoff seed it.
	newInfo, newSess, err := r.Spawn(SpawnSpec{
		Name:      name,
		AgentSlug: agentSlug,
		Team:      team,
		Role:      RoleShepherd,
		Cwd:       cwd,
	})
	if err != nil {
		r.RecordEvent(Event{
			Kind: "shepherd_respawn_failed", Actor: "runtime",
			Body: fmt.Sprintf("respawn of shepherd %q failed: %v", name, err),
		})
		return
	}
	r.RecordEvent(Event{
		Kind: "shepherd_respawned", Actor: "runtime",
		Body: fmt.Sprintf("shepherd %q respawned (id=%s) after anti-rot rotation", name, newInfo.ID),
	})
	r.BootstrapShepherd(newSess, name)
}

// findShepherdHandoff scans recent events for a shepherd_handoff record
// authored by an agent with the same name (the retired predecessor).
// Returns its summary body, or "" if no handoff is found.
// Used by anti-rot to seed the fresh replacement shepherd with context.
func (r *Runtime) findShepherdHandoff(name string) string {
	events := r.Events(200)
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Kind == "shepherd_handoff" && e.Actor == name {
			return e.Body
		}
	}
	return ""
}
