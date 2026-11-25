package token

import (
	"context"
	"net/http"
	"strings"

	"github.com/shani8615/go-httpkit/respond"
)

// Verifier verifies a raw token.
type Verifier interface {
	Verify(tok string) (Claims, error)
}

type ctxKey struct{}

// RequireAuth rejects requests without a valid Bearer token and stores the
// claims in the request context. Services verify tokens themselves rather
// than trusting identity headers set by the gateway.
func RequireAuth(v Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || raw == "" {
				respond.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			c, err := v.Verify(raw)
			if err != nil {
				respond.Error(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c)))
		})
	}
}

// FromContext returns the claims set by RequireAuth.
func FromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(Claims)
	return c, ok
}
