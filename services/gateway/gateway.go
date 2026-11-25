// Package gateway is the single public entry point. It routes by path prefix
// to internal services and adds per-client rate limiting, a circuit breaker
// per upstream, CORS, request IDs and access logs. Authentication is still
// enforced by each service (defense in depth); the gateway never sets
// identity headers.
package gateway

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/shani8615/go-httpkit/middleware"
	"github.com/shani8615/go-httpkit/respond"
	"github.com/shani8615/go-resilience/breaker"
	"github.com/shani8615/go-resilience/ratelimit"
)

// Route maps a public prefix to an upstream service and its internal prefix.
type Route struct {
	Prefix   string // e.g. "/api/orders"
	Upstream string // e.g. "http://orders:8080"
	Rewrite  string // e.g. "/v1/orders"
}

// Config for New.
type Config struct {
	Routes         []Route
	RatePerSec     float64 // per client IP; default 20
	Burst          int     // default 40
	AllowedOrigins []string
	// TrustProxyHeaders uses the first X-Forwarded-For address as the client
	// IP. Only enable it when a trusted load balancer sets that header.
	TrustProxyHeaders bool
	Log               *slog.Logger
	Transport         http.RoundTripper
}

var errUpstream5xx = errors.New("upstream 5xx")

// New builds the gateway handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.RatePerSec <= 0 {
		cfg.RatePerSec = 20
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 40
	}
	if cfg.Transport == nil {
		cfg.Transport = &http.Transport{
			Proxy:               nil, // internal traffic never goes through an egress proxy
			MaxIdleConnsPerHost: 64,
			IdleConnTimeout:     90 * time.Second,
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, rt := range cfg.Routes {
		target, err := url.Parse(rt.Upstream)
		if err != nil {
			return nil, err
		}
		br := breaker.New(breaker.Settings{
			FailureThreshold: 5, OpenTimeout: 10 * time.Second,
			OnStateChange: func(from, to breaker.State) {
				cfg.Log.Warn("circuit breaker state change", slog.String("upstream", rt.Upstream),
					slog.String("from", from.String()), slog.String("to", to.String()))
			},
		})
		rt := rt
		proxy := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.Out.URL.Path = rt.Rewrite + strings.TrimPrefix(pr.In.URL.Path, rt.Prefix)
				pr.Out.URL.RawPath = ""
				pr.Out.Host = target.Host
				pr.SetXForwarded()
				pr.Out.Header.Set(middleware.RequestIDHeader, middleware.GetRequestID(pr.In.Context()))
			},
			Transport: breakerTransport{next: cfg.Transport, b: br},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				if errors.Is(err, breaker.ErrOpen) {
					w.Header().Set("Retry-After", "10")
					respond.Error(w, http.StatusServiceUnavailable, "service temporarily unavailable")
					return
				}
				cfg.Log.ErrorContext(r.Context(), "upstream error", slog.String("upstream", rt.Upstream), slog.String("error", err.Error()))
				respond.Error(w, http.StatusBadGateway, "upstream unavailable")
			},
		}
		mux.Handle(rt.Prefix, proxy)
		mux.Handle(rt.Prefix+"/", proxy)
	}

	limiter := ratelimit.NewKeyed(cfg.RatePerSec, cfg.Burst, 10*time.Minute)
	rateLimit := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow(clientIP(r, cfg.TrustProxyHeaders)) {
				w.Header().Set("Retry-After", "1")
				respond.Error(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	return middleware.Chain(mux,
		middleware.RequestID,
		middleware.Logger(cfg.Log),
		middleware.Recover(cfg.Log),
		middleware.CORS(middleware.CORSOptions{AllowedOrigins: cfg.AllowedOrigins, MaxAge: time.Hour}),
		rateLimit,
		middleware.MaxBodyBytes(1<<20),
	), nil
}

type breakerTransport struct {
	next http.RoundTripper
	b    *breaker.Breaker
}

func (t breakerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := breaker.Execute(t.b, func() (*http.Response, error) {
		resp, err := t.next.RoundTrip(r)
		if err == nil && resp.StatusCode >= 500 {
			return resp, errUpstream5xx // counts as a failure, response still returned
		}
		return resp, err
	})
	if errors.Is(err, errUpstream5xx) {
		return resp, nil
	}
	return resp, err
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			ip, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(ip)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
