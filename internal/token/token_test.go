package token

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func newSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := NewSigner(secret, "auth", "kit", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoundTrip(t *testing.T) {
	s := newSigner(t)
	tok, _, err := s.Issue("u1", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Verify(tok)
	if err != nil || c.Subject != "u1" || c.Email != "a@example.com" {
		t.Fatalf("c=%+v err=%v", c, err)
	}
}

func TestRejects(t *testing.T) {
	s := newSigner(t)
	tok, _, _ := s.Issue("u1", "")
	parts := strings.Split(tok, ".")

	other, _ := NewSigner([]byte(strings.Repeat("x", 32)), "auth", "kit", time.Minute)
	forged, _, _ := other.Issue("u1", "")

	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + parts[1] + "."
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"admin","iss":"auth","aud":"kit","exp":9999999999}`)) + "." + parts[2]

	cases := map[string]error{
		"garbage":       ErrMalformed,
		none:            ErrMalformed,
		tampered:        ErrSignature,
		forged:          ErrSignature,
		parts[0] + ".x": ErrMalformed,
	}
	for in, want := range cases {
		if _, err := s.Verify(in); !errors.Is(err, want) {
			t.Errorf("%.30q: got %v want %v", in, err, want)
		}
	}

	wrongAud, _ := NewSigner(secret, "auth", "other", time.Minute)
	if _, err := wrongAud.Verify(tok); !errors.Is(err, ErrClaims) {
		t.Errorf("audience: got %v", err)
	}

	s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := s.Verify(tok); !errors.Is(err, ErrExpired) {
		t.Errorf("expiry: got %v", err)
	}
}

func TestWeakKey(t *testing.T) {
	if _, err := NewSigner([]byte("short"), "a", "b", 0); !errors.Is(err, ErrWeakKey) {
		t.Fatal("short secret must be rejected")
	}
}

func TestRequireAuth(t *testing.T) {
	s := newSigner(t)
	var sub string
	h := RequireAuth(s)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		c, _ := FromContext(r.Context())
		sub = c.Subject
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 401 {
		t.Fatalf("no token: code=%d", rec.Code)
	}

	tok, _, _ := s.Issue("u42", "")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || sub != "u42" {
		t.Fatalf("code=%d sub=%q", rec.Code, sub)
	}
}
