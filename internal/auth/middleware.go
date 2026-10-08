package auth

import (
	"context"
	"net/http"
	"strings"

	"meupreco/internal/httpx"
)

type principalKey struct{}

// Middleware wires TokenService validation into chi as RequireAuth and
// RequireRole gates. Every protected route runs behind one of these.
type Middleware struct {
	tokens *TokenService
}

// NewMiddleware builds the auth gates for one token service.
func NewMiddleware(tokens *TokenService) *Middleware {
	return &Middleware{tokens: tokens}
}

// RequireAuth accepts an access token and puts its claims on the request
// context. Missing, malformed, expired or wrong-type tokens get a 401.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := m.authenticate(w, r)
		if !ok {
			return
		}
		next.ServeHTTP(w, r.WithContext(contextWithPrincipal(r.Context(), claims)))
	})
}

// RequireRole accepts an access token whose role matches role, else 401/403.
func (m *Middleware) RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := m.authenticate(w, r)

			if !ok {
				return
			}
			
			if claims.Role != role {
				httpx.Error(w, r, http.StatusForbidden, "forbidden", "insufficient role")
				return
			}

			next.ServeHTTP(w, r.WithContext(contextWithPrincipal(r.Context(), claims)))
		})
	}
}

func (m *Middleware) authenticate(w http.ResponseWriter, r *http.Request) (*Claims, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthorized", "missing bearer token")
		return nil, false
	}

	claims, err := m.tokens.Validate(token, TypeAccess)
	if err != nil {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
		return nil, false
	}
	return claims, true
}

func contextWithPrincipal(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, principalKey{}, c)
}

// Principal returns the authenticated claims stored on the context, or nil.
func Principal(ctx context.Context) *Claims {
	c, _ := ctx.Value(principalKey{}).(*Claims)
	return c
}
