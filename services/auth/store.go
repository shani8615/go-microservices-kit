package auth

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/shani8615/go-microservices-kit/internal/platform"
	"github.com/shani8615/go-outbox"
)

// Schema is applied at startup (idempotent).
const Schema = `CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY,
    email         TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);`

// PGStore is the Postgres Store.
type PGStore struct {
	DB     *sql.DB
	Outbox *outbox.Postgres
}

func (s *PGStore) Create(ctx context.Context, u User, evt outbox.Message) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO users (id, email, password_hash, created_at) VALUES ($1, $2, $3, $4)`,
		u.ID, u.Email, u.PasswordHash, u.CreatedAt)
	if platform.IsUniqueViolation(err) {
		return ErrEmailTaken
	}
	if err != nil {
		return err
	}
	if err := s.Outbox.Enqueue(ctx, tx, evt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PGStore) ByEmail(ctx context.Context, email string) (User, error) {
	return s.one(ctx, `SELECT id, email, password_hash, created_at FROM users WHERE email = $1`, email)
}

func (s *PGStore) ByID(ctx context.Context, id string) (User, error) {
	return s.one(ctx, `SELECT id, email, password_hash, created_at FROM users WHERE id = $1`, id)
}

func (s *PGStore) one(ctx context.Context, q string, arg any) (User, error) {
	var u User
	err := s.DB.QueryRowContext(ctx, q, arg).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// MemStore is an in-memory Store for tests and local runs.
type MemStore struct {
	mu     sync.Mutex
	users  map[string]User
	Outbox *outbox.Memory
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{users: map[string]User{}, Outbox: outbox.NewMemory()}
}

func (s *MemStore) Create(_ context.Context, u User, evt outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.users {
		if x.Email == u.Email {
			return ErrEmailTaken
		}
	}
	s.users[u.ID] = u
	return s.Outbox.Add(evt)
}

func (s *MemStore) ByEmail(_ context.Context, email string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.Email == email {
			return u, nil
		}
	}
	return User{}, ErrNotFound
}

func (s *MemStore) ByID(_ context.Context, id string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}
