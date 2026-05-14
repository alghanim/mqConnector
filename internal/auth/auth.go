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
}

// Service is the auth backend. Construct with NewService.
type Service struct {
	client     authClient
	cookieName string
	sessionTTL time.Duration
	secure     bool
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

// Login exchanges credentials for a JWT via SimpleAuth.
func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	tok, err := s.client.Login(ctx, username, password)
	if err != nil {
		return "", ErrInvalidCredentials
	}
	if tok == nil || tok.AccessToken == "" {
		return "", ErrInvalidCredentials
	}
	return tok.AccessToken, nil
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
