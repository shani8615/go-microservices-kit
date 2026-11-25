// Package natsbus connects services to NATS JetStream: it publishes outbox
// messages (with broker-side de-duplication) and runs durable consumers.
package natsbus

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/shani8615/go-microservices-kit/internal/events"
	"github.com/shani8615/go-outbox"
)

// StreamName is the JetStream stream holding all domain events.
const StreamName = "EVENTS"

const msgIDHeader = "Nats-Msg-Id"

// Bus wraps a NATS connection and JetStream context.
type Bus struct {
	nc     *nats.Conn
	js     jetstream.JetStream
	source string
	log    *slog.Logger
}

// Connect dials NATS and makes sure the EVENTS stream exists. source is the
// publishing service name; it prefixes message IDs so IDs from different
// services' outbox tables never collide.
func Connect(ctx context.Context, url, source string, log *slog.Logger) (*Bus, error) {
	nc, err := nats.Connect(url,
		nats.Name(source),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		nats.RetryOnFailedConnect(true),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       StreamName,
		Subjects:   []string{"orders.>", "users.>"},
		Storage:    jetstream.FileStorage,
		MaxAge:     7 * 24 * time.Hour,
		Duplicates: 10 * time.Minute, // de-dup window for Nats-Msg-Id
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("ensure stream: %w", err)
	}
	return &Bus{nc: nc, js: js, source: source, log: log}, nil
}

// Close drains the connection.
func (b *Bus) Close() { _ = b.nc.Drain() }

// Publish implements outbox.Publisher. It returns once JetStream has
// persisted the message.
func (b *Bus) Publish(ctx context.Context, m outbox.Message) error {
	msg := nats.NewMsg(m.Topic)
	msg.Data = m.Payload
	for k, v := range m.Headers {
		msg.Header.Set(k, v)
	}
	msg.Header.Set(msgIDHeader, b.source+"-"+strconv.FormatInt(m.ID, 10))
	_, err := b.js.PublishMsg(ctx, msg)
	return err
}

// Subscribe runs a durable consumer for subjects. A handler error naks the
// message for redelivery after a delay; after MaxDeliver attempts JetStream
// stops redelivering it.
func (b *Bus) Subscribe(ctx context.Context, durable string, subjects []string,
	handle func(context.Context, events.Envelope) error,
) (stop func(), err error) {
	cons, err := b.js.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Durable:        durable,
		FilterSubjects: subjects,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        30 * time.Second,
		MaxDeliver:     10,
	})
	if err != nil {
		return nil, fmt.Errorf("create consumer: %w", err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		env := events.Envelope{Topic: msg.Subject(), Data: msg.Data(), Headers: map[string]string{}}
		for k, v := range msg.Headers() {
			if len(v) > 0 {
				env.Headers[k] = v[0]
			}
		}
		env.ID = env.Headers[msgIDHeader]
		if env.ID == "" {
			if md, err := msg.Metadata(); err == nil {
				env.ID = "seq-" + strconv.FormatUint(md.Sequence.Stream, 10)
			}
		}
		hctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		if err := handle(hctx, env); err != nil {
			b.log.Warn("message handling failed, will retry",
				slog.String("id", env.ID), slog.String("subject", env.Topic), slog.String("error", err.Error()))
			_ = msg.NakWithDelay(5 * time.Second)
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return nil, err
	}
	return cc.Stop, nil
}
