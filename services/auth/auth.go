// Package auth is the identity service: sign-up, login (JWT access tokens)
// and "who am I". It publishes users.signed_up through the outbox.
package auth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/shani8615/go-httpkit/respond"
	"github.com/shani8615/go-microservices-kit/internal/events"
	"github.com/shani8615/go-microservices-kit/internal/platform"
	"github.com/shani8615/go-microservices-kit/internal/token"
	"github.com/shani8615/go-outbox"
)

// User is a stored account.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

var (
	ErrNotFound   = errors.New("auth: user not found")
	ErrEmailTaken = errors.New("auth: email already registered")
)

// Store persists users. Create must write the user and the event atomically.
type Store interface {
	Create(ctx context.Context, u User, evt outbox.Message) error
	ByEmail(ctx context.Context, email string) (User, error)
	ByID(ctx context.Context, id string) (User, error)
}

// ---- password hashing (PBKDF2-SHA256, stdlib, OWASP 2023 iteration count) ----

const (
	saltLen = 16
	keyLen  = 32
)

// pbkdf2Iter is a var only so tests can lower it.
var pbkdf2Iter = 600_000

// HashPassword returns "pbkdf2-sha256$iter$salt$hash".
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, keyLen)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iter, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// CheckPassword compares pw to an encoded hash in constant time.
func CheckPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash keeps login timing similar for unknown emails.
var dummyHash, _ = HashPassword("timing-equaliser")

// ---- HTTP ----

// Service wires handlers to a store and token signer.
type Service struct {
	Store  Store
	Tokens *token.Signer
	Log    *slog.Logger
}

// Routes registers the auth endpoints on mux.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/register", s.register)
	mux.HandleFunc("POST /v1/login", s.login)
	mux.Handle("GET /v1/me", token.RequireAuth(s.Tokens)(http.HandlerFunc(s.me)))
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Service) register(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := respond.Decode(w, r, &in, 4<<10); err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	email, err := normaliseEmail(in.Email)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if l := len(in.Password); l < 12 || l > 128 {
		respond.Error(w, http.StatusBadRequest, "password must be 12-128 characters")
		return
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		s.fail(w, r, "hash password", err)
		return
	}
	u := User{ID: platform.NewID(), Email: email, PasswordHash: hash, CreatedAt: time.Now().UTC()}
	payload, _ := json.Marshal(events.UserSignedUp{UserID: u.ID, Email: u.Email, CreatedAt: u.CreatedAt})
	evt := outbox.Message{Topic: events.TopicUserSignedUp, Key: u.ID, Payload: payload,
		Headers: map[string]string{events.HeaderEventType: "UserSignedUp", events.HeaderVersion: "1"}}

	switch err := s.Store.Create(r.Context(), u, evt); {
	case errors.Is(err, ErrEmailTaken):
		respond.Error(w, http.StatusConflict, "email already registered")
	case err != nil:
		s.fail(w, r, "create user", err)
	default:
		respond.JSON(w, http.StatusCreated, map[string]string{"id": u.ID, "email": u.Email})
	}
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := respond.Decode(w, r, &in, 4<<10); err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	email, _ := normaliseEmail(in.Email)
	u, err := s.Store.ByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		s.fail(w, r, "load user", err)
		return
	}
	hash := u.PasswordHash
	if err != nil {
		hash = dummyHash
	}
	if !CheckPassword(hash, in.Password) || err != nil {
		respond.Error(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	tok, claims, err := s.Tokens.Issue(u.ID, u.Email)
	if err != nil {
		s.fail(w, r, "issue token", err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"access_token": tok, "token_type": "Bearer", "expires_in": claims.ExpiresAt - claims.IssuedAt,
	})
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	c, _ := token.FromContext(r.Context())
	u, err := s.Store.ByID(r.Context(), c.Subject)
	if errors.Is(err, ErrNotFound) {
		respond.Error(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		s.fail(w, r, "load user", err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"id": u.ID, "email": u.Email, "created_at": u.CreatedAt})
}

func (s *Service) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	s.Log.ErrorContext(r.Context(), op, slog.String("error", err.Error()))
	respond.Error(w, http.StatusInternalServerError, "internal error")
}

func normaliseEmail(s string) (string, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil || a.Name != "" || len(a.Address) > 254 {
		return "", errors.New("invalid email")
	}
	return strings.ToLower(a.Address), nil
}
