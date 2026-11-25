// Package notifications consumes domain events and notifies users. Delivery
// from the bus is at-least-once, so every message is deduplicated by ID
// before any side effect happens.
package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/shani8615/go-microservices-kit/internal/events"
)

// Dedup records processed message IDs.
type Dedup interface {
	// Claim returns true the first time it sees id.
	Claim(ctx context.Context, id string) (bool, error)
	// Release forgets id so a failed message can be retried.
	Release(ctx context.Context, id string) error
}

// Notifier delivers a message to a user (email, push, Slack, ...).
type Notifier interface {
	Notify(ctx context.Context, userID, subject, body string) error
}

// Handler processes envelopes from the bus.
type Handler struct {
	Dedup    Dedup
	Notifier Notifier
	Log      *slog.Logger
}

// Handle is safe to call more than once with the same message.
func (h *Handler) Handle(ctx context.Context, env events.Envelope) error {
	if env.ID == "" {
		return fmt.Errorf("message on %s has no id", env.Topic)
	}
	first, err := h.Dedup.Claim(ctx, env.ID)
	if err != nil {
		return err
	}
	if !first {
		h.Log.DebugContext(ctx, "duplicate message skipped", slog.String("id", env.ID))
		return nil
	}
	if err := h.dispatch(ctx, env); err != nil {
		_ = h.Dedup.Release(ctx, env.ID)
		return err
	}
	return nil
}

func (h *Handler) dispatch(ctx context.Context, env events.Envelope) error {
	switch env.Topic {
	case events.TopicOrderCreated:
		var e events.OrderCreated
		if err := json.Unmarshal(env.Data, &e); err != nil {
			return h.malformed(ctx, env, err)
		}
		return h.Notifier.Notify(ctx, e.UserID, "Order confirmed",
			fmt.Sprintf("Your order %s for %d x %s (%.2f) is confirmed.", e.OrderID, e.Quantity, e.SKU, float64(e.TotalCents)/100))
	case events.TopicUserSignedUp:
		var e events.UserSignedUp
		if err := json.Unmarshal(env.Data, &e); err != nil {
			return h.malformed(ctx, env, err)
		}
		return h.Notifier.Notify(ctx, e.UserID, "Welcome!", "Thanks for signing up.")
	default:
		return nil
	}
}

// malformed logs and drops a payload that can never be processed; retrying
// it would only block the consumer.
func (h *Handler) malformed(ctx context.Context, env events.Envelope, err error) error {
	h.Log.WarnContext(ctx, "dropping malformed message",
		slog.String("id", env.ID), slog.String("topic", env.Topic), slog.String("error", err.Error()))
	return nil
}

// LogNotifier "sends" notifications by logging them. It logs the user ID and
// subject only, never message bodies or email addresses.
type LogNotifier struct{ Log *slog.Logger }

func (n LogNotifier) Notify(ctx context.Context, userID, subject, _ string) error {
	n.Log.InfoContext(ctx, "notification sent", slog.String("user_id", userID), slog.String("subject", subject))
	return nil
}

// Schema is applied at startup.
const Schema = `CREATE TABLE IF NOT EXISTS processed_messages (
    id           TEXT PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`

// PGDedup stores processed IDs in Postgres.
type PGDedup struct{ DB *sql.DB }

func (d PGDedup) Claim(ctx context.Context, id string) (bool, error) {
	res, err := d.DB.ExecContext(ctx, `INSERT INTO processed_messages (id) VALUES ($1) ON CONFLICT DO NOTHING`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (d PGDedup) Release(ctx context.Context, id string) error {
	_, err := d.DB.ExecContext(ctx, `DELETE FROM processed_messages WHERE id = $1`, id)
	return err
}

// MemDedup is an in-memory Dedup for tests.
type MemDedup struct {
	mu   sync.Mutex
	seen map[string]bool
}

func NewMemDedup() *MemDedup { return &MemDedup{seen: map[string]bool{}} }

func (d *MemDedup) Claim(_ context.Context, id string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen[id] {
		return false, nil
	}
	d.seen[id] = true
	return true, nil
}

func (d *MemDedup) Release(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.seen, id)
	return nil
}
