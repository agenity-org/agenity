// internal/persistence/sqlite/channels_test.go — #655 epic #654.
// Smoke-test the channel data model: migration runs, CRUD works for
// channels + members + messages, JSON mentions round-trip.
package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

func TestChannelRepository_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := NewStore(ctx, filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	r := store.Channels()

	// Save + Get a channel.
	now := time.Now().UTC().Truncate(time.Second)
	ch := &persistence.Channel{
		ID:         "0192c000-0000-7000-8000-000000000001",
		Name:       "#team-default",
		Kind:       "team",
		CreatedBy:  "operator",
		CreatedAt:  now,
		Visibility: "irc",
	}
	if err := r.Save(ctx, ch); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, ch.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "#team-default" || got.Visibility != "irc" {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// GetByName lookup.
	byName, err := r.GetByName(ctx, "#team-default")
	if err != nil || byName.ID != ch.ID {
		t.Errorf("GetByName: got=%v err=%v", byName, err)
	}

	// Members add + list.
	for _, name := range []string{"operator", "tech-lead", "full-stack"} {
		if err := r.AddMember(ctx, &persistence.ChannelMember{
			ChannelID: ch.ID, Member: name, JoinedAt: now,
		}); err != nil {
			t.Fatalf("AddMember %s: %v", name, err)
		}
	}
	members, err := r.Members(ctx, ch.ID)
	if err != nil || len(members) != 3 {
		t.Errorf("Members: got %d, want 3 (err=%v)", len(members), err)
	}

	// Add same member twice → idempotent (no error).
	if err := r.AddMember(ctx, &persistence.ChannelMember{
		ChannelID: ch.ID, Member: "operator", JoinedAt: now,
	}); err != nil {
		t.Errorf("AddMember idempotent: %v", err)
	}

	// Remove member.
	if err := r.RemoveMember(ctx, ch.ID, "tech-lead"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	members, _ = r.Members(ctx, ch.ID)
	if len(members) != 2 {
		t.Errorf("after remove: got %d, want 2", len(members))
	}

	// Messages — save + list, mentions round-trip.
	msg := &persistence.ChannelMessage{
		ID:        "0192c001-0000-7000-8000-000000000001",
		ChannelID: ch.ID,
		Author:    "operator",
		Body:      "hey @full-stack, can you check this?",
		Mentions:  []string{"full-stack"},
		CreatedAt: now,
	}
	if err := r.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	msgs, err := r.Messages(ctx, ch.ID, 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Messages: got %d, want 1 (err=%v)", len(msgs), err)
	}
	if msgs[0].Body != msg.Body || len(msgs[0].Mentions) != 1 || msgs[0].Mentions[0] != "full-stack" {
		t.Errorf("Messages round-trip mismatch: %+v", msgs[0])
	}

	// Delete channel cascades members + messages.
	if err := r.Delete(ctx, ch.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if members, _ := r.Members(ctx, ch.ID); len(members) != 0 {
		t.Errorf("after channel delete: members not cascaded, got %d", len(members))
	}
	if msgs, _ := r.Messages(ctx, ch.ID, 10); len(msgs) != 0 {
		t.Errorf("after channel delete: messages not cascaded, got %d", len(msgs))
	}
}
