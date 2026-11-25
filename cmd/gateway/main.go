// Command gateway runs the public API gateway.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/shani8615/go-microservices-kit/internal/platform"
	"github.com/shani8615/go-microservices-kit/services/gateway"
)

func main() {
	log := platform.Logger("gateway")
	ctx, stop := platform.SignalContext()
	defer stop()

	rate, _ := strconv.ParseFloat(platform.Env("RATE_LIMIT_PER_SEC", "20"), 64)
	burst, _ := strconv.Atoi(platform.Env("RATE_LIMIT_BURST", "40"))
	var origins []string
	if v := platform.Env("CORS_ALLOWED_ORIGINS", ""); v != "" {
		origins = strings.Split(v, ",")
	}

	h, err := gateway.New(gateway.Config{
		Routes: []gateway.Route{
			{Prefix: "/api/auth", Upstream: platform.Env("AUTH_URL", "http://localhost:8081"), Rewrite: "/v1"},
			{Prefix: "/api/orders", Upstream: platform.Env("ORDERS_URL", "http://localhost:8082"), Rewrite: "/v1/orders"},
		},
		RatePerSec:        rate,
		Burst:             burst,
		AllowedOrigins:    origins,
		TrustProxyHeaders: platform.Env("TRUST_PROXY_HEADERS", "false") == "true",
		Log:               log,
	})
	if err != nil {
		log.Error("build gateway", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if err := platform.Serve(ctx, log, &http.Server{Addr: platform.Env("HTTP_ADDR", ":8080"), Handler: h}); err != nil {
		log.Error("serve", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
