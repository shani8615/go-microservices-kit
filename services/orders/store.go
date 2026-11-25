package orders

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"

	"github.com/shani8615/go-outbox"
)

// Schema is applied at startup (idempotent).
const Schema = `CREATE TABLE IF NOT EXISTS orders (
    id          UUID PRIMARY KEY,
    user_id     UUID        NOT NULL,
    sku         TEXT        NOT NULL,
    quantity    INT         NOT NULL CHECK (quantity > 0),
    unit_cents  BIGINT      NOT NULL,
    total_cents BIGINT      NOT NULL,
    status      TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS orders_user_created_idx ON orders (user_id, created_at DESC, id DESC);`

// PGStore is the Postgres Store.
type PGStore struct {
	DB     *sql.DB
	Outbox *outbox.Postgres
}

const cols = `id, user_id, sku, quantity, unit_cents, total_cents, status, created_at`

func (s *PGStore) Create(ctx context.Context, o Order, evt outbox.Message) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO orders (`+cols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		o.ID, o.UserID, o.SKU, o.Quantity, o.UnitCents, o.TotalCents, o.Status, o.CreatedAt); err != nil {
		return err
	}
	if err := s.Outbox.Enqueue(ctx, tx, evt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PGStore) Get(ctx context.Context, userID, id string) (Order, error) {
	var o Order
	err := s.DB.QueryRowContext(ctx, `SELECT `+cols+` FROM orders WHERE id = $1 AND user_id = $2`, id, userID).
		Scan(&o.ID, &o.UserID, &o.SKU, &o.Quantity, &o.UnitCents, &o.TotalCents, &o.Status, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	if err != nil && isInvalidUUID(err) {
		return o, ErrNotFound
	}
	return o, err
}

func (s *PGStore) List(ctx context.Context, userID string, after *Cursor, limit int) ([]Order, error) {
	q := `SELECT ` + cols + ` FROM orders WHERE user_id = $1`
	args := []any{userID, limit}
	if after != nil {
		q += ` AND (created_at, id) < ($3, $4)`
		args = append(args, after.CreatedAt, after.ID)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT $2`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Order
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.SKU, &o.Quantity, &o.UnitCents, &o.TotalCents, &o.Status, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func isInvalidUUID(err error) bool {
	var s interface{ SQLState() string }
	return errors.As(err, &s) && s.SQLState() == "22P02" // invalid_text_representation
}

// MemStore is an in-memory Store for tests and local runs.
type MemStore struct {
	mu     sync.Mutex
	orders []Order
	Outbox *outbox.Memory
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore { return &MemStore{Outbox: outbox.NewMemory()} }

func (s *MemStore) Create(_ context.Context, o Order, evt outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders = append(s.orders, o)
	return s.Outbox.Add(evt)
}

func (s *MemStore) Get(_ context.Context, userID, id string) (Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.orders {
		if o.ID == id && o.UserID == userID {
			return o, nil
		}
	}
	return Order{}, ErrNotFound
}

func (s *MemStore) List(_ context.Context, userID string, after *Cursor, limit int) ([]Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var mine []Order
	for _, o := range s.orders {
		if o.UserID == userID {
			mine = append(mine, o)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return less(mine[j], mine[i]) }) // newest first
	var out []Order
	for _, o := range mine {
		if after != nil && !less(o, Order{CreatedAt: after.CreatedAt, ID: after.ID}) {
			continue
		}
		out = append(out, o)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func less(a, b Order) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}
