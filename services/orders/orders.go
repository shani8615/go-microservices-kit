// Package orders is the order service. Creating an order writes the order and
// an orders.created event in one transaction (transactional outbox), and POST
// is idempotent per user via the Idempotency-Key header.
package orders

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/shani8615/go-httpkit/idempotency"
	"github.com/shani8615/go-httpkit/pagination"
	"github.com/shani8615/go-httpkit/respond"
	"github.com/shani8615/go-microservices-kit/internal/events"
	"github.com/shani8615/go-microservices-kit/internal/platform"
	"github.com/shani8615/go-microservices-kit/internal/token"
	"github.com/shani8615/go-outbox"
)

// Order is a placed order. Prices come from the server-side catalog, never
// from the client.
type Order struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	SKU        string    `json:"sku"`
	Quantity   int       `json:"quantity"`
	UnitCents  int64     `json:"unit_price_cents"`
	TotalCents int64     `json:"total_cents"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

// Cursor is the keyset position for listing (newest first).
type Cursor struct {
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"id"`
}

// ErrNotFound is returned for missing orders and orders owned by someone else.
var ErrNotFound = errors.New("orders: not found")

// Store persists orders. Create must write the order and event atomically.
// Every read is scoped to userID.
type Store interface {
	Create(ctx context.Context, o Order, evt outbox.Message) error
	Get(ctx context.Context, userID, id string) (Order, error)
	List(ctx context.Context, userID string, after *Cursor, limit int) ([]Order, error)
}

// Catalog maps SKU to unit price in cents.
type Catalog map[string]int64

// DefaultCatalog is demo data.
var DefaultCatalog = Catalog{"BOOK-GO": 3999, "MUG-GOPHER": 1499, "STICKER-PACK": 499}

// Service wires the HTTP API.
type Service struct {
	Store       Store
	Catalog     Catalog
	Tokens      token.Verifier
	Idempotency idempotency.Store
	Log         *slog.Logger
}

// Routes registers endpoints. All routes require a valid token.
func (s *Service) Routes(mux *http.ServeMux) {
	auth := token.RequireAuth(s.Tokens)
	idem := idempotency.Middleware(idempotency.Options{
		Store: s.Idempotency,
		Scope: func(r *http.Request) string { c, _ := token.FromContext(r.Context()); return c.Subject },
	})
	mux.Handle("POST /v1/orders", auth(idem(http.HandlerFunc(s.create))))
	mux.Handle("GET /v1/orders", auth(http.HandlerFunc(s.list)))
	mux.Handle("GET /v1/orders/{id}", auth(http.HandlerFunc(s.get)))
}

type createReq struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	c, _ := token.FromContext(r.Context())
	var in createReq
	if err := respond.Decode(w, r, &in, 4<<10); err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	price, ok := s.Catalog[in.SKU]
	if !ok {
		respond.Error(w, http.StatusUnprocessableEntity, "unknown sku")
		return
	}
	if in.Quantity < 1 || in.Quantity > 100 {
		respond.Error(w, http.StatusUnprocessableEntity, "quantity must be 1-100")
		return
	}
	o := Order{
		ID: platform.NewID(), UserID: c.Subject, SKU: in.SKU, Quantity: in.Quantity,
		UnitCents: price, TotalCents: price * int64(in.Quantity), Status: "placed",
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond), // Postgres precision
	}
	payload, _ := json.Marshal(events.OrderCreated{
		OrderID: o.ID, UserID: o.UserID, SKU: o.SKU, Quantity: o.Quantity, TotalCents: o.TotalCents, CreatedAt: o.CreatedAt,
	})
	evt := outbox.Message{Topic: events.TopicOrderCreated, Key: o.ID, Payload: payload, Headers: map[string]string{
		events.HeaderEventType: "OrderCreated", events.HeaderVersion: "1",
		events.HeaderRequestID: r.Header.Get("X-Request-ID"),
	}}
	if err := s.Store.Create(r.Context(), o, evt); err != nil {
		s.fail(w, r, "create order", err)
		return
	}
	w.Header().Set("Location", "/v1/orders/"+o.ID)
	respond.JSON(w, http.StatusCreated, o)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	c, _ := token.FromContext(r.Context())
	o, err := s.Store.Get(r.Context(), c.Subject, r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		respond.Error(w, http.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		s.fail(w, r, "get order", err)
		return
	}
	respond.JSON(w, http.StatusOK, o)
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	c, _ := token.FromContext(r.Context())
	p, err := pagination.Parse(r)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid limit or cursor")
		return
	}
	var after *Cursor
	if p.Cursor != "" {
		cur, err := pagination.Decode[Cursor](p.Cursor)
		if err != nil {
			respond.Error(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		after = &cur
	}
	items, err := s.Store.List(r.Context(), c.Subject, after, p.Limit+1)
	if err != nil {
		s.fail(w, r, "list orders", err)
		return
	}
	res, err := pagination.Build(items, p.Limit, func(o Order) Cursor { return Cursor{o.CreatedAt, o.ID} })
	if err != nil {
		s.fail(w, r, "build page", err)
		return
	}
	respond.JSON(w, http.StatusOK, res)
}

func (s *Service) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	s.Log.ErrorContext(r.Context(), op, slog.String("error", err.Error()))
	respond.Error(w, http.StatusInternalServerError, "internal error")
}
