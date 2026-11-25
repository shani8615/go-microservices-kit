package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/shani8615/go-microservices-kit/internal/events"
)

type fakeNotifier struct {
	sent []string
	fail int
}

func (f *fakeNotifier) Notify(_ context.Context, userID, subject, _ string) error {
	if f.fail > 0 {
		f.fail--
		return errors.New("smtp down")
	}
	f.sent = append(f.sent, userID+":"+subject)
	return nil
}

func orderEnv(id string) events.Envelope {
	b, _ := json.Marshal(events.OrderCreated{OrderID: "o1", UserID: "u1", SKU: "BOOK-GO", Quantity: 1, TotalCents: 3999})
	return events.Envelope{ID: id, Topic: events.TopicOrderCreated, Data: b}
}

func newHandler(n Notifier) *Handler {
	return &Handler{Dedup: NewMemDedup(), Notifier: n, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestDuplicateDeliveryNotifiesOnce(t *testing.T) {
	n := &fakeNotifier{}
	h := newHandler(n)
	for i := 0; i < 3; i++ {
		if err := h.Handle(context.Background(), orderEnv("orders-1")); err != nil {
			t.Fatal(err)
		}
	}
	if len(n.sent) != 1 || n.sent[0] != "u1:Order confirmed" {
		t.Fatalf("sent=%v", n.sent)
	}
}

func TestFailedNotificationIsRetried(t *testing.T) {
	n := &fakeNotifier{fail: 1}
	h := newHandler(n)
	if err := h.Handle(context.Background(), orderEnv("orders-2")); err == nil {
		t.Fatal("first attempt should fail")
	}
	if err := h.Handle(context.Background(), orderEnv("orders-2")); err != nil {
		t.Fatal(err)
	}
	if len(n.sent) != 1 {
		t.Fatalf("redelivery after failure must notify, sent=%v", n.sent)
	}
}

func TestMissingIDRejected(t *testing.T) {
	if err := newHandler(&fakeNotifier{}).Handle(context.Background(), events.Envelope{Topic: events.TopicOrderCreated}); err == nil {
		t.Fatal("message without id must be rejected")
	}
}
