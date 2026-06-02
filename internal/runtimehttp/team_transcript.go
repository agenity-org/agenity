// Package runtimehttp — Team Transcript (#657/#658/#659 epic #654).
//
// Endpoints:
//
//	POST /api/v1/teams/{name}/messages        — operator/agent posts to the team channel
//	GET  /api/v1/teams/{name}/messages        — fetch recent transcript (Team Transcript UI)
//	GET  /api/v1/transcript?teams=…           — merged multi-team feed in ONE round-trip (#668)
//	GET  /api/v1/teams/{name}/ticket-mentions — per-ticket mention counts for kanban badges (#665 BE)
//
// Fan-out: each POST parses @-mentions, persists one ChannelMessage row (the
// unified transcript), then spawns one A2A message/send per recipient (the
// existing knock pattern wakes each addressed agent). The human operator sees
// every message via GET regardless of recipient.
//
// v0.9.3+ feature additions:
//   - #662 default-route operator messages without @-mention to team lead role
//     (resolveTeamLead → scrum-master > tech-lead > orchestrator > architect >
//     first worker > "operator" fallback). Persisted message gets
//     routed_to_default=true + default_target=@<lead> in the GET response.
//   - #663 per-team github_url for #-ticket links. Derived from any live
//     member's GitHubURL (same source the kanban widget uses). Included in
//     every GET response + per-row in the multi-team merged endpoint.
//   - #667 alert-kind stripe metadata. Messages routed via the alert_human MCP
//     tool land in Runtime.Inbox() with a "[<kind>] " prefix on body; merged
//     into the transcript with kind="alert:<kind>" so the UI can render
//     colored left-stripes (failure=red, stuck=orange, question=amber,
//     accomplishment=green). Normal sends carry kind="message".
//   - #668 merged multi-team endpoint — frontend "all" scope used to iterate
//     N team fetches; new GET /api/v1/transcript?teams=all|csv returns ONE
//     payload {teams, messages} with each row tagged by its team.
//   - #665 BE ticket-mentions map for the kanban "💬 N" badge.
package runtimehttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/runtime"
	"github.com/google/uuid"
)

// mentionRegex matches @-mentions: @everyone, @team-<name>, @<agent-name>.
// Agent + team names follow the chepherd convention: alphanumeric + dash/underscore.
var mentionRegex = regexp.MustCompile(`@([a-zA-Z][a-zA-Z0-9_-]*)`)

// ticketRegex matches #-tickets the way the frontend auto-links them. Used by
// the per-ticket mentions-count endpoint (#665 BE). Kept loose: a # followed
// by one or more digits, with a non-word boundary in front so "ax#3" doesn't
// match but "see #651 fixed" does.
var ticketRegex = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])#([0-9]+)`)

// alertKindPrefixRegex parses the "[<kind>] " prefix the mcpserver alert_human
// handler prepends to HumanInbox body (server.go:811). Captures the kind
// token. We re-use the same kind enum the dashboard already knows about
// (accomplishment | failure | stuck | question) — anything else is exposed
// as "alert:<raw>" so the UI can still render the message with a generic
// stripe.
var alertKindPrefixRegex = regexp.MustCompile(`^\[([a-zA-Z][a-zA-Z0-9_:.-]*)\]\s+`)

// parseMentions extracts every @-handle from msg body. Returns lower-cased
// handles in source order; deduplicated.
func parseMentions(body string) []string {
	matches := mentionRegex.FindAllStringSubmatch(body, -1)
	seen := map[string]struct{}{}
	out := []string{}
	for _, m := range matches {
		name := m[1]
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// parseTickets extracts every #<num> token from body. Returns dedupe'd numeric
// strings in source order (preserves "#651" → "651"). Used by the
// /ticket-mentions endpoint (#665 BE) so the kanban widget can show
// "💬 N" badges per card.
func parseTickets(body string) []string {
	matches := ticketRegex.FindAllStringSubmatch(body, -1)
	seen := map[string]struct{}{}
	out := []string{}
	for _, m := range matches {
		num := m[1]
		if _, dup := seen[num]; dup {
			continue
		}
		seen[num] = struct{}{}
		out = append(out, num)
	}
	return out
}

// resolveRecipients expands mentions to concrete @-handles given the team's
// member list. @everyone → all team members. @team-<name> → that team's
// members (best-effort). @<agent-name> → that agent if in team.
func resolveRecipients(mentions []string, teamMembers []string, allTeams func(string) []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, m := range mentions {
		switch {
		case m == "everyone":
			for _, member := range teamMembers {
				if _, dup := seen[member]; dup {
					continue
				}
				seen[member] = struct{}{}
				out = append(out, member)
			}
		case strings.HasPrefix(m, "team-") && allTeams != nil:
			for _, member := range allTeams(strings.TrimPrefix(m, "team-")) {
				if _, dup := seen[member]; dup {
					continue
				}
				seen[member] = struct{}{}
				out = append(out, member)
			}
		default:
			if _, dup := seen[m]; dup {
				continue
			}
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	return out
}

// teamMembersOf returns the @-handles of agents on a given team.
func (s *Server) teamMembersOf(team string) []string {
	if s.rt == nil {
		return nil
	}
	var out []string
	for _, info := range s.rt.List() {
		if info != nil && info.Team == team {
			out = append(out, info.Name)
		}
	}
	return out
}

// leadRolePriority is the canonical lead-role precedence used by
// resolveTeamLead (#662). Lower index wins.
var leadRolePriority = []string{
	"scrum-master",
	"tech-lead",
	"orchestrator",
	"architect",
	// Shepherd is functionally similar to scrum-master in chepherd's catalog
	// and is the SessionInfo.Role value scrum-master agents are assigned;
	// kept between architect and worker so legacy-shaped teams still resolve.
	"shepherd",
	"worker",
}

// rolePriorityIndex returns the canonical lead-role priority (lower = wins).
// Unknown roles get a high sentinel so they only win as the final fallback
// (after named workers).
func rolePriorityIndex(role string) int {
	role = strings.ToLower(strings.TrimSpace(role))
	for i, r := range leadRolePriority {
		if r == role {
			return i
		}
	}
	return len(leadRolePriority) + 1
}

// resolveTeamLead returns the canonical lead @-handle for a team based on
// member roles (#662). Priority: scrum-master > tech-lead > orchestrator >
// architect > shepherd > first worker > "operator" fallback.
//
// Role data is sourced from Runtime.ListMemberships (the v0.6 unified-model
// home for per-team roles) with fallback to SessionInfo.Role for legacy
// teams that haven't been migrated yet. When multiple agents share the
// winning role, the alphabetically-first @-handle wins (stable, no
// timestamp dependency).
func (s *Server) resolveTeamLead(team string) string {
	if s.rt == nil {
		return "operator"
	}
	// Source 1: explicit memberships (v0.6 unified-model — has rich roles).
	type candidate struct {
		name     string
		priority int
	}
	var cands []candidate
	for _, m := range s.rt.ListMemberships("", team) {
		if m == nil || m.AgentName == "" {
			continue
		}
		cands = append(cands, candidate{name: m.AgentName, priority: rolePriorityIndex(string(m.Role))})
	}
	// Source 2: SessionInfo.Role for legacy teams (only "worker"|"shepherd"
	// in the v0.5 model — but enough to pick a shepherd as lead when no
	// membership row exists).
	seen := map[string]struct{}{}
	for _, c := range cands {
		seen[c.name] = struct{}{}
	}
	for _, info := range s.rt.List() {
		if info == nil || info.Team != team {
			continue
		}
		if _, ok := seen[info.Name]; ok {
			continue
		}
		cands = append(cands, candidate{name: info.Name, priority: rolePriorityIndex(string(info.Role))})
	}
	if len(cands) == 0 {
		return "operator"
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].priority != cands[j].priority {
			return cands[i].priority < cands[j].priority
		}
		return cands[i].name < cands[j].name
	})
	return cands[0].name
}

// resolveTeamLeadRole returns the role string that drove the lead pick (used
// by the GET response so the UI can render "↪ routed to @X (scrum-master)").
// Returns "operator" when no candidates exist.
func (s *Server) resolveTeamLeadRole(team, lead string) string {
	if s.rt == nil || lead == "" || lead == "operator" {
		return "operator"
	}
	for _, m := range s.rt.ListMemberships(lead, team) {
		if m != nil && m.Role != "" {
			return string(m.Role)
		}
	}
	for _, info := range s.rt.List() {
		if info != nil && info.Name == lead && info.Team == team {
			return string(info.Role)
		}
	}
	return "operator"
}

// teamGitHubURL derives the repo URL for a team from any live member's
// GitHubURL (#663). Same source the kanban widget uses (SessionInfo.GitHubURL
// is set at spawn from `git config --get remote.origin.url` against the
// agent's cwd). Returns "" when no live member has a GitHubURL — caller
// renders #N as plain text in that case.
//
// Algorithm: scan rt.List() for the first agent on this team with a
// non-empty GitHubURL. We trim a trailing ".git" so the URL composes with
// "/issues/N" cleanly. We don't probe SessionStore (persisted-but-not-live)
// because the kanban widget uses live data too — staying consistent.
func (s *Server) teamGitHubURL(team string) string {
	if s.rt == nil {
		return ""
	}
	for _, info := range s.rt.List() {
		if info == nil || info.Team != team {
			continue
		}
		u := strings.TrimSpace(info.GitHubURL)
		if u == "" {
			continue
		}
		// Normalize: trim trailing ".git" (`git@github.com:foo/bar.git` style
		// won't compose with /issues/N — but we handle the common https case
		// first; SSH normalization is a follow-up if needed).
		u = strings.TrimSuffix(u, ".git")
		return u
	}
	return ""
}

// ensureTeamChannel idempotently creates the persistent channel record
// backing a team's transcript (#655 ChannelRepository). Members are
// re-sync'd from the live team roster so newly-spawned agents auto-appear.
func (s *Server) ensureTeamChannel(ctx context.Context, team string) (*persistence.Channel, error) {
	store := s.SessionStore
	_ = store // unused; channels live on the dedicated repo
	if s.ChannelStore == nil {
		return nil, http.ErrServerClosed // sentinel — caller surfaces 503
	}
	name := "#" + team
	ch, err := s.ChannelStore.GetByName(ctx, name)
	if err != nil || ch == nil {
		ch = &persistence.Channel{
			ID:         uuid.NewString(),
			Name:       name,
			Kind:       "team",
			CreatedBy:  "operator",
			CreatedAt:  time.Now().UTC(),
			Visibility: "irc",
		}
		if err := s.ChannelStore.Save(ctx, ch); err != nil {
			return nil, err
		}
	}
	// Always include the operator.
	_ = s.ChannelStore.AddMember(ctx, &persistence.ChannelMember{
		ChannelID: ch.ID, Member: "operator", JoinedAt: time.Now().UTC(),
	})
	// Sync live team members.
	for _, m := range s.teamMembersOf(team) {
		_ = s.ChannelStore.AddMember(ctx, &persistence.ChannelMember{
			ChannelID: ch.ID, Member: m, JoinedAt: time.Now().UTC(),
		})
	}
	return ch, nil
}

// postMessageRequest is the operator/agent compose payload.
type postMessageRequest struct {
	Author string `json:"author"` // optional; defaults to "operator" for human posts
	Body   string `json:"body"`
}

// transcriptRow is what the GET endpoint returns per row.
//
// Frontend contract (per #662/#663/#667/#668 DoD):
//   - kind ∈ {"message", "alert:question", "alert:failure", "alert:stuck",
//     "alert:accomplishment", "alert:<other>"}
//   - routed_to_default=true + default_target="@<lead>" when an operator
//     post had no explicit @-mentions and was auto-routed
//   - team_github_url is the per-row repo URL the UI uses to compose #N
//     links (carried per-row so the multi-team merged feed links to each
//     team's own repo, not just the channel's primary team)
//   - team is the team the row belongs to (set when row appears in the
//     merged multi-team feed; empty in per-team endpoints since the channel
//     already identifies the team)
type transcriptRow struct {
	ID               string    `json:"id"`
	Author           string    `json:"author"`
	Body             string    `json:"body"`
	Mentions         []string  `json:"mentions"`
	Recipients       []string  `json:"recipients"`
	CreatedAt        time.Time `json:"created_at"`
	Kind             string    `json:"kind"`
	RoutedToDefault  bool      `json:"routed_to_default,omitempty"`
	DefaultTarget    string    `json:"default_target,omitempty"`
	Team             string    `json:"team,omitempty"`
	TeamGitHubURL    string    `json:"team_github_url,omitempty"`
}

// teamMessagesHandler routes POST/GET to the team transcript endpoints.
func (s *Server) teamMessagesHandler(w http.ResponseWriter, r *http.Request) {
	// /api/v1/teams/{name}/messages
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/teams/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[1] != "messages" {
		http.NotFound(w, r)
		return
	}
	team := parts[0]
	if team == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "team required"})
		return
	}
	if s.ChannelStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "channel store not configured"})
		return
	}

	ctx := r.Context()
	ch, err := s.ensureTeamChannel(ctx, team)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "ensure channel: " + err.Error()})
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.teamTranscriptGet(w, r, ch, team)
	case http.MethodPost:
		s.teamTranscriptPost(w, r, ch, team)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

// collectTranscriptRows returns the merged transcript for a single team.
// Shared by the per-team GET (teamTranscriptGet) and the multi-team merged
// endpoint (transcriptMultiTeamHandler #668) so both surfaces apply the
// same merge logic + tagging.
func (s *Server) collectTranscriptRows(ctx context.Context, ch *persistence.Channel, team string) []transcriptRow {
	teamURL := s.teamGitHubURL(team)
	lead := s.resolveTeamLead(team)
	rows := make([]transcriptRow, 0, 64)

	// 1. ChannelMessage rows (operator-initiated via POST /api/v1/teams/.../messages).
	msgs, _ := s.ChannelStore.Messages(ctx, ch.ID, 100)
	for _, m := range msgs {
		row := transcriptRow{
			ID:            m.ID,
			Author:        m.Author,
			Body:          m.Body,
			Mentions:      m.Mentions,
			Recipients:    m.Mentions, // for v1, recipients == resolved mentions; #661 will refine
			CreatedAt:     m.CreatedAt,
			Kind:          "message",
			Team:          team,
			TeamGitHubURL: teamURL,
		}
		// #662 — operator-authored row with no explicit @-mentions was
		// auto-routed to the team lead at POST time. Mirror that in the
		// GET response so the UI renders "↪ routed to @<lead>".
		if (m.Author == "operator" || m.Author == "") && len(m.Mentions) == 0 && lead != "" && lead != "operator" {
			row.RoutedToDefault = true
			row.DefaultTarget = lead
			row.Recipients = []string{lead}
		}
		rows = append(rows, row)
	}

	// 2. A2A TaskStore rows (agent↔agent + human↔agent comms via
	//    chepherd.send_to_session / A2A message/send).
	if s.TaskStore != nil {
		tasks, err := s.TaskStore.List(ctx, persistence.TaskListOpts{Limit: 200})
		if err == nil {
			for _, t := range tasks {
				if len(t.InputBlob) == 0 {
					continue
				}
				// The persisted InputBlob is the A2A Message envelope at the
				// ROOT level (not nested under .message). Fields:
				// role/contextId/parts/kind. The chepherd-internal sender
				// @-handle is in chepherd_from (since 2026-06-02 — pre
				// that, From was json:"-" and the transcript showed every
				// agent as "user"; now correctly attributed).
				var msg struct {
					Role         string `json:"role"`
					ContextID    string `json:"contextId"`
					ChepherdFrom string `json:"chepherd_from"`
					Parts        []struct {
						Kind string `json:"kind"`
						Text string `json:"text"`
					} `json:"parts"`
				}
				if err := json.Unmarshal(t.InputBlob, &msg); err != nil {
					continue
				}
				var body string
				for _, p := range msg.Parts {
					if p.Kind == "text" {
						body = p.Text
						break
					}
				}
				if body == "" {
					continue
				}
				author := msg.ChepherdFrom
				if author == "" {
					author = msg.Role
				}
				if author == "" {
					author = "?"
				}
				rcpt := msg.ContextID
				if rcpt == "" {
					rcpt = t.RunnerSID
				}
				// Restrict to recipients (or authors) that belong to this
				// team so the per-team feed doesn't bleed cross-team comms
				// of agents who happen to span multiple teams.
				if !s.taskRowBelongsToTeam(team, author, rcpt) {
					continue
				}
				rows = append(rows, transcriptRow{
					ID:            "task:" + t.ID,
					Author:        author,
					Body:          body,
					Recipients:    []string{rcpt},
					CreatedAt:     t.CreatedAt,
					Kind:          "message",
					Team:          team,
					TeamGitHubURL: teamURL,
				})
			}
		}
	}

	// 3. HumanInbox rows from alert_human (#667) — surface escalations in the
	//    same pane with kind="alert:<kind>" so the UI can render the colored
	//    stripe. We tag rows by team if the author maps to a team member;
	//    untagged (e.g., from non-agent callers like "shepherd"/"runtime")
	//    only appear on a team if that team owns the author, otherwise they
	//    surface on every team's feed so the operator always sees them.
	if s.rt != nil {
		for _, e := range s.rt.Inbox() {
			body := e.Body
			kindTag := "alert"
			if m := alertKindPrefixRegex.FindStringSubmatch(body); m != nil {
				kindTag = "alert:" + m[1]
				body = body[len(m[0]):]
			}
			if !s.alertBelongsToTeam(team, e.From) {
				continue
			}
			rows = append(rows, transcriptRow{
				ID:            "alert:" + e.ID,
				Author:        e.From,
				Body:          body,
				Recipients:    []string{"operator"},
				CreatedAt:     e.At,
				Kind:          kindTag,
				Team:          team,
				TeamGitHubURL: teamURL,
			})
		}
	}

	// Sort newest-first by CreatedAt for consistent display.
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})
	return rows
}

// taskRowBelongsToTeam returns true when either the author or recipient of
// an A2A task row is a live member of the given team. Used to scope the
// per-team transcript feed so cross-team comms don't leak between feeds.
func (s *Server) taskRowBelongsToTeam(team, author, recipient string) bool {
	if s.rt == nil {
		return true // permissive when runtime is offline (tests)
	}
	for _, info := range s.rt.List() {
		if info == nil || info.Team != team {
			continue
		}
		if info.Name == author || info.Name == recipient {
			return true
		}
	}
	return false
}

// alertBelongsToTeam returns true when an alert_human entry should appear on
// the given team's feed. An alert belongs if its sender is a live member of
// the team. Alerts from non-agent callers ("runtime", "shepherd", "operator")
// are surfaced on EVERY team's feed so the operator never misses an
// escalation regardless of which team's view they're looking at.
func (s *Server) alertBelongsToTeam(team, from string) bool {
	if s.rt == nil {
		return true
	}
	for _, info := range s.rt.List() {
		if info == nil {
			continue
		}
		if info.Name == from {
			return info.Team == team
		}
	}
	// Unknown sender → broadcast to all teams.
	return true
}

func (s *Server) teamTranscriptGet(w http.ResponseWriter, r *http.Request, ch *persistence.Channel, team string) {
	rows := s.collectTranscriptRows(r.Context(), ch, team)
	teamURL := s.teamGitHubURL(team)
	lead := s.resolveTeamLead(team)

	// also list current members so the UI can show the team roster
	members, _ := s.ChannelStore.Members(r.Context(), ch.ID)
	memberNames := make([]string, 0, len(members))
	for _, m := range members {
		memberNames = append(memberNames, m.Member)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channel": map[string]any{
			"id": ch.ID, "name": ch.Name, "members": memberNames,
		},
		"messages":        rows,
		"team":            team,
		"team_github_url": teamURL,
		"default_target":  lead,
		"default_role":    s.resolveTeamLeadRole(team, lead),
	})
}

func (s *Server) teamTranscriptPost(w http.ResponseWriter, r *http.Request, ch *persistence.Channel, team string) {
	var req postMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "decode: " + err.Error()})
		return
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "body required"})
		return
	}
	author := strings.TrimSpace(req.Author)
	if author == "" {
		author = "operator"
	}

	// Parse @-mentions and resolve to concrete recipients.
	mentions := parseMentions(body)
	teamMembers := s.teamMembersOf(team)
	recipients := resolveRecipients(mentions, teamMembers, s.teamMembersOf)

	// #662 — when the operator posts without an explicit @-mention, route to
	// the team lead so the message actually reaches an agent. The mentions
	// list stays empty on the persisted ChannelMessage (auditable: "operator
	// didn't @-anyone"); the recipients fan-out below sees the synthesized
	// lead and delivers a knock.
	routedToDefault := false
	defaultTarget := ""
	if (author == "operator" || author == "") && len(mentions) == 0 {
		lead := s.resolveTeamLead(team)
		if lead != "" && lead != "operator" {
			recipients = []string{lead}
			routedToDefault = true
			defaultTarget = lead
		}
	}

	// Persist the message in the channel transcript first (single source of truth).
	msg := &persistence.ChannelMessage{
		ID:        uuid.NewString(),
		ChannelID: ch.ID,
		Author:    author,
		Body:      body,
		Mentions:  mentions,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.ChannelStore.SaveMessage(r.Context(), msg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save message: " + err.Error()})
		return
	}

	// Fan out to each named recipient via the A2A Deliverer — same path
	// chepherd.send_to_session uses. Each delivery becomes a knock
	// marker in the recipient's PTY so the agent wakes and calls
	// chepherd.get_task to fetch the body. The operator never gets a
	// knock (they see everything in the transcript UI).
	//
	// Operator-hit 2026-06-02: before this commit the loop only filtered
	// to "delivered" status but NEVER called Deliver(); agents got no
	// knock for operator messages.
	delivered := []string{}
	undelivered := []string{}
	for _, rcpt := range recipients {
		if rcpt == "operator" || rcpt == author {
			continue
		}
		if s.rt == nil {
			continue
		}
		if _, info := s.rt.Get(rcpt); info == nil {
			fmt.Fprintf(os.Stderr, "[transcript-post] %s → %s: skip — rt.Get returned nil\n", author, rcpt)
			undelivered = append(undelivered, rcpt)
			continue
		}
		if s.Deliverer == nil {
			fmt.Fprintf(os.Stderr, "[transcript-post] %s → %s: skip — s.Deliverer nil (persistence-only mode)\n", author, rcpt)
			undelivered = append(undelivered, rcpt)
			continue
		}
		if _, err := s.Deliverer.Deliver(r.Context(), a2a.Message{
			Role:      "user",
			Kind:      "message",
			ContextID: rcpt,
			Parts:     []a2a.Part{{Kind: "text", Text: body}},
			From:      author,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "[transcript-post] %s → %s: Deliver err: %v\n", author, rcpt, err)
			undelivered = append(undelivered, rcpt)
			continue
		}
		fmt.Fprintf(os.Stderr, "[transcript-post] %s → %s: delivered via Deliverer.Deliver\n", author, rcpt)
		delivered = append(delivered, rcpt)
	}

	resp := map[string]any{
		"id":          msg.ID,
		"author":      author,
		"body":        body,
		"mentions":    mentions,
		"recipients":  recipients,
		"delivered":   delivered,
		"undelivered": undelivered,
		"created_at":  msg.CreatedAt,
	}
	if routedToDefault {
		resp["routed_to_default"] = true
		resp["default_target"] = defaultTarget
	}
	writeJSON(w, http.StatusCreated, resp)
}

// transcriptMultiTeamHandler handles GET /api/v1/transcript?teams=… (#668).
// Returns the merged transcript across N teams in ONE round-trip so the
// frontend's "all" scope doesn't have to iterate per-team fetches.
//
// Query: ?teams=all  → every team the runtime knows about
//        ?teams=trio,scrum → just those two
//        (missing/empty teams param → all)
//
// Response shape (per DoD):
//   {
//     "teams": ["trio","scrum"],
//     "messages": [ {…, "team": "trio", …}, … ]
//   }
//
// Per-team scope (single-team queries) continues to use the existing
// /api/v1/teams/{name}/messages endpoint — this multi-team endpoint is
// strictly the merged-fetch path.
func (s *Server) transcriptMultiTeamHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if s.ChannelStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "channel store not configured"})
		return
	}

	requested := strings.TrimSpace(r.URL.Query().Get("teams"))
	var teams []string
	if requested == "" || requested == "all" {
		if s.rt != nil {
			for _, t := range s.rt.ListTeams() {
				if t != nil && t.Name != "" {
					teams = append(teams, t.Name)
				}
			}
		}
	} else {
		for _, part := range strings.Split(requested, ",") {
			if name := strings.TrimSpace(part); name != "" {
				teams = append(teams, name)
			}
		}
	}
	sort.Strings(teams)

	ctx := r.Context()
	all := make([]transcriptRow, 0, 256)
	for _, team := range teams {
		ch, err := s.ensureTeamChannel(ctx, team)
		if err != nil || ch == nil {
			continue
		}
		all = append(all, s.collectTranscriptRows(ctx, ch, team)...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"teams":    teams,
		"messages": all,
	})
}

// ticketMentionsHandler returns {"<num>": <count>} for every #-ticket
// mentioned in the team's current transcript (#665 BE). The kanban widget
// polls this every 30s to render the "💬 N" badge on each card.
//
// Route: GET /api/v1/teams/{name}/ticket-mentions
func (s *Server) ticketMentionsHandler(w http.ResponseWriter, r *http.Request, team string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if s.ChannelStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "channel store not configured"})
		return
	}
	if team == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "team required"})
		return
	}
	ctx := r.Context()
	ch, err := s.ensureTeamChannel(ctx, team)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "ensure channel: " + err.Error()})
		return
	}
	rows := s.collectTranscriptRows(ctx, ch, team)
	counts := map[string]int{}
	for _, row := range rows {
		// One row can mention the same ticket multiple times — count
		// distinct tickets per row (matches operator intuition: "how many
		// transcript messages reference #N").
		for _, num := range parseTickets(row.Body) {
			counts["#"+num]++
		}
	}
	writeJSON(w, http.StatusOK, counts)
}

// runtime import sanity (compile-time check that the import is used)
var _ = runtime.SessionInfo{}
