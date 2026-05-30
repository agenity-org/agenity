package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// materializeAgentBriefing writes the chepherd-specific CLAUDE.md +
// skills/ directory into the per-agent home dir at spawn time.
// Without this, spawned claude-code agents are "vanilla" — they have
// no idea they're inside a chepherd team, can't message peers, and
// answer "who are your siblings" with claude-code's local subagent
// catalog instead of the actual chepherd peer list.
//
// #395 — generates /home/agent/.claude/CLAUDE.md (mounted from
// agentHomeDir/.claude/CLAUDE.md per containerRuntime.AgentHomeDir).
// claude-code reads CLAUDE.md from $HOME/.claude on session start +
// auto-prepends to its system prompt.
//
// #396 — generates /home/agent/.claude/skills/{team-orientation,
// peer-message, operator-escalation, role-<role>}.md so the
// in-agent /skills command surfaces chepherd-specific recipes the
// agent can follow when shepherding work, not just claude-code's
// generic Explore/Plan agent types.
//
// Both writes are best-effort: a failure logs to stderr and the
// spawn continues (the alternative — failing the spawn entirely on
// a docs write — would punish operators in dev/test envs more than
// it would help).
func materializeAgentBriefing(spec SpawnSpec, agentHomeDir string, peers []PeerBrief) {
	claudeDir := filepath.Join(agentHomeDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "[chepherd-spawn-briefing] %s: mkdir .claude: %v\n", spec.Name, err)
		return
	}

	claudeMD := renderAgentClaudeMD(spec, peers)
	if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte(claudeMD), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "[chepherd-spawn-briefing] %s: write CLAUDE.md: %v\n", spec.Name, err)
	} else {
		fmt.Fprintf(os.Stderr, "[chepherd-spawn-briefing] %s: wrote CLAUDE.md (%d peers, role=%s, team=%s) (#395 P0)\n", spec.Name, len(peers), spec.Role, spec.Team)
	}

	skillsDir := filepath.Join(claudeDir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "[chepherd-spawn-briefing] %s: mkdir skills: %v\n", spec.Name, err)
		return
	}
	for name, body := range renderSkillSet(spec) {
		if err := os.WriteFile(filepath.Join(skillsDir, name), []byte(body), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "[chepherd-spawn-briefing] %s: write skill %s: %v\n", spec.Name, name, err)
		}
	}
	fmt.Fprintf(os.Stderr, "[chepherd-spawn-briefing] %s: wrote skills/ (#396 P0)\n", spec.Name)
}

// PeerBrief is the minimal per-peer summary materializeAgentBriefing
// embeds into the per-agent CLAUDE.md. Constructed by the caller
// from the live runtime registry.
type PeerBrief struct {
	Name      string
	Role      string
	AgentSlug string
	Team      string
}

// renderAgentClaudeMD emits the markdown content for
// .claude/CLAUDE.md. Same shape as the chepherd-shell preamble the
// control-plane claude session sees on startup (so spawned agents
// have the same orientation their orchestrator has).
func renderAgentClaudeMD(spec SpawnSpec, peers []PeerBrief) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# chepherd worker briefing — %s\n\n", spec.Name)
	fmt.Fprintf(&b, "You are a worker agent hosted by a Chepherd runtime, working alongside the operator (the human) and possibly other peer agents.\n\n")
	fmt.Fprintf(&b, "## Your identity\n\n")
	fmt.Fprintf(&b, "- **Name**: `%s` (your canonical @-address)\n", spec.Name)
	fmt.Fprintf(&b, "- **Role**: `%s`\n", spec.Role)
	fmt.Fprintf(&b, "- **Team**: `%s`\n", spec.Team)
	fmt.Fprintf(&b, "- **Agent type**: `%s` (claude-code flavor)\n\n", spec.AgentSlug)

	fmt.Fprintf(&b, "## Your peers (snapshot at spawn time)\n\n")
	if len(peers) == 0 {
		fmt.Fprintf(&b, "_No other agents in your team yet — you're the first. Use the `chepherd.list_sessions` MCP tool to check for new peers later._\n\n")
	} else {
		// Stable-sorted so the briefing is reproducible across reruns.
		sortedPeers := append([]PeerBrief(nil), peers...)
		sort.Slice(sortedPeers, func(i, j int) bool { return sortedPeers[i].Name < sortedPeers[j].Name })
		for _, p := range sortedPeers {
			fmt.Fprintf(&b, "- **`%s`** — role `%s`, agent `%s`", p.Name, p.Role, p.AgentSlug)
			if p.Team != "" && p.Team != spec.Team {
				fmt.Fprintf(&b, " (team `%s`)", p.Team)
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "\n_This is a snapshot. Use `chepherd.list_sessions` MCP tool for the LIVE peer list._\n\n")
	}

	fmt.Fprintf(&b, "## How to talk to peers\n\n")
	fmt.Fprintf(&b, "Use the `chepherd.send_to_session` MCP tool with `name=<peer>` and `body=<message>`. The peer will see your message appear in their pane.\n\n")
	fmt.Fprintf(&b, "Inbound peer messages arrive in your pane prefixed with `[@<sender>]`. Read those as you would a user message.\n\n")
	fmt.Fprintf(&b, "DO NOT write `@<peer>: <message>` in your own output thinking it'll be routed — outbound MCP messages must use `chepherd.send_to_session`. Only INBOUND `@<me>` lines from peers surface in your pane.\n\n")

	fmt.Fprintf(&b, "## How to talk to the operator\n\n")
	fmt.Fprintf(&b, "Use `chepherd.alert_human` MCP tool when you need human attention — they see it in the dashboard's inbox. Use sparingly: the human is the ultimate authority but you're expected to drive work autonomously between escalations.\n\n")

	fmt.Fprintf(&b, "## What chepherd is\n\n")
	fmt.Fprintf(&b, "Chepherd is a multi-agent orchestration runtime. It spawns claude-code (and other agent) sessions in containers, gives them the MCP toolkit to talk to each other + the operator, and provides a dashboard for the human to observe + steer the team. The whole point is parallel + peer-to-peer agent work. You're one node in that mesh, not a solo agent.\n\n")

	fmt.Fprintf(&b, "## What good looks like for your role\n\n")
	fmt.Fprintf(&b, "%s\n\n", roleGuidance(string(spec.Role)))

	fmt.Fprintf(&b, "## MCP tools you have\n\n")
	fmt.Fprintf(&b, "- `chepherd.list_sessions` — enumerate current peers\n")
	fmt.Fprintf(&b, "- `chepherd.get_peer_card` — fetch a peer's role, capabilities, skills, current state. Use BEFORE engaging a peer (#404 P0.1)\n")
	fmt.Fprintf(&b, "- `chepherd.peer_status` — fetch a peer's LIVE activity (last_activity_at, idle_seconds, recent PTY excerpt). Use to answer 'what is X doing right now' (#404 P0.2)\n")
	fmt.Fprintf(&b, "- `chepherd.send_to_session` — message a peer\n")
	fmt.Fprintf(&b, "- `chepherd.alert_human` — escalate to operator\n")
	fmt.Fprintf(&b, "- `chepherd.spawn` — spawn a new agent (use sparingly; every peer is real LLM cost)\n")
	fmt.Fprintf(&b, "- `chepherd.note` / `chepherd.record_event` — durable note/event in the shared ledger\n")
	fmt.Fprintf(&b, "- `chepherd.read_canon` / `chepherd.read_pane` — read shared docs / peer panes\n\n")

	fmt.Fprintf(&b, "_Generated by chepherd at spawn time. To regenerate, stop + respawn this agent._\n")
	return b.String()
}

// roleGuidance returns a one-paragraph guidance for the given role.
// Mirrors the operator's mental model of what each role should
// optimize for.
func roleGuidance(role string) string {
	switch strings.ToLower(role) {
	case "shepherd":
		return "You're the team's shepherd — your job is to KEEP THE TEAM ALIGNED + UNBLOCKED, not to do the implementation work yourself. Watch peer panes via `chepherd.read_pane`, route work between peers via `send_to_session`, escalate to operator via `alert_human` when peers are deadlocked. You are READ-ONLY on code repos by default; let workers ship the PRs."
	case "architect", "lead":
		return "You're the team's architect — your job is to DECIDE THE SHAPE of work before workers implement. Read the operator's prompt + the task surface, produce a concrete spec (file paths, function names, types), then dispatch to workers via `chepherd.send_to_session`. Verify workers' output against your spec before reporting completion."
	case "worker":
		return "You're a worker — your job is to SHIP CONCRETE CHANGES (code, docs, configs) in response to architect/operator direction. Read the spec, make the change, run tests, commit, push, report back. Don't over-engineer; ship the smallest correct fix that meets the spec."
	case "qa":
		return "You're QA — your job is to FIND DEFECTS in what other peers ship. Walk the surface end-to-end (UI + API), file issues with reproduction steps, retract closures when verdict-evidence contradicts. Be skeptical by default; trust requires demonstrated coverage."
	default:
		return "Your role is `" + role + "`. Read the operator's brief + this CLAUDE.md, then act in service of the team's goal. When unsure of your specific responsibilities, ask the operator via `chepherd.alert_human` or read related canon via `chepherd.read_canon`."
	}
}

// renderSkillSet returns a map of skill-filename → markdown body.
// claude-code's /skills command surfaces these as discoverable
// recipes. Each one teaches the agent a specific multi-agent
// workflow.
func renderSkillSet(spec SpawnSpec) map[string]string {
	out := map[string]string{
		"team-orientation.md": `---
name: team-orientation
description: Orient yourself to the chepherd team you're spawned into — who are your peers, what's their role, how do you reach them
---

# team-orientation

Use this skill any time you need to refresh your understanding of the team you're in.

## Steps

1. Call MCP tool ` + "`chepherd.list_sessions`" + ` (optionally with ` + "`team=\"" + spec.Team + "\"`" + ` to filter).
2. Read the returned list to see peer names, roles, and team membership.
3. For any peer whose pane you want to read, call ` + "`chepherd.read_pane`" + ` with their name.
4. Cross-reference against your CLAUDE.md (` + "`~/.claude/CLAUDE.md`" + `) for the canonical team-charter + your role's guidance.

## Output

A one-line summary of "who is in this team and what each is doing right now" suitable for reporting to the operator or a peer.
`,
		"peer-message.md": `---
name: peer-message
description: Send a message to a peer agent (route via MCP, not @-text)
---

# peer-message

Send a message to another chepherd agent in your team.

## Steps

1. Confirm the target peer exists: ` + "`chepherd.list_sessions`" + `, find a name that matches.
2. Call ` + "`chepherd.send_to_session`" + ` with ` + "`name=<peer-name>`" + ` and ` + "`body=<your message>`" + `.
3. The peer sees your message prefixed with ` + "`[@<your-name>]`" + ` in their pane.

## DO NOT

- DO NOT write ` + "`@<peer>: message`" + ` in your own output expecting it to route. Outbound messages MUST use ` + "`chepherd.send_to_session`" + `; that text in your output is just text to the operator. Inbound ` + "`[@<sender>]`" + ` lines from peers DO surface in your pane — that's the protocol's other direction.

## Use this for

- Asking the architect to clarify a spec
- Telling a worker their PR landed clean / failed CI
- Coordinating handoffs between phases of multi-agent work
`,
		"operator-escalation.md": `---
name: operator-escalation
description: Escalate to the human operator when peers can't resolve the blocker autonomously
---

# operator-escalation

Use this skill SPARINGLY. The operator is the ultimate authority, but you're expected to drive autonomously between escalations.

## When to escalate

- You + peers have tried 3+ approaches and they all hit the same wall
- You need credentials, account access, or a physical action only the operator can perform
- You found a P0/security defect that requires human verdict before continuing
- The operator explicitly asked for a check-in at a specific point

## When NOT to escalate

- You can ask a peer (use ` + "`peer-message`" + ` skill instead)
- You can look it up in canon (` + "`chepherd.read_canon`" + `)
- You're about to ship a fix and want approval — just ship + report
- It's been < 5 minutes since you last alerted

## Steps

1. Call ` + "`chepherd.alert_human`" + ` with ` + "`kind=<info|warning|failure>`" + ` and ` + "`body=<concise summary>`" + `.
2. ` + "`info`" + ` for FYI, ` + "`warning`" + ` for "I'm choosing path A unless you say otherwise in 10min", ` + "`failure`" + ` for "blocked + need human input now".
3. The operator sees it in the dashboard's inbox. Don't expect a synchronous reply — keep working on what you CAN advance.
`,
	}

	// Role-specific skill — concise companion to the CLAUDE.md role guidance.
	roleName := strings.ToLower(string(spec.Role))
	if roleName == "" {
		roleName = "worker"
	}
	out["role-"+roleName+".md"] = fmt.Sprintf(`---
name: role-%s
description: Your role-specific operating mode for the %s team
---

# role-%s

You were spawned with role ` + "`%s`" + ` on team ` + "`%s`" + `. The full role-charter is in your ` + "`~/.claude/CLAUDE.md`" + ` ("What good looks like for your role"). This skill is the action-checklist version.

%s
`, roleName, spec.Team, roleName, spec.Role, spec.Team, roleSkillChecklist(roleName))

	return out
}

func roleSkillChecklist(role string) string {
	switch role {
	case "shepherd":
		return `## Per-cycle checklist

- Read each peer's pane (` + "`chepherd.read_pane`" + `) at least once per cycle
- If a peer's been silent for > 10min: ` + "`chepherd.read_pane`" + ` + decide if they're stuck or progressing
- If a peer hits a blocker your other peers can solve: route via ` + "`chepherd.send_to_session`" + `
- If the team is deadlocked: ` + "`chepherd.alert_human`" + ` with the specific decision the operator needs to make
- Do NOT make code changes yourself — your job is routing, not implementation`
	case "architect", "lead":
		return `## Per-task checklist

- Read the operator's prompt + relevant canon (` + "`chepherd.read_canon`" + `)
- Produce a concrete spec: file paths, function names, types, acceptance criteria
- Dispatch via ` + "`chepherd.send_to_session`" + ` to the appropriate worker
- When worker reports back, verify their output against your spec before reporting completion to operator`
	case "worker":
		return `## Per-task checklist

- Read the spec the architect/operator sent
- Make the change in the smallest correct way
- Run tests + go vet + go build (or repo equivalent) before committing
- Commit + push + report back via ` + "`chepherd.send_to_session`" + ` to the dispatcher
- If you hit an architectural question: ` + "`peer-message`" + ` the architect, don't guess`
	case "qa":
		return `## Per-cycle checklist

- For each surface labeled "shipped" by a peer: walk it end-to-end (UI + API + edge cases)
- File defects with reproduction steps as GitHub issues
- If a peer closes an issue with insufficient evidence: retract via comment + reopen
- Trust requires demonstrated coverage — be skeptical until you've walked the surface yourself`
	default:
		return `## Per-task checklist

- Re-read your ` + "`~/.claude/CLAUDE.md`" + ` for role-specific guidance
- If your role is unclear, escalate via ` + "`operator-escalation`" + `
- Act in service of the team's goal, not your own preferences`
	}
}
