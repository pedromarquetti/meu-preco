package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"meupreco/internal/auth"
	"meupreco/internal/config"
	"meupreco/internal/domain"
	"meupreco/internal/httpx"
)

const maxAuthBody = 16 << 10 // passwords/refresh tokens fit in 16 KiB
const maxNameLength = 100    // reject long display names

// userView is the shape of a user in every auth response: never includes the
// password hash (asserted by tests).
type userView struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// authResponse is the login/refresh success payload: an access+refresh pair.
type authResponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	User         userView `json:"user"`
}

// authAPI token signer/verifier and the RequireAuth/RequireRole gates.
type authAPI struct {
	tokens        *auth.TokenService
	mw            *auth.Middleware
	users         domain.UserStore
	refreshTokens domain.RefreshTokenStore
}

func newAuthAPI(cfg config.Config, stores domain.Stores) *authAPI {
	tokens := auth.NewTokenService(cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
	api := &authAPI{
		tokens: tokens,
		mw:     auth.NewMiddleware(tokens),
	}

	if stores == nil {
		// Construction path for the no-database readyz test; auth handlers
		// are unreachable in this state (readyz answers 503).
		return api
	}

	api.users = stores.Users()
	api.refreshTokens = stores.RefreshTokens()
	return api
}

// register implements POST /api/v1/auth/register: validate, bcrypt-hash, and
// insert a role="user" account. Duplicate emails are a 409; the hash never
// leaves the server.
func (a *authAPI) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if !decodeAuthBody(w, r, &req) {
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLength {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "invalid_name", "a name between 1 and 100 characters is required")
		return
	}

	email := normalizeEmail(req.Email)
	if _, err := mail.ParseAddress(email); err != nil || !strings.Contains(email, "@") {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "invalid_email", "a valid email address is required")
		return
	}

	if err := auth.ValidatePassword(req.Password); err != nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "invalid_password", err.Error())
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		httpx.InternalError(w, r)
		return
	}

	u := &domain.User{Name: name, Email: email, PasswordHash: hash, Role: "user"}
	if err := a.users.Create(r.Context(), u); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			httpx.Error(w, r, http.StatusConflict, "user_exists", "name or email is already registered")
			return
		}
		httpx.InternalError(w, r)
		return
	}

	httpx.Respond(w, http.StatusCreated, map[string]any{"user": toUserView(u)})
}

// login implements POST /api/v1/auth/login: resolve the identifier (email or
// name), verify the password and hand out an access+refresh pair (new token
// family). The legacy "email" body field is still accepted as an alias.
func (a *authAPI) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login    string `json:"login"`
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}

	login := firstNonEmpty(req.Login, req.Email, req.Name)
	if login == "" {
		httpx.Error(w, r, http.StatusUnauthorized, "invalid_credentials", "email/name or password is incorrect")
		return
	}

	u, err := a.users.GetByLogin(r.Context(), normalizeLogin(login))
	if err != nil {
		httpx.Error(w, r, http.StatusUnauthorized, "invalid_credentials", "email or name or password is incorrect")
		return
	}

	if !auth.CheckPassword(u.PasswordHash, req.Password) {
		httpx.Error(w, r, http.StatusUnauthorized, "invalid_credentials", "email or name or password is incorrect")
		return
	}

	pair, err := a.issuePair(r.Context(), u, "")
	if err != nil {
		httpx.InternalError(w, r)
		return
	}

	httpx.Respond(w, http.StatusOK, pair)
}

// refresh implements POST /api/v1/auth/refresh: rotate the presented refresh
// token into a new access+refresh pair in the same family. A token that was
// already rotated (reuse) revokes the whole family and answers 401.
func (api *authAPI) refreshHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}

	claims, err := api.tokens.Validate(req.RefreshToken, auth.TypeRefresh)
	if err != nil || claims.Family == "" {
		httpx.Error(w, r, http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid or expired")
		return
	}

	uid, err := auth.SubjectID(claims)
	if err != nil {
		httpx.Error(w, r, http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid or expired")
		return
	}
	u := &domain.User{ID: uid, Email: claims.Email, Role: claims.Role}
	if api.users != nil {
		full, err := api.users.GetByID(r.Context(), uid)
		switch {
		case err == nil:
			u = full
		case errors.Is(err, domain.ErrNotFound):
			httpx.Error(w, r, http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid or expired")
			return
		default:
			httpx.InternalError(w, r)
			return
		}
	}

	access, err := api.tokens.IssueAccess(u)
	if err != nil {
		httpx.InternalError(w, r)
		return
	}

	nextRefresh, err := api.tokens.IssueRefresh(u, claims.Family)
	if err != nil {
		httpx.InternalError(w, r)
		return
	}

	next := &domain.RefreshToken{
		UserID:    uid,
		FamilyID:  claims.Family,
		TokenHash: auth.TokenHash(nextRefresh),
		ExpiresAt: time.Now().Add(api.tokens.RefreshTTL()),
	}

	err = api.refreshTokens.Rotate(r.Context(), auth.TokenHash(req.RefreshToken), next)
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrConflict):
		// The presented token was already consumed: reuse detected. Kill the
		// whole family so a stolen token cannot keep refreshing.
		_ = api.refreshTokens.RevokeFamily(r.Context(), claims.Family)
		httpx.Error(w, r, http.StatusUnauthorized, "refresh_reuse_detected", "refresh token has already been used")
		return
	case err != nil:
		httpx.InternalError(w, r)
		return
	}

	httpx.Respond(w, http.StatusOK, authResponse{
		AccessToken:  access,
		RefreshToken: nextRefresh,
		User:         toUserView(u),
	})
}

// me implements GET /api/v1/auth/me: the authenticated user loaded fresh from
// the store so name and created_at are current.
func (a *authAPI) me(w http.ResponseWriter, r *http.Request) {
	claims := auth.Principal(r.Context())
	if claims == nil {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthorized", "missing bearer token")
		return
	}
	id, err := auth.SubjectID(claims)
	if err != nil {
		httpx.InternalError(w, r)
		return
	}

	if a.users == nil {
		// No-database construction path (readyz tests): degrade to claims.
		httpx.Respond(w, http.StatusOK, map[string]any{"user": userView{ID: id, Email: claims.Email, Role: claims.Role}})
		return
	}

	u, err := a.users.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			httpx.Error(w, r, http.StatusUnauthorized, "unauthorized", "account no longer exists")
			return
		}
		httpx.InternalError(w, r)
		return
	}
	httpx.Respond(w, http.StatusOK, map[string]any{"user": toUserView(u)})
}

// issuePair mints an access+refresh pair with a shared family and records the
// refresh token hash. family == "" starts a new family.
func (a *authAPI) issuePair(ctx context.Context, u *domain.User, family string) (authResponse, error) {
	if family == "" {
		f, err := auth.RandomHex(16)
		if err != nil {
			return authResponse{}, err
		}
		family = f
	}

	access, err := a.tokens.IssueAccess(u)
	if err != nil {
		return authResponse{}, err
	}
	refresh, err := a.tokens.IssueRefresh(u, family)
	if err != nil {
		return authResponse{}, err
	}

	rt := &domain.RefreshToken{
		UserID:    u.ID,
		FamilyID:  family,
		TokenHash: auth.TokenHash(refresh),
		ExpiresAt: time.Now().Add(a.tokens.RefreshTTL()),
	}
	if err := a.refreshTokens.Create(ctx, rt); err != nil {
		return authResponse{}, err
	}

	return authResponse{AccessToken: access, RefreshToken: refresh, User: toUserView(u)}, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// normalizeLogin trims the login identifier; emails are lowercased by the
// store lookup, names are matched exactly as typed.
func normalizeLogin(login string) string {
	return strings.TrimSpace(login)
}

// firstNonEmpty returns the first non-empty value, used to accept the legacy
// email key alongside the canonical login key.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func toUserView(u *domain.User) userView {
	return userView{ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role, CreatedAt: u.CreatedAt}
}

// decodeAuthBody decodes a small JSON body into dst; a malformed body is
// answered 400 and returns false.
func decodeAuthBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "body must be valid JSON")
		return false
	}
	return true
}
