package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestRoutesAndRewritesPath(t *testing.T) {
	var gotPath, gotReqID string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotReqID = r.URL.Path, r.Header.Get("X-Request-ID")
	}))
	defer up.Close()

	gw, err := New(Config{Routes: []Route{{Prefix: "/api/orders", Upstream: up.URL, Rewrite: "/v1/orders"}}, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest("GET", "/api/orders/123", nil))
	if rec.Code != 200 || gotPath != "/v1/orders/123" {
		t.Fatalf("code=%d path=%q", rec.Code, gotPath)
	}
	if gotReqID == "" || gotReqID != rec.Header().Get("X-Request-ID") {
		t.Fatalf("request id not propagated: %q", gotReqID)
	}

	rec = httptest.NewRecorder()
	gw.ServeHTTP(rec, httptest.NewRequest("GET", "/api/unknown", nil))
	if rec.Code != 404 {
		t.Fatalf("unknown route code=%d", rec.Code)
	}
}

func TestRateLimitPerClient(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer up.Close()
	gw, _ := New(Config{Routes: []Route{{Prefix: "/api/x", Upstream: up.URL, Rewrite: "/x"}}, RatePerSec: 0.001, Burst: 2, Log: quiet})

	codes := []int{}
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/api/x", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 {
		t.Fatalf("codes=%v", codes)
	}
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.RemoteAddr = "10.0.0.2:1234"
	req.Header.Set("X-Forwarded-For", "10.0.0.1") // spoofed, not trusted by default
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("other client limited: %d", rec.Code)
	}
}

func TestBreakerOpensOnUpstream5xx(t *testing.T) {
	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(500)
	}))
	defer up.Close()
	gw, _ := New(Config{Routes: []Route{{Prefix: "/api/x", Upstream: up.URL, Rewrite: "/x"}}, Log: quiet})

	var last int
	for i := 0; i < 8; i++ {
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, httptest.NewRequest("GET", "/api/x", nil))
		last = rec.Code
	}
	if last != 503 || atomic.LoadInt32(&hits) != 5 {
		t.Fatalf("last=%d upstream hits=%d (breaker should stop calls after 5 failures)", last, hits)
	}
}
