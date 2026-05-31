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
	// #396 P0 REOPENED — claude-code v2.1+ expects subdirectory-per-
	// skill format: ~/.claude/skills/<skill-name>/SKILL.md, not flat
	// ~/.claude/skills/<skill-name>.md. Operator's /skills still
	// returned "No skills found" after the original #396 close
	// because we shipped the flat-file format. Architect-confirmed
	// via claude-code release notes. Each skill becomes its own
	// directory; SKILL.md inside is what claude-code's /skills
	// surface reads.
	for skillName, body := range renderSkillSet(spec) {
		skillDir := filepath.Join(skillsDir, skillName)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "[chepherd-spawn-briefing] %s: mkdir skill %s: %v\n", spec.Name, skillName, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "[chepherd-spawn-briefing] %s: write skill %s/SKILL.md: %v\n", spec.Name, skillName, err)
		}
	}
	fmt.Fprintf(os.Stderr, "[chepherd-spawn-briefing] %s: wrote skills/<name>/SKILL.md (#396 P0 reopened — subdir format)\n", spec.Name)
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
	fmt.Fprintf(&b, "claude-code surfaces chepherd's MCP tools to you AUTOMATICALLY when configured (you should see them listed when you run `/mcp` and they appear as available tool calls in your context).\n\n")
	fmt.Fprintf(&b, "**Do NOT try to run them as bash commands** — they are NOT shell binaries. `chepherd.send_to_session` is not a path on `$PATH`. Invoke them as native tool calls the same way you'd call `Read` or `Bash` — they're tools, not subprocesses.\n\n")
	fmt.Fprintf(&b, "Available MCP tools (call as native tools, never via Bash):\n")
	fmt.Fprintf(&b, "- `chepherd.send_to_session(name, body)` — message a peer\n")
	fmt.Fprintf(&b, "- `chepherd.list_sessions()` — enumerate current peers\n")
	fmt.Fprintf(&b, "- `chepherd.get_peer_card(name)` — fetch a peer's capabilities (#404 P0.1)\n")
	fmt.Fprintf(&b, "- `chepherd.peer_status(name)` — fetch a peer's live activity (#404 P0.2)\n")
	fmt.Fprintf(&b, "- `chepherd.alert_human(kind, urgency, body)` — escalate to operator\n\n")
	fmt.Fprintf(&b, "**If `/mcp` shows the chepherd server is DISCONNECTED, or you don't see these tools listed in your context** — STOP. That's a chepherd transport bug (#398 family). Report via the operator with: `\"[chepherd] /mcp not connected, can't reach peers\"`. Don't confabulate an explanation; don't pretend the tools work; don't fall back to bash. Just report the gap.\n\n")
	fmt.Fprintf(&b, "Inbound peer messages arrive in your pane prefixed with `[@<sender>]`. Read those as you would a user message.\n\n")
	fmt.Fprintf(&b, "DO NOT write `@<peer>: <message>` in your own output thinking it'll be routed — outbound MCP messages must use the `chepherd.send_to_session` tool call. Only INBOUND `@<me>` lines from peers surface in your pane.\n\n")

	// #475 Wave K4 — V0.9.2-ARCH §10 Pattern 1 step 12-17.
	// Teach the agent the knock pattern: PTY-injected marker →
	// chepherd.get_task → process → reply via stdout. Without this
	// briefing section, agents treat knock lines as noise or user
	// input.
	fmt.Fprintf(&b, "## Inbound peer messages — the knock pattern\n\n")
	fmt.Fprintf(&b, "When another agent sends YOU an A2A task (via their `chepherd.send_to_session` or a federation peer's `message/send`), chepherd writes ONE marker line into your PTY:\n\n")
	fmt.Fprintf(&b, "```\n[chepherd-knock taskID=<uuid> from=<name>]\n```\n\n")
	fmt.Fprintf(&b, "That's the WHOLE notification — no submit sequence, no Enter, no extra newlines. **You** decide when to handle it.\n\n")
	fmt.Fprintf(&b, "**Action when you see a knock**:\n\n")
	fmt.Fprintf(&b, "1. Call the MCP tool `chepherd.get_task(taskID)` with the taskID from the marker — returns the full A2A task envelope `{task, input}` where `input` is the sender's `a2a.Message` with their actual message body in `parts[].text`.\n")
	fmt.Fprintf(&b, "2. Do the task. Read their request, compose your response.\n")
	fmt.Fprintf(&b, "3. Reply via stdout — write your response naturally in your output. chepherd-runner's silence-finalize completer captures everything you write AFTER the knock line and persists it as the agent reply on the task; state transitions WORKING → COMPLETED on idle.\n\n")
	fmt.Fprintf(&b, "**Recipient-scoping**: `chepherd.get_task` returns `-32004 forbidden` if you call it for a task whose contextID isn't your @-handle. Only call get_task for taskIDs from knock markers YOU received.\n\n")
	fmt.Fprintf(&b, "**Don't reply by calling `chepherd.send_to_session` back** — the response routing is automatic through the runner's PTY pump. send_to_session is for INITIATING a peer call (which generates a knock on THEIR side), not for replying to one you received.\n\n")

	fmt.Fprintf(&b, "## How to talk to the operator\n\n")
	fmt.Fprintf(&b, "Use `chepherd.alert_human` MCP tool when you need human attention — they see it in the dashboard's inbox. Use sparingly: the human is the ultimate authority but you're expected to drive work autonomously between escalations.\n\n")

	fmt.Fprintf(&b, "## How to react to team changes\n\n")
	fmt.Fprintf(&b, "When a peer joins/leaves your team or changes role, chepherd injects a notification into your PTY prefixed `[chepherd team-event]`. Read those notifications — they're authoritative team-state updates. Examples:\n\n")
	fmt.Fprintf(&b, "- `[chepherd team-event] `beta` joined team `dev` as `reviewer`` — call `chepherd.get_peer_card(\"beta\")` if their role is relevant to your work\n")
	fmt.Fprintf(&b, "- `[chepherd team-event] `gamma` left team `dev` (was `qa`)` — your handoff target may need re-routing\n")
	fmt.Fprintf(&b, "- `[chepherd team-event] `alpha` role in team `dev`: `worker` → `lead`` — escalations should now go to them, not the previous lead\n\n")
	fmt.Fprintf(&b, "Your `~/.claude/CLAUDE.md` (this file) is REGENERATED ~1 second after each event so the peer list stays current. Reload by running `cat ~/.claude/CLAUDE.md` if you want a fresh view; otherwise rely on the inline notifications + the MCP tools above for live state.\n\n")

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
		"team-orientation": `---
name: team-orientation
description: Orient yourself to the chepherd team you're spawned into — who are your peers, what's their role, how do you reach them
---

# team-orientation

Use this skill any time you need to refresh your understanding of the team you're in.

## Pre-flight: confirm MCP is reachable

` + "`chepherd.*`" + ` tools are claude-code native tool calls, NOT bash commands. Before invoking, run ` + "`/mcp`" + ` and confirm the ` + "`chepherd`" + ` server is connected + the tools are listed. If it shows disconnected, STOP — report ` + "`\"[chepherd] /mcp not connected, can't reach peers\"`" + ` to the operator instead of confabulating.

## Steps

1. Invoke MCP tool ` + "`chepherd.list_sessions`" + ` (as a native tool call, not as bash). Optional arg ` + "`team=\"" + spec.Team + "\"`" + ` to filter.
2. Read the returned list to see peer names, roles, and team membership.
3. For any peer whose pane you want to read, invoke ` + "`chepherd.read_pane`" + ` with their name.
4. Cross-reference against your CLAUDE.md (` + "`~/.claude/CLAUDE.md`" + `) for the canonical team-charter + your role's guidance.

## Output

A one-line summary of "who is in this team and what each is doing right now" suitable for reporting to the operator or a peer.
`,
		"peer-message": `---
name: peer-message
description: Send a message to a peer agent (route via MCP, not @-text)
---

# peer-message

Send a message to another chepherd agent in your team.

## Pre-flight: MCP tool, not bash

` + "`chepherd.send_to_session`" + ` is a NATIVE MCP TOOL CALL, not a shell binary. Don't try ` + "`Bash(chepherd.send_to_session ...)`" + ` — that fails with "command not found". Invoke it the way you'd call ` + "`Read`" + ` or ` + "`Edit`" + `.

If ` + "`/mcp`" + ` shows the chepherd server disconnected, STOP. Report ` + "`\"[chepherd] /mcp not connected, can't reach peers\"`" + ` instead of falling back to bash or guessing.

## Steps

1. Confirm the target peer exists by invoking ` + "`chepherd.list_sessions`" + ` (native tool call). Find a name that matches.
2. Invoke ` + "`chepherd.send_to_session`" + ` with ` + "`name=<peer-name>`" + ` and ` + "`body=<your message>`" + `.
3. The peer sees your message prefixed with ` + "`[@<your-name>]`" + ` in their pane.

## DO NOT

- DO NOT write ` + "`@<peer>: message`" + ` in your own output expecting it to route. Outbound messages MUST use ` + "`chepherd.send_to_session`" + `; that text in your output is just text to the operator. Inbound ` + "`[@<sender>]`" + ` lines from peers DO surface in your pane — that's the protocol's other direction.

## Use this for

- Asking the architect to clarify a spec
- Telling a worker their PR landed clean / failed CI
- Coordinating handoffs between phases of multi-agent work
`,
		"operator-escalation": `---
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
	out["role-"+roleName] = fmt.Sprintf(`---
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
