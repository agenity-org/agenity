package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/persistence/migrate"
)

// Store implements persistence.Store backed by a single *sql.DB
// connected to PostgreSQL via jackc/pgx/v5/stdlib. Mirror of the
// SQLite Store; the equivalence test framework gates behavioral
// drift between the two backends.
//
// Refs #208.
type Store struct {
	db *sql.DB

	sessions    *SessionRepository
	skills      *SkillRepository
	agents      *AgentRepository
	canon       *CanonRepository
	keychain    *KeychainRepository
	templates   *TemplateRepository
	authSecrets *AuthSecretRepository
	events      *EventRepository
	grants      *RBACGrantRepository
	tasks       *TaskRepository
	artifacts   *ArtifactRepository
	pushConfigs *PushNotificationConfigRepository
	agentCards  *AgentCardRepository
	accounts    *AccountRepository
}

// NewStore opens a PostgreSQL DSN, runs migrations, and returns a
// ready Store.
func NewStore(ctx context.Context, dsn string) (*Store, error) {
	db, err := Open(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres.NewStore: %w", err)
	}
	if err := migrate.Run(ctx, db, "postgres"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres.NewStore migrate: %w", err)
	}
	return NewStoreFromDB(db), nil
}

// NewStoreFromDB wraps an already-open *sql.DB (e.g., one with
// migrations already applied — useful for testing).
func NewStoreFromDB(db *sql.DB) *Store {
	return &Store{
		db:          db,
		sessions:    NewSessionRepository(db),
		skills:      NewSkillRepository(db),
		agents:      NewAgentRepository(db),
		canon:       NewCanonRepository(db),
		keychain:    NewKeychainRepository(db),
		templates:   NewTemplateRepository(db),
		authSecrets: NewAuthSecretRepository(db),
		events:      NewEventRepository(db),
		grants:      NewRBACGrantRepository(db),
		tasks:       NewTaskRepository(db),
		artifacts:   NewArtifactRepository(db),
		pushConfigs: NewPushNotificationConfigRepository(db),
		agentCards:  NewAgentCardRepository(db),
		accounts:    NewAccountRepository(db),
	}
}

func (s *Store) Sessions() persistence.SessionRepository       { return s.sessions }
func (s *Store) Skills() persistence.SkillRepository           { return s.skills }
func (s *Store) Agents() persistence.AgentRepository           { return s.agents }
func (s *Store) Canon() persistence.CanonRepository            { return s.canon }
func (s *Store) Keychain() persistence.KeychainRepository      { return s.keychain }
func (s *Store) Templates() persistence.TemplateRepository     { return s.templates }
func (s *Store) AuthSecrets() persistence.AuthSecretRepository { return s.authSecrets }
func (s *Store) Events() persistence.EventRepository           { return s.events }
func (s *Store) Grants() persistence.RBACGrantRepository       { return s.grants }
func (s *Store) Tasks() persistence.TaskRepository         { return s.tasks }
func (s *Store) Artifacts() persistence.ArtifactRepository { return s.artifacts }
func (s *Store) PushConfigs() persistence.PushNotificationConfigRepository {
	return s.pushConfigs
}
func (s *Store) AgentCards() persistence.AgentCardRepository { return s.agentCards }
func (s *Store) Accounts() persistence.AccountRepository     { return s.accounts }

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

var _ persistence.Store = (*Store)(nil)
