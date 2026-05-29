package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// PushNotificationConfigRepository implements
// persistence.PushNotificationConfigRepository against SQLite.
// NEW in v0.9.2. Persists webhook configs registered via the 4 A2A
// push-notification-config methods.
//
// Refs #208.
type PushNotificationConfigRepository struct {
	db *sql.DB
}

func NewPushNotificationConfigRepository(db *sql.DB) *PushNotificationConfigRepository {
	return &PushNotificationConfigRepository{db: db}
}

func (r *PushNotificationConfigRepository) Get(ctx context.Context, id string) (*persistence.PushNotificationConfig, error) {
	if id == "" {
		return nil, errors.New("sqlite PushNotificationConfigRepository: empty id")
	}
	var (
		c           persistence.PushNotificationConfig
		filtersJSON string
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT id, task_id, url, signing_key, filters_json, created_at
		 FROM push_notification_configs WHERE id = $1`,
		id,
	).Scan(&c.ID, &c.TaskID, &c.URL, &c.SigningKey, &filtersJSON, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sqlite push_notification_configs Get %q: not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite push_notification_configs Get %q: %w", id, err)
	}
	if filtersJSON != "" {
		if err := json.Unmarshal([]byte(filtersJSON), &c.Filters); err != nil {
			return nil, fmt.Errorf("unmarshal filters: %w", err)
		}
	}
	return &c, nil
}

func (r *PushNotificationConfigRepository) List(ctx context.Context, taskID string) ([]*persistence.PushNotificationConfig, error) {
	var rows *sql.Rows
	var err error
	if taskID == "" {
		rows, err = r.db.QueryContext(ctx,
			`SELECT id FROM push_notification_configs ORDER BY created_at`,
		)
	} else {
		rows, err = r.db.QueryContext(ctx,
			`SELECT id FROM push_notification_configs WHERE task_id = $1 ORDER BY created_at`,
			taskID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite push_notification_configs List: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*persistence.PushNotificationConfig, 0, len(ids))
	for _, id := range ids {
		c, err := r.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *PushNotificationConfigRepository) Save(ctx context.Context, c *persistence.PushNotificationConfig) error {
	if c == nil {
		return errors.New("sqlite PushNotificationConfigRepository: nil config")
	}
	if c.ID == "" {
		return errors.New("sqlite PushNotificationConfigRepository: empty ID")
	}
	if c.TaskID == "" {
		return errors.New("sqlite PushNotificationConfigRepository: empty TaskID")
	}
	if c.URL == "" {
		return errors.New("sqlite PushNotificationConfigRepository: empty URL")
	}
	if c.Filters == nil {
		c.Filters = []string{}
	}
	filters, err := json.Marshal(c.Filters)
	if err != nil {
		return fmt.Errorf("marshal filters: %w", err)
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO push_notification_configs
		   (id, task_id, url, signing_key, filters_json, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT(id) DO UPDATE SET
		     task_id = excluded.task_id,
		     url = excluded.url,
		     signing_key = excluded.signing_key,
		     filters_json = excluded.filters_json`,
		c.ID, c.TaskID, c.URL, c.SigningKey, string(filters), c.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("sqlite push_notification_configs Save %q: %w", c.ID, err)
	}
	return nil
}

func (r *PushNotificationConfigRepository) Delete(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("sqlite PushNotificationConfigRepository: empty id")
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM push_notification_configs WHERE id = $1`, id,
	)
	if err != nil {
		return fmt.Errorf("sqlite push_notification_configs Delete %q: %w", id, err)
	}
	return nil
}

var _ persistence.PushNotificationConfigRepository = (*PushNotificationConfigRepository)(nil)
