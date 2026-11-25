package orders

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shani8615/go-httpkit/idempotency"
	"github.com/shani8615/go-microservices-kit/internal/token"
)

type env struct {
	srv    *httptest.Server
	store  *MemStore
	signer *token.Signer
}

func setup(t *testing.T) env {
	t.Helper()
	signer, _ := token.NewSigner([]byte(strings.Repeat("k", 32)), "auth", "kit", time.Hour)
	store := NewMemStore()
	mux := http.NewServeMux()
	(&Service{Store: store, Catalog: DefaultCatalog, Tokens: signer, Idempotency: idempotency.NewMemoryStore(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return env{srv, store, signer}
}

func (e env) do(t *testing.T, user, method, path, body string, hdr ...string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if user != "" {
		tok, _, _ := e.signer.Issue(user, "")
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestCreateWritesOrderAndEvent(t *testing.T) {
	e := setup(t)
	code, body := e.do(t, "alice", "POST", "/v1/orders", `{"sku":"MUG-GOPHER","quantity":2}`)
	if code != 201 {
		t.Fatalf("code=%d body=%s", code, body)
	}
	var o Order
	_ = json.Unmarshal(body, &o)
	if o.TotalCents != 2998 || o.UserID != "alice" {
		t.Fatalf("order=%+v", o)
	}
	if e.store.Outbox.Pending() != 1 {
		t.Fatal("orders.created must be in the outbox")
	}
}

func TestPriceComesFromCatalog(t *testing.T) {
	e := setup(t)
	if code, _ := e.do(t, "alice", "POST", "/v1/orders", `{"sku":"MUG-GOPHER","quantity":1,"unit_price_cents":1}`); code != 400 {
		t.Fatalf("client-supplied price must be rejected, code=%d", code)
	}
	if code, _ := e.do(t, "alice", "POST", "/v1/orders", `{"sku":"NOPE","quantity":1}`); code != 422 {
		t.Fatalf("unknown sku code=%d", code)
	}
}

func TestIdempotentCreate(t *testing.T) {
	e := setup(t)
	body := `{"sku":"BOOK-GO","quantity":1}`
	_, a := e.do(t, "alice", "POST", "/v1/orders", body, "Idempotency-Key", "abc")
	_, b := e.do(t, "alice", "POST", "/v1/orders", body, "Idempotency-Key", "abc")
	if string(a) != string(b) || e.store.Outbox.Pending() != 1 {
		t.Fatalf("retry created a second order: %s vs %s", a, b)
	}
	// Same key, different user: must be a separate order.
	e.do(t, "bob", "POST", "/v1/orders", body, "Idempotency-Key", "abc")
	if e.store.Outbox.Pending() != 2 {
		t.Fatal("idempotency keys must be scoped per user")
	}
}

func TestOrdersAreScopedToOwner(t *testing.T) {
	e := setup(t)
	_, body := e.do(t, "alice", "POST", "/v1/orders", `{"sku":"BOOK-GO","quantity":1}`)
	var o Order
	_ = json.Unmarshal(body, &o)

	if code, _ := e.do(t, "bob", "GET", "/v1/orders/"+o.ID, ""); code != 404 {
		t.Fatalf("bob read alice's order: code=%d", code)
	}
	if code, _ := e.do(t, "alice", "GET", "/v1/orders/"+o.ID, ""); code != 200 {
		t.Fatalf("owner read: code=%d", code)
	}
	if code, _ := e.do(t, "", "GET", "/v1/orders", ""); code != 401 {
		t.Fatalf("anonymous list: code=%d", code)
	}
}

func TestListPaginates(t *testing.T) {
	e := setup(t)
	for i := 0; i < 5; i++ {
		e.do(t, "alice", "POST", "/v1/orders", `{"sku":"STICKER-PACK","quantity":1}`)
		time.Sleep(time.Millisecond)
	}
	e.do(t, "bob", "POST", "/v1/orders", `{"sku":"STICKER-PACK","quantity":1}`)

	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 5; page++ {
		_, body := e.do(t, "alice", "GET", fmt.Sprintf("/v1/orders?limit=2&cursor=%s", cursor), "")
		var res struct {
			Items      []Order `json:"items"`
			NextCursor string  `json:"next_cursor"`
			HasMore    bool    `json:"has_more"`
		}
		_ = json.Unmarshal(body, &res)
		for _, o := range res.Items {
			if seen[o.ID] || o.UserID != "alice" {
				t.Fatalf("duplicate or foreign order in page %d", page)
			}
			seen[o.ID] = true
		}
		if !res.HasMore {
			break
		}
		cursor = res.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("paged through %d orders, want 5", len(seen))
	}
}
