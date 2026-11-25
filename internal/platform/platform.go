// Package platform holds the cross-cutting plumbing every service shares:
// config, logging, database connection and graceful HTTP shutdown.
package platform

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shani8615/go-resilience/retry"
)

// Env returns the variable or def.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// MustEnv returns the variable or exits. Use it for secrets: there is no default.
func MustEnv(log *slog.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Error("missing required environment variable", slog.String("key", key))
		os.Exit(1)
	}
	return v
}

// Logger returns a JSON logger tagged with the service name.
func Logger(service string) *slog.Logger {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).
		With(slog.String("service", service))
}

// SignalContext is cancelled on SIGINT or SIGTERM.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// OpenDB opens driverName (e.g. "pgx") and waits for the database with backoff,
// which matters when docker-compose starts services before Postgres is ready.
func OpenDB(ctx context.Context, log *slog.Logger, driverName, dsn string) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxIdleTime(5 * time.Minute)
	err = retry.Run(ctx, retry.Policy{MaxAttempts: 10, BaseDelay: 500 * time.Millisecond, MaxDelay: 5 * time.Second,
		OnRetry: func(n int, err error, wait time.Duration) {
			log.Warn("database not ready", slog.Int("attempt", n), slog.Duration("retry_in", wait))
		}},
		func(ctx context.Context) error { return db.PingContext(ctx) })
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect database: %w", err)
	}
	return db, nil
}

// Serve runs srv until ctx ends, then shuts down gracefully.
func Serve(ctx context.Context, log *slog.Logger, srv *http.Server) error {
	if srv.ReadHeaderTimeout == 0 {
		srv.ReadHeaderTimeout = 5 * time.Second
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("http server listening", slog.String("addr", srv.Addr))
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// Health registers liveness and readiness probes. ready may be nil.
func Health(mux *http.ServeMux, ready func(ctx context.Context) error) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if ready != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := ready(ctx); err != nil {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

// NewID returns a random RFC 4122 version 4 UUID.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// IsUniqueViolation reports a Postgres unique_violation (SQLSTATE 23505)
// without importing a specific driver.
func IsUniqueViolation(err error) bool {
	var s interface{ SQLState() string }
	return errors.As(err, &s) && s.SQLState() == "23505"
}
