// Package token issues and verifies HS256 JWT access tokens using only the
// standard library. It accepts exactly one algorithm (HS256), so "alg: none"
// and algorithm-confusion attacks are rejected by construction.
//
// For production with several issuers or key rotation, swap this for an
// asymmetric scheme (EdDSA/RS256 + JWKS) behind the same Verifier interface.
package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Claims carried in an access token.
type Claims struct {
	Subject   string `json:"sub"`
	Email     string `json:"email,omitempty"`
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

var (
	ErrMalformed = errors.New("token: malformed")
	ErrSignature = errors.New("token: invalid signature")
	ErrExpired   = errors.New("token: expired")
	ErrClaims    = errors.New("token: invalid issuer or audience")
	ErrWeakKey   = errors.New("token: secret must be at least 32 bytes")
)

// Signer issues and verifies tokens with a shared secret.
type Signer struct {
	secret   []byte
	issuer   string
	audience string
	ttl      time.Duration
	now      func() time.Time
}

// NewSigner validates the secret length and returns a Signer.
func NewSigner(secret []byte, issuer, audience string, ttl time.Duration) (*Signer, error) {
	if len(secret) < 32 {
		return nil, ErrWeakKey
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &Signer{secret: secret, issuer: issuer, audience: audience, ttl: ttl, now: time.Now}, nil
}

var header = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// Issue returns a signed token for subject.
func (s *Signer) Issue(subject, email string) (string, Claims, error) {
	now := s.now()
	c := Claims{
		Subject: subject, Email: email, Issuer: s.issuer, Audience: s.audience,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(s.ttl).Unix(),
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", c, err
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	return unsigned + "." + s.sign(unsigned), c, nil
}

// Verify checks signature, expiry, issuer and audience.
func (s *Signer) Verify(tok string) (Claims, error) {
	var c Claims
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return c, ErrMalformed
	}
	if parts[0] != header {
		return c, ErrMalformed // only our exact HS256 header is accepted
	}
	want := s.sign(parts[0] + "." + parts[1])
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return c, ErrSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, ErrMalformed
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, ErrMalformed
	}
	if c.Issuer != s.issuer || c.Audience != s.audience || c.Subject == "" {
		return c, ErrClaims
	}
	if s.now().Unix() >= c.ExpiresAt {
		return c, ErrExpired
	}
	return c, nil
}

func (s *Signer) sign(data string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
