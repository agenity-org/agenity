// Package postgres — ChannelRepository implementation (#655 epic #654).
// Mirror of sqlite/channels.go using pgx-compatible $N placeholders.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/agenity-org/agenity/internal/persistence"
)

type ChannelRepository struct {
	db *sql.DB
}

func NewChannelRepository(db *sql.DB) *ChannelRepository {
	return &ChannelRepository{db: db}
}

func (r *ChannelRepository) Get(ctx context.Context, id string) (*persistence.Channel, error) {
	if id == "" {
		return nil, errors.New("postgres ChannelRepository: empty id")
	}
	var c persistence.Channel
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, kind, created_by, created_at, visibility FROM channels WHERE id = $1`,
		id,
	).Scan(&c.ID, &c.Name, &c.Kind, &c.CreatedBy, &c.CreatedAt, &c.Visibility)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("postgres channels Get %q: not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres channels Get %q: %w", id, err)
	}
	return &c, nil
}

func (r *ChannelRepository) GetByName(ctx context.Context, name string) (*persistence.Channel, error) {
	if name == "" {
		return nil, errors.New("postgres ChannelRepository: empty name")
	}
	var c persistence.Channel
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, kind, created_by, created_at, visibility FROM channels WHERE name = $1`,
		name,
	).Scan(&c.ID, &c.Name, &c.Kind, &c.CreatedBy, &c.CreatedAt, &c.Visibility)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("postgres channels GetByName %q: not found", name)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres channels GetByName %q: %w", name, err)
	}
	return &c, nil
}

func (r *ChannelRepository) List(ctx context.Context) ([]*persistence.Channel, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, kind, created_by, created_at, visibility FROM channels ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("postgres channels List: %w", err)
	}
	defer rows.Close()
	var out []*persistence.Channel
	for rows.Next() {
		var c persistence.Channel
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.CreatedBy, &c.CreatedAt, &c.Visibility); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (r *ChannelRepository) Save(ctx context.Context, ch *persistence.Channel) error {
	if ch == nil || ch.ID == "" || ch.Name == "" {
		return errors.New("postgres ChannelRepository.Save: id and name required")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO channels (id, name, kind, created_by, created_at, visibility)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT(id) DO UPDATE SET
		   name=EXCLUDED.name, kind=EXCLUDED.kind, created_by=EXCLUDED.created_by,
		   created_at=EXCLUDED.created_at, visibility=EXCLUDED.visibility`,
		ch.ID, ch.Name, ch.Kind, ch.CreatedBy, ch.CreatedAt, ch.Visibility,
	)
	if err != nil {
		return fmt.Errorf("postgres channels Save: %w", err)
	}
	return nil
}

func (r *ChannelRepository) Delete(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM channels WHERE id = $1`, id); err != nil {
		return fmt.Errorf("postgres channels Delete %q: %w", id, err)
	}
	return nil
}

func (r *ChannelRepository) Members(ctx context.Context, channelID string) ([]*persistence.ChannelMember, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT channel_id, member, joined_at FROM channel_members WHERE channel_id = $1 ORDER BY joined_at`,
		channelID)
	if err != nil {
		return nil, fmt.Errorf("postgres channel_members list: %w", err)
	}
	defer rows.Close()
	var out []*persistence.ChannelMember
	for rows.Next() {
		var m persistence.ChannelMember
		if err := rows.Scan(&m.ChannelID, &m.Member, &m.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (r *ChannelRepository) AddMember(ctx context.Context, m *persistence.ChannelMember) error {
	if m == nil || m.ChannelID == "" || m.Member == "" {
		return errors.New("postgres ChannelRepository.AddMember: channel_id and member required")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO channel_members (channel_id, member, joined_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT(channel_id, member) DO NOTHING`,
		m.ChannelID, m.Member, m.JoinedAt)
	return err
}

func (r *ChannelRepository) RemoveMember(ctx context.Context, channelID, member string) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM channel_members WHERE channel_id = $1 AND member = $2`, channelID, member); err != nil {
		return fmt.Errorf("postgres channel_members RemoveMember: %w", err)
	}
	return nil
}

func (r *ChannelRepository) Messages(ctx context.Context, channelID string, limit int) ([]*persistence.ChannelMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, channel_id, author, body, mentions, task_id, created_at
		 FROM channel_messages WHERE channel_id = $1 ORDER BY created_at DESC LIMIT $2`,
		channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres channel_messages list: %w", err)
	}
	defer rows.Close()
	var out []*persistence.ChannelMessage
	for rows.Next() {
		var m persistence.ChannelMessage
		var mentionsJSON string
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.Author, &m.Body, &mentionsJSON, &m.TaskID, &m.CreatedAt); err != nil {
			return nil, err
		}
		if mentionsJSON != "" && mentionsJSON != "[]" {
			if err := json.Unmarshal([]byte(mentionsJSON), &m.Mentions); err != nil {
				return nil, fmt.Errorf("postgres channel_messages: unmarshal mentions for %s: %w", m.ID, err)
			}
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (r *ChannelRepository) SaveMessage(ctx context.Context, msg *persistence.ChannelMessage) error {
	if msg == nil || msg.ID == "" || msg.ChannelID == "" || msg.Author == "" {
		return errors.New("postgres ChannelRepository.SaveMessage: id, channel_id, author required")
	}
	mentionsJSON := "[]"
	if len(msg.Mentions) > 0 {
		b, err := json.Marshal(msg.Mentions)
		if err != nil {
			return fmt.Errorf("postgres channel_messages: marshal mentions: %w", err)
		}
		mentionsJSON = string(b)
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO channel_messages (id, channel_id, author, body, mentions, task_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		msg.ID, msg.ChannelID, msg.Author, msg.Body, mentionsJSON, msg.TaskID, msg.CreatedAt)
	if err != nil {
		return fmt.Errorf("postgres channel_messages save: %w", err)
	}
	return nil
}
