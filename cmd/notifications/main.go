// Command notifications consumes domain events and notifies users.
package main

import (
	"log/slog"
	"net/http"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/shani8615/go-microservices-kit/internal/events"
	"github.com/shani8615/go-microservices-kit/internal/natsbus"
	"github.com/shani8615/go-microservices-kit/internal/platform"
	"github.com/shani8615/go-microservices-kit/services/notifications"
)

func main() {
	log := platform.Logger("notifications")
	if err := run(log); err != nil {
		log.Error("fatal", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := platform.SignalContext()
	defer stop()

	db, err := platform.OpenDB(ctx, log, "pgx", platform.MustEnv(log, "DATABASE_URL"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, notifications.Schema); err != nil {
		return err
	}

	bus, err := natsbus.Connect(ctx, platform.Env("NATS_URL", "nats://localhost:4222"), "notifications", log)
	if err != nil {
		return err
	}
	defer bus.Close()

	h := &notifications.Handler{Dedup: notifications.PGDedup{DB: db}, Notifier: notifications.LogNotifier{Log: log}, Log: log}
	stopConsumer, err := bus.Subscribe(ctx, "notifications",
		[]string{events.TopicOrderCreated, events.TopicUserSignedUp}, h.Handle)
	if err != nil {
		return err
	}
	defer stopConsumer()

	mux := http.NewServeMux()
	platform.Health(mux, db.PingContext)
	return platform.Serve(ctx, log, &http.Server{Addr: platform.Env("HTTP_ADDR", ":8080"), Handler: mux})
}
