// Package events defines the contracts services exchange over the bus.
// Payloads are versioned JSON; add fields, never repurpose them.
package events

import "time"

// Topics.
const (
	TopicOrderCreated = "orders.created"
	TopicUserSignedUp = "users.signed_up"
)

// Header keys carried on every message.
const (
	HeaderEventType = "event_type"
	HeaderVersion   = "event_version"
	HeaderRequestID = "request_id"
)

// OrderCreated is published by the orders service.
type OrderCreated struct {
	OrderID    string    `json:"order_id"`
	UserID     string    `json:"user_id"`
	SKU        string    `json:"sku"`
	Quantity   int       `json:"quantity"`
	TotalCents int64     `json:"total_cents"`
	CreatedAt  time.Time `json:"created_at"`
}

// UserSignedUp is published by the auth service.
type UserSignedUp struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// Envelope is what consumers receive.
type Envelope struct {
	ID      string // unique per message; use it to deduplicate
	Topic   string
	Headers map[string]string
	Data    []byte
}
