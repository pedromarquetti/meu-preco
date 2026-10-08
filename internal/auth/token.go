// Package auth implements authentication: JWT issuing/validation
// (golang-jwt/v5), bcrypt password hashing, and the RequireAuth / RequireRole
// HTTP middleware. It has no store dependency so every piece is unit-testable.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"meupreco/internal/domain"
)

// TokenType discriminates access from refresh tokens in the same key space.
type TokenType = string

const (
	TypeAccess  TokenType = "access"
	TypeRefresh TokenType = "refresh"
)

const issuer = "meupreco"

// Claims is the JWT payload for both token kinds. Access tokens carry
// sub/email/role/type and a short expiry; refresh tokens additionally carry a
// family id used for rotation + reuse detection and a random jti.
type Claims struct {
	Email  string    `json:"email"`
	Role   string    `json:"role"`
	Type   TokenType `json:"type"`
	Family string    `json:"fam,omitempty"`
	jwt.RegisteredClaims
}

// SubjectID parses the numeric user id out of the claims' sub claim.
func SubjectID(c *Claims) (int64, error) {
	return strconv.ParseInt(c.Subject, 10, 64)
}

// TokenService signs and verifies both token kinds with one shared secret.
type TokenService struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewTokenService builds the signer/verifier; secret must already be validated
// (config enforces >= 32 bytes).
func NewTokenService(secret string, accessTTL, refreshTTL time.Duration) *TokenService {
	return &TokenService{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

// AccessTTL exposes the configured access-token lifetime (used by handlers
// that must mirror expiry into DB rows).
func (s *TokenService) AccessTTL() time.Duration { return s.accessTTL }

// RefreshTTL exposes the configured refresh-token lifetime.
func (s *TokenService) RefreshTTL() time.Duration { return s.refreshTTL }

// IssueAccess issues a short-lived access token for u.
func (s *TokenService) IssueAccess(u *domain.User) (string, error) {
	return s.sign(Claims{
		Email: u.Email,
		Role:  u.Role,
		Type:  TypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(u.ID, 10),
			Issuer:    issuer,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.accessTTL)),
		},
	})
}

// IssueRefresh issues a long-lived refresh token. When family is empty a fresh
// family id is generated; a non-empty family keeps rotation in the same family.
func (s *TokenService) IssueRefresh(u *domain.User, family string) (string, error) {
	if family == "" {
		f, err := randomHex(16)
		if err != nil {
			return "", err
		}
		family = f
	}

	randomID, err := randomHex(16)
	if err != nil {
		return "", err
	}

	return s.sign(Claims{
		Email:  u.Email,
		Role:   u.Role,
		Type:   TypeRefresh,
		Family: family,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(u.ID, 10),
			ID:        randomID,
			Issuer:    issuer,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.refreshTTL)),
		},
	})
}

func (s *TokenService) sign(claims Claims) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

// Validate verifies signature, issuer, expiry and that the token carries the
// expected type. It returns the claims on success.
func (s *TokenService) Validate(token string, want TokenType) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims,
		func(*jwt.Token) (any, error) { return s.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)

	if err != nil {
		return nil, err
	}

	if claims.Type != want {
		return nil, fmt.Errorf("auth: token type %q, want %q", claims.Type, want)
	}

	if claims.Subject == "" {
		return nil, errors.New("auth: token missing subject")
	}

	if claims.Family == "" && want == TypeRefresh {
		return nil, errors.New("auth: refresh token missing family")
	}

	return claims, nil
}

// TokenHash fingerprints a refresh token for storage: the raw JWT never
// touches the database, only sha256(token) is persisted and looked up.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RandomHex returns n cryptographically random bytes encoded as lowercase hex
// (used for token families and jti values).
func RandomHex(n int) (string, error) {
	return randomHex(n)
}

// randomHex returns n cryptographically random bytes encoded as lowercase hex.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
