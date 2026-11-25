// Command auth runs the identity service.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/shani8615/go-httpkit/middleware"
	"github.com/shani8615/go-microservices-kit/internal/natsbus"
	"github.com/shani8615/go-microservices-kit/internal/platform"
	"github.com/shani8615/go-microservices-kit/internal/token"
	"github.com/shani8615/go-microservices-kit/services/auth"
	"github.com/shani8615/go-outbox"
)

func main() {
	log := platform.Logger("auth")
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
	if _, err := db.ExecContext(ctx, auth.Schema+outbox.Schema("outbox")); err != nil {
		return err
	}
	ob, err := outbox.NewPostgres(db, "outbox")
	if err != nil {
		return err
	}

	bus, err := natsbus.Connect(ctx, platform.Env("NATS_URL", "nats://localhost:4222"), "auth", log)
	if err != nil {
		return err
	}
	defer bus.Close()
	relay := outbox.NewRelay(ob, bus)
	relay.Logger = log
	go func() { _ = relay.Run(ctx) }()

	signer, err := token.NewSigner([]byte(platform.MustEnv(log, "JWT_SECRET")),
		platform.Env("JWT_ISSUER", "auth"), platform.Env("JWT_AUDIENCE", "go-microservices-kit"), 15*time.Minute)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	platform.Health(mux, db.PingContext)
	(&auth.Service{Store: &auth.PGStore{DB: db, Outbox: ob}, Tokens: signer, Log: log}).Routes(mux)

	h := middleware.Chain(mux, middleware.RequestID, middleware.Logger(log), middleware.Recover(log))
	return platform.Serve(ctx, log, &http.Server{Addr: platform.Env("HTTP_ADDR", ":8080"), Handler: h})
}
