// Command orders runs the order service.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/shani8615/go-httpkit/idempotency"
	"github.com/shani8615/go-httpkit/middleware"
	"github.com/shani8615/go-microservices-kit/internal/natsbus"
	"github.com/shani8615/go-microservices-kit/internal/platform"
	"github.com/shani8615/go-microservices-kit/internal/token"
	"github.com/shani8615/go-microservices-kit/services/orders"
	"github.com/shani8615/go-outbox"
)

func main() {
	log := platform.Logger("orders")
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
	if _, err := db.ExecContext(ctx, orders.Schema+outbox.Schema("outbox")); err != nil {
		return err
	}
	ob, err := outbox.NewPostgres(db, "outbox")
	if err != nil {
		return err
	}

	bus, err := natsbus.Connect(ctx, platform.Env("NATS_URL", "nats://localhost:4222"), "orders", log)
	if err != nil {
		return err
	}
	defer bus.Close()
	relay := outbox.NewRelay(ob, bus)
	relay.Logger = log
	go func() { _ = relay.Run(ctx) }()

	// Orders only verifies tokens; it never issues them.
	verifier, err := token.NewSigner([]byte(platform.MustEnv(log, "JWT_SECRET")),
		platform.Env("JWT_ISSUER", "auth"), platform.Env("JWT_AUDIENCE", "go-microservices-kit"), 15*time.Minute)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	platform.Health(mux, db.PingContext)
	(&orders.Service{
		Store:   &orders.PGStore{DB: db, Outbox: ob},
		Catalog: orders.DefaultCatalog,
		Tokens:  verifier,
		// In-memory is fine for one replica; use a Redis/Postgres Store when scaling out.
		Idempotency: idempotency.NewMemoryStore(),
		Log:         log,
	}).Routes(mux)

	h := middleware.Chain(mux, middleware.RequestID, middleware.Logger(log), middleware.Recover(log))
	return platform.Serve(ctx, log, &http.Server{Addr: platform.Env("HTTP_ADDR", ":8080"), Handler: h})
}
