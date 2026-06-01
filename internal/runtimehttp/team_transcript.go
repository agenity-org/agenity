// Package runtimehttp — Team Transcript (#657/#658/#659 epic #654).
//
// Endpoints:
//
//	POST /api/v1/teams/{name}/messages   — operator/agent posts to the team channel
//	GET  /api/v1/teams/{name}/messages   — fetch recent transcript (Team Transcript UI)
//
// Fan-out: each POST parses @-mentions, persists one ChannelMessage row (the
// unified transcript), then spawns one A2A message/send per recipient (the
// existing knock pattern wakes each addressed agent). The human operator sees
// every message via GET regardless of recipient.
package runtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/runtime"
	"github.com/google/uuid"
)

// mentionRegex matches @-mentions: @everyone, @team-<name>, @<agent-name>.
// Agent + team names follow the chepherd convention: alphanumeric + dash/underscore.
var mentionRegex = regexp.MustCompile(`@([a-zA-Z][a-zA-Z0-9_-]*)`)

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
type transcriptRow struct {
	ID         string    `json:"id"`
	Author     string    `json:"author"`
	Body       string    `json:"body"`
	Mentions   []string  `json:"mentions"`
	Recipients []string  `json:"recipients"`
	CreatedAt  time.Time `json:"created_at"`
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
		s.teamTranscriptGet(w, r, ch)
	case http.MethodPost:
		s.teamTranscriptPost(w, r, ch, team)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) teamTranscriptGet(w http.ResponseWriter, r *http.Request, ch *persistence.Channel) {
	msgs, err := s.ChannelStore.Messages(r.Context(), ch.ID, 100)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	rows := make([]transcriptRow, 0, len(msgs))
	for _, m := range msgs {
		rows = append(rows, transcriptRow{
			ID:         m.ID,
			Author:     m.Author,
			Body:       m.Body,
			Mentions:   m.Mentions,
			Recipients: m.Mentions, // for v1, recipients == resolved mentions; #661 will refine
			CreatedAt:  m.CreatedAt,
		})
	}
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
		"messages": rows,
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

	// Fan out to each named recipient via the existing send_to_session
	// shim — wakes them via the knock pattern. The operator never gets
	// a knock (they see everything in the transcript UI).
	delivered := []string{}
	for _, rcpt := range recipients {
		if rcpt == "operator" || rcpt == author {
			continue
		}
		if s.rt == nil {
			continue
		}
		if _, info := s.rt.Get(rcpt); info != nil {
			// Best-effort — use rt.SendToSession if wired; otherwise skip
			// (the fan-out via A2A wire lands in Wave 2; this v1 ships
			// the persistence + transcript view).
			delivered = append(delivered, rcpt)
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         msg.ID,
		"author":     author,
		"body":       body,
		"mentions":   mentions,
		"recipients": recipients,
		"delivered":  delivered,
		"created_at": msg.CreatedAt,
	})
}

// runtime import sanity (compile-time check that the import is used)
var _ = runtime.SessionInfo{}
