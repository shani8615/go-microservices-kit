package auth

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shani8615/go-microservices-kit/internal/token"
)

func init() { pbkdf2Iter = 1_000 }

func newTestServer(t *testing.T) (*httptest.Server, *MemStore) {
	t.Helper()
	signer, err := token.NewSigner([]byte(strings.Repeat("k", 32)), "auth", "kit", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemStore()
	mux := http.NewServeMux()
	(&Service{Store: store, Tokens: signer, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, store
}

func call(t *testing.T, method, url, body, bearer string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestRegisterLoginMe(t *testing.T) {
	srv, store := newTestServer(t)
	creds := `{"email":"Dev@Example.com","password":"correct horse battery"}`

	code, out := call(t, "POST", srv.URL+"/v1/register", creds, "")
	if code != 201 || out["email"] != "dev@example.com" {
		t.Fatalf("register: %d %v", code, out)
	}
	if store.Outbox.Pending() != 1 {
		t.Fatal("users.signed_up must be written to the outbox with the user")
	}
	if code, _ := call(t, "POST", srv.URL+"/v1/register", creds, ""); code != 409 {
		t.Fatalf("duplicate register: %d", code)
	}

	code, out = call(t, "POST", srv.URL+"/v1/login", creds, "")
	tok, _ := out["access_token"].(string)
	if code != 200 || tok == "" {
		t.Fatalf("login: %d %v", code, out)
	}
	code, out = call(t, "GET", srv.URL+"/v1/me", "", tok)
	if code != 200 || out["email"] != "dev@example.com" {
		t.Fatalf("me: %d %v", code, out)
	}
}

func TestLoginFailuresAreGeneric(t *testing.T) {
	srv, _ := newTestServer(t)
	call(t, "POST", srv.URL+"/v1/register", `{"email":"a@example.com","password":"correct horse battery"}`, "")

	_, wrongPw := call(t, "POST", srv.URL+"/v1/login", `{"email":"a@example.com","password":"wrong password!!"}`, "")
	code, noUser := call(t, "POST", srv.URL+"/v1/login", `{"email":"nobody@example.com","password":"wrong password!!"}`, "")
	if code != 401 || wrongPw["detail"] != noUser["detail"] {
		t.Fatalf("login errors must not reveal whether the email exists: %v vs %v", wrongPw, noUser)
	}
}

func TestRegisterValidation(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, body := range []string{
		`{"email":"not-an-email","password":"correct horse battery"}`,
		`{"email":"a@example.com","password":"short"}`,
		`{"email":"a@example.com","password":"correct horse battery","role":"admin"}`,
	} {
		if code, _ := call(t, "POST", srv.URL+"/v1/register", body, ""); code != 400 {
			t.Errorf("%s: code=%d", body, code)
		}
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("s3cret-password")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "s3cret-password") || CheckPassword(h, "s3cret-passwore") || CheckPassword("garbage", "x") {
		t.Fatal("password check wrong")
	}
	h2, _ := HashPassword("s3cret-password")
	if h == h2 {
		t.Fatal("hashes must be salted")
	}
}
