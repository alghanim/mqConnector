// Package auth integrates the department's SimpleAuth identity service into
// mqConnector. Login is forwarded to the SimpleAuth server; the returned JWT
// is wrapped in an HttpOnly + Secure cookie. Middleware validates incoming
// cookies by verifying the JWT against SimpleAuth's JWKS endpoint.
//
// SimpleAuth (https://github.com/bodaay/SimpleAuth) is the department's
// air-gapped identity service. Third-party OIDC is intentionally not supported.
package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/bodaay/simpleauth-go"
)

// Common errors.
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrUnauthorized       = errors.New("auth: unauthorized")
)

// authClient is the subset of simpleauth.Client the Service depends on,
// extracted to an interface so tests can substitute a fake.
type authClient interface {
	Login(ctx context.Context, username, password string) (*simpleauth.TokenResponse, error)
	Verify(token string) (*simpleauth.User, error)
	Refresh(ctx context.Context, refreshToken string) (*simpleauth.TokenResponse, error)
}

// refreshCookieSuffix is appended to CookieName to derive the refresh-cookie
// name. So the default pair is "mqc_session" and "mqc_session_refresh". They
// share Secure + SameSite settings.
const refreshCookieSuffix = "_refresh"

// Service is the auth backend. Construct with NewService.
type Service struct {
	client         authClient
	cookieName     string
	sessionTTL     time.Duration
	secure         bool
	tenantResolver TenantResolver // optional; nil = single-tenant fallback
}

// Options bundle the constructor arguments. Secure controls the cookie Secure
// flag — set to false only in dev mode.
type Options struct {
	SimpleAuthURL       string
	SimpleAuthAdminKey  string
	InsecureSkipVerify  bool
	CookieName          string
	SessionTTL          time.Duration
	Secure              bool
}

// NewService constructs a Service backed by a real SimpleAuth client.
func NewService(opts Options) (*Service, error) {
	if opts.SimpleAuthURL == "" {
		return nil, errors.New("auth: simpleauth_url is required")
	}
	if opts.CookieName == "" {
		opts.CookieName = "mqc_session"
	}
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = 12 * time.Hour
	}
	client := simpleauth.New(simpleauth.Options{
		URL:                opts.SimpleAuthURL,
		AdminKey:           opts.SimpleAuthAdminKey,
		InsecureSkipVerify: opts.InsecureSkipVerify,
	})
	return newServiceWith(client, opts), nil
}

// NewServiceForTest wraps any authClient (used by tests outside this package
// to inject fakes — e.g. internal/server's integration tests).
//
// Production code MUST use NewService instead; this helper exists only so
// the test suite can avoid a real network round-trip to SimpleAuth.
func NewServiceForTest(client AuthClient, opts Options) *Service {
	return newServiceWith(client, opts)
}

// AuthClient is the exported alias for the unexported authClient interface.
// Required for NewServiceForTest because Go won't let cross-package callers
// satisfy an unexported interface even when the type signatures match.
type AuthClient = authClient

// newServiceWith is the test seam — wraps any authClient into a Service.
func newServiceWith(client authClient, opts Options) *Service {
	if opts.CookieName == "" {
		opts.CookieName = "mqc_session"
	}
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = 12 * time.Hour
	}
	return &Service{
		client:     client,
		cookieName: opts.CookieName,
		sessionTTL: opts.SessionTTL,
		secure:     opts.Secure,
	}
}

// Login exchanges credentials for a JWT via SimpleAuth. Returns only the
// access token — keep using this for tests that don't care about refresh.
func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	access, _, err := s.LoginWithRefresh(ctx, username, password)
	return access, err
}

// LoginWithRefresh is the full-fidelity login: returns both access and
// refresh tokens. The refresh token may be empty when SimpleAuth is
// configured not to issue them — callers must handle that gracefully.
func (s *Service) LoginWithRefresh(ctx context.Context, username, password string) (access, refresh string, err error) {
	tok, err := s.client.Login(ctx, username, password)
	if err != nil {
		return "", "", ErrInvalidCredentials
	}
	if tok == nil || tok.AccessToken == "" {
		return "", "", ErrInvalidCredentials
	}
	return tok.AccessToken, tok.RefreshToken, nil
}

// Refresh exchanges a refresh token for a fresh access/refresh pair.
// Returns ErrUnauthorized if the refresh token is empty or rejected by
// SimpleAuth (e.g. expired or revoked).
func (s *Service) Refresh(ctx context.Context, refreshToken string) (access, refresh string, err error) {
	if refreshToken == "" {
		return "", "", ErrUnauthorized
	}
	tok, err := s.client.Refresh(ctx, refreshToken)
	if err != nil || tok == nil || tok.AccessToken == "" {
		return "", "", ErrUnauthorized
	}
	// SimpleAuth may rotate the refresh token; fall back to the old one if
	// it doesn't.
	out := tok.RefreshToken
	if out == "" {
		out = refreshToken
	}
	return tok.AccessToken, out, nil
}

// Validate verifies a JWT and returns the SimpleAuth User claims.
func (s *Service) Validate(token string) (*simpleauth.User, error) {
	if token == "" {
		return nil, ErrUnauthorized
	}
	user, err := s.client.Verify(token)
	if err != nil {
		return nil, ErrUnauthorized
	}
	return user, nil
}

// SetCookie writes the JWT-bearing session cookie to the response.
func (s *Service) SetCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.sessionTTL.Seconds()),
	})
}

// ClearCookie writes an immediately-expired session cookie. Server-side
// invalidation of JWTs requires SimpleAuth-side revocation; clearing the
// cookie removes the local artefact.
func (s *Service) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// CookieName exposes the configured cookie name (handlers need it for reads).
func (s *Service) CookieName() string {
	return s.cookieName
}

// SetRefreshCookie stores the refresh token under <cookieName>_refresh.
// A longer MaxAge gives the UI a window to use Refresh after the session
// cookie has expired — capped at SimpleAuth's own refresh-token lifetime
// (we use 7 days here as a reasonable browser default).
func (s *Service) SetRefreshCookie(w http.ResponseWriter, token string) {
	if token == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName + refreshCookieSuffix,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((7 * 24 * time.Hour).Seconds()),
	})
}

// ClearRefreshCookie removes the refresh cookie.
func (s *Service) ClearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName + refreshCookieSuffix,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// RefreshCookieValue extracts the refresh token from the request, or ""
// if no refresh cookie is present.
func (s *Service) RefreshCookieValue(r *http.Request) string {
	c, err := r.Cookie(s.cookieName + refreshCookieSuffix)
	if err != nil {
		return ""
	}
	return c.Value
}

// userKey is used to attach the verified SimpleAuth User to a request context.
type userKey struct{}

// WithUser attaches the user to ctx.
func WithUser(ctx context.Context, user *simpleauth.User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

// UserFromContext extracts the authenticated user, if any.
func UserFromContext(ctx context.Context) (*simpleauth.User, bool) {
	u, ok := ctx.Value(userKey{}).(*simpleauth.User)
	return u, ok
}

// tenantKey carries the resolved tenant id (and the caller's role in
// that tenant) through the request context. Populated by middleware
// after RequireSession has run.
type tenantKey struct{}

// TenantClaim is what the handler reads.
type TenantClaim struct {
	TenantID string
	Role     string // "viewer" | "operator" | "admin" | "owner"
}

// WithTenant attaches the active tenant claim to ctx.
func WithTenant(ctx context.Context, c TenantClaim) context.Context {
	return context.WithValue(ctx, tenantKey{}, c)
}

// TenantFromContext returns the active tenant claim. If no claim is
// present (e.g. an internal call path that bypassed the middleware),
// the second return is false and the caller should refuse the request.
// Public handlers should treat the boolean as authoritative.
func TenantFromContext(ctx context.Context) (TenantClaim, bool) {
	c, ok := ctx.Value(tenantKey{}).(TenantClaim)
	return c, ok
}

// TenantID is a sugar helper for the common case where the handler only
// needs the id and trusts the middleware ran. Returns "" if no tenant
// is in the context; the storage layer then rejects with
// ErrTenantRequired, which propagates as a 400/401.
func TenantID(ctx context.Context) string {
	c, _ := TenantFromContext(ctx)
	return c.TenantID
}
