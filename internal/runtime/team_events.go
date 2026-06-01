package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TeamEventKind enumerates the membership-mutation events agents care
// about. Scorecard changes intentionally NOT included — per architect's
// P0.3 scope ("membership-only; scorecard is a separate concern, defer
// to a hypothetical P0.5 if operator asks").
type TeamEventKind string

const (
	TeamEventJoin       TeamEventKind = "join"
	TeamEventLeave      TeamEventKind = "leave"
	TeamEventRoleChange TeamEventKind = "role-change"
)

// teamEvent is the in-process membership-mutation signal emitted by
// Runtime.{Spawn,Stop,JoinTeam,LeaveTeam,UpdateMembershipRole}. Each
// affected SESSION (peer in the same team) gets an immediate PTY
// notification + a debounced briefing regen.
//
// #404 P0.3.
type teamEvent struct {
	Kind    TeamEventKind
	Agent   string // the agent that joined/left/changed role
	Team    string
	OldRole string // populated for role-change only
	NewRole string
	At      time.Time
}

// teamCanonMemberBrief is the per-team-canon member shape. Distinct
// from the per-agent PeerBrief which feeds individual CLAUDE.md
// briefings.
type teamCanonMemberBrief struct {
	Name      string
	Role      string
	AgentSlug string
}

// emitTeamEvent pushes onto r.teamEvents non-blocking. A dropped
// event (full channel) is a non-fatal awareness gap; the next event
// will re-broadcast current state via regeneration so the briefing
// catches up. Caller MUST already hold r.mu.
//
// #404 P0.3.
func (r *Runtime) emitTeamEvent(ev teamEvent) {
	if r.teamEvents == nil {
		return
	}
	select {
	case r.teamEvents <- ev:
	default:
		fmt.Fprintf(os.Stderr, "[chepherd-team-events] dropped %s event for %s/%s (buffer full)\n",
			ev.Kind, ev.Agent, ev.Team)
	}
}

// startTeamEventLoop spawns the goroutine that reads teamEvents and
// fans out PTY notifications + briefing regens to affected sessions.
// Called once from NewWithStore after the runtime is otherwise
// initialised.
//
// #404 P0.3.
func (r *Runtime) startTeamEventLoop() {
	r.teamEvents = make(chan teamEvent, 64)
	r.regenTimers = make(map[string]*time.Timer)
	go r.teamEventLoop()
}

// teamEventLoop is the single fan-out goroutine. For each event:
//  1. Briefing regen — debounced 1s. Cancels any pending regen for
//     the same session + schedules a new one. Burst spawns collapse
//     into one regen at the end of the burst.
//  2. Team-canon materialise — synchronous file write under teams/.
//
// NOTE: PTY stdin injection for team-event notifications was removed.
// Writing \n-prefixed text to PTY stdin triggers claude-code's
// multi-line textarea mode; subsequent CR submit sequences insert
// newlines instead of submitting the knock message. Agents learn
// about team changes via CLAUDE.md regen (step 1) which is the
// correct durable channel. (#615 root-cause / multi-line mode fix)
func (r *Runtime) teamEventLoop() {
	for ev := range r.teamEvents {
		r.fanOutTeamEvent(ev)
	}
}

// fanOutTeamEvent finds every session in the affected team (excluding
// the agent that triggered the event itself) + delivers notification
// + schedules regen + materializes the team-level CLAUDE.md.
func (r *Runtime) fanOutTeamEvent(ev teamEvent) {
	type target struct {
		sessionID    string
		agentHomeDir string
		spec         SpawnSpec
	}
	r.mu.Lock()
	var targets []target
	for id, info := range r.info {
		if info == nil || info.Exited {
			continue
		}
		if info.Team != ev.Team || info.Name == ev.Agent {
			continue
		}
		targets = append(targets, target{
			sessionID:    id,
			agentHomeDir: info.AgentHomeDir,
			spec: SpawnSpec{
				Name:      info.Name,
				Team:      info.Team,
				Role:      info.Role,
				AgentSlug: info.AgentSlug,
			},
		})
	}
	r.mu.Unlock()

	for _, t := range targets {
		r.scheduleBriefingRegen(t.sessionID, t.spec, t.agentHomeDir)
	}

	r.materializeTeamCanon(ev.Team)
}


// renderTeamEventNotification formats the inline notification.
// Single line per architect's spec, prefixed [chepherd team-event].
func renderTeamEventNotification(ev teamEvent) string {
	switch ev.Kind {
	case TeamEventJoin:
		return fmt.Sprintf("\n[chepherd team-event] `%s` joined team `%s` as `%s` — use `chepherd.get_peer_card(\"%s\")` for details\n",
			ev.Agent, ev.Team, ev.NewRole, ev.Agent)
	case TeamEventLeave:
		return fmt.Sprintf("\n[chepherd team-event] `%s` left team `%s` (was `%s`)\n",
			ev.Agent, ev.Team, ev.OldRole)
	case TeamEventRoleChange:
		return fmt.Sprintf("\n[chepherd team-event] `%s` role in team `%s`: `%s` → `%s`\n",
			ev.Agent, ev.Team, ev.OldRole, ev.NewRole)
	default:
		return fmt.Sprintf("\n[chepherd team-event] %s\n", ev.Kind)
	}
}

// scheduleBriefingRegen debounces briefing regeneration for one
// session by 1s. Burst events on the same team collapse into one
// regen at the end of the burst. Per architect: "debounce 1s on
// briefing rewrite (avoid thrash on burst spawns)".
//
// #404 P0.3.
func (r *Runtime) scheduleBriefingRegen(sessionID string, spec SpawnSpec, agentHomeDir string) {
	if agentHomeDir == "" {
		return
	}
	r.regenMu.Lock()
	defer r.regenMu.Unlock()
	if r.regenTimers == nil {
		r.regenTimers = make(map[string]*time.Timer)
	}
	if t, ok := r.regenTimers[sessionID]; ok {
		t.Stop()
	}
	r.regenTimers[sessionID] = time.AfterFunc(1*time.Second, func() {
		peers := r.snapshotPeersForBriefing(spec.Team, spec.Name)
		materializeAgentBriefing(spec, agentHomeDir, peers)
	})
}

// materializeTeamCanon writes the team-level CLAUDE.md at the team's
// canon_path. Triggered on every team event so the canon stays in
// sync with the membership state.
//
// Architect's P0.3 bonus: "the team CLAUDE.md canon_path field
// returned by /api/v1/teams is a LIE — file at that path doesn't
// exist. When you're materializing per-agent briefings on team
// changes, ALSO materialize the team CLAUDE.md at the canon_path."
//
// #404 P0.3.
func (r *Runtime) materializeTeamCanon(teamName string) {
	r.mu.Lock()
	t, ok := r.teams[teamName]
	if !ok {
		r.mu.Unlock()
		return
	}
	canonPath := t.CanonPath
	topology := string(t.Topology)
	var members []teamCanonMemberBrief
	for _, m := range r.memberships {
		if m.TeamName != teamName {
			continue
		}
		var slug string
		for _, info := range r.info {
			if info.Name == m.AgentName {
				slug = info.AgentSlug
				break
			}
		}
		members = append(members, teamCanonMemberBrief{
			Name:      m.AgentName,
			Role:      string(m.Role),
			AgentSlug: slug,
		})
	}
	r.mu.Unlock()
	if canonPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(canonPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "[chepherd-team-canon] mkdir %s: %v\n", filepath.Dir(canonPath), err)
		return
	}
	body := renderTeamCanon(teamName, topology, members)
	if err := os.WriteFile(canonPath, []byte(body), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "[chepherd-team-canon] write %s: %v\n", canonPath, err)
		return
	}
}

// renderTeamCanon emits the team-level CLAUDE.md content.
func renderTeamCanon(team string, topology string, members []teamCanonMemberBrief) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# team `%s` charter\n\n", team)
	fmt.Fprintf(&b, "Topology: `%s`\n\n", topology)
	fmt.Fprintf(&b, "## Current members\n\n")
	if len(members) == 0 {
		fmt.Fprintf(&b, "_No members yet._\n\n")
	} else {
		for _, m := range members {
			fmt.Fprintf(&b, "- **`%s`** — role `%s`, agent `%s`\n", m.Name, m.Role, m.AgentSlug)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## How to coordinate within this team\n\n")
	fmt.Fprintf(&b, "- Use `chepherd.list_sessions` for live peers\n")
	fmt.Fprintf(&b, "- Use `chepherd.get_peer_card(name)` for a peer's capabilities + skills\n")
	fmt.Fprintf(&b, "- Use `chepherd.peer_status(name)` for a peer's live activity\n")
	fmt.Fprintf(&b, "- Use `chepherd.send_to_session(name, body)` to message a peer\n\n")
	fmt.Fprintf(&b, "_Materialized by chepherd on every team membership change._\n")
	return b.String()
}
