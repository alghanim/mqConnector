package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/bodaay/simpleauth-go"
)

// DefaultTenantID mirrors storage.DefaultTenantID. Duplicated here to
// avoid an import cycle (auth → storage → auth). Keep in sync.
const DefaultTenantID = "00000000-0000-0000-0000-000000000000"

// TenantResolver is implemented by something that, given a verified
// user and the request, returns the active tenant claim. The concrete
// implementation lives in a higher layer (it needs storage access).
// When the resolver returns ok=false, the request is treated as having
// no active tenant — downstream tenant-required handlers return 403.
//
// Set via Service.SetTenantResolver during server boot. A nil resolver
// falls back to a single-tenant model (every authed user is an
// implicit owner of the default tenant) — that's the behaviour the
// codebase had before multi-tenancy landed and lets pre-16b tests
// keep working unchanged.
type TenantResolver interface {
	Resolve(r *http.Request, user any) (TenantClaim, bool)
}

// SetTenantResolver installs the resolver used by RequireSession.
func (s *Service) SetTenantResolver(tr TenantResolver) { s.tenantResolver = tr }

// APITokenInfo is the shape Service expects from the token store. It
// mirrors storage.APIToken but lives here so the storage package
// doesn't have to depend on auth.
type APITokenInfo struct {
	ID       string
	TenantID string
	UserSub  string
	Name     string
	Role     string
}

// APITokenLookup is implemented by something that resolves a presented
// bearer secret to an active token row. Returning (nil, error) means
// the token isn't valid — the middleware then falls through to cookie
// auth, so a malformed or revoked token doesn't lock a browser session
// out by accident.
type APITokenLookup interface {
	Lookup(ctx context.Context, secret string) (*APITokenInfo, error)
}

// SetAPITokenLookup wires the token store. Calling RequireSession on a
// service without a lookup installed disables Bearer auth entirely
// (cookie session still works).
func (s *Service) SetAPITokenLookup(l APITokenLookup) { s.apiTokenLookup = l }

// extractBearerToken pulls the secret out of `Authorization: Bearer <secret>`.
// Returns "" if the header is missing, malformed, or doesn't carry the
// mqConnector token prefix.
func extractBearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	secret := strings.TrimSpace(h[len(prefix):])
	if !strings.HasPrefix(secret, "mqct_") {
		return ""
	}
	return secret
}

// syntheticUserForToken builds a simpleauth.User shell so downstream
// handlers (audit log "actor", tenant resolver) read the same shape
// regardless of which auth path produced the request.
func syntheticUserForToken(t *APITokenInfo) *simpleauth.User {
	return &simpleauth.User{
		Sub:               t.UserSub,
		PreferredUsername: "api-token:" + t.Name,
	}
}

// RequireSession gates a handler behind a valid credential. Accepts
// two shapes in order:
//
//  1. An `Authorization: Bearer mqct_<secret>` header against the
//     APITokenLookup (when installed). Validates the secret, then
//     populates the request context with a synthetic user and the
//     token's tenant + role claim — bypassing the cookie tenant
//     resolver, because tokens are pinned to a single tenant + role
//     at issue time.
//  2. A session cookie validated against SimpleAuth. Goes through the
//     tenant resolver to derive the active tenant.
//
// A malformed or revoked Bearer token falls through to cookie auth so
// a stale `Authorization` header in a browser tab doesn't lock the
// user out — they still have their cookie.
func (s *Service) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ─── 1) Bearer token ────────────────────────────────────────
		if s.apiTokenLookup != nil {
			if secret := extractBearerToken(r); secret != "" {
				if tok, err := s.apiTokenLookup.Lookup(r.Context(), secret); err == nil && tok != nil {
					user := syntheticUserForToken(tok)
					ctx := WithUser(r.Context(), user)
					ctx = WithTenant(ctx, TenantClaim{
						TenantID: tok.TenantID,
						Role:     tok.Role,
					})
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// Fall through to cookie auth on bad token. Don't 401
				// here — a browser with a stale header would lose its
				// cookie session otherwise.
			}
		}

		// ─── 2) Cookie session ──────────────────────────────────────
		c, err := r.Cookie(s.cookieName)
		if err != nil || c.Value == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		user, err := s.Validate(c.Value)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		ctx := WithUser(r.Context(), user)

		// Resolve the active tenant. With no resolver installed, fall
		// back to the legacy "everyone is owner of the default tenant"
		// shape so single-tenant deployments and the existing test
		// suite keep working unchanged.
		var claim TenantClaim
		if s.tenantResolver != nil {
			if c, ok := s.tenantResolver.Resolve(r, user); ok {
				claim = c
			}
		}
		if claim.TenantID == "" {
			claim = TenantClaim{TenantID: DefaultTenantID, Role: "owner"}
		}
		ctx = WithTenant(ctx, claim)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole gates a handler behind a minimum tenant-scoped role.
// Roles are ordered viewer < operator < admin < owner; "admin" lets
// admin and owner through, "viewer" lets everyone authenticated through.
// Must be composed after RequireSession.
//
// Legacy: a role like "admin" with no matching tenant-scoped role
// falls back to the SimpleAuth global-role check. Lets existing tests
// that grant a global "admin" role continue to work; production
// deployments should rely on tenant-scoped roles instead.
func (s *Service) RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := UserFromContext(r.Context())
			if !ok || user == nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			// Tenant-scoped check first.
			if claim, ok := TenantFromContext(r.Context()); ok && claim.Role != "" {
				if roleAtLeast(claim.Role, role) {
					next.ServeHTTP(w, r)
					return
				}
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
				return
			}
			// Fallback: global SimpleAuth role.
			if user.HasRole(role) {
				next.ServeHTTP(w, r)
				return
			}
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		})
	}
}

// roleAtLeast reports whether have is at least as strong as want.
// Order: viewer < operator < admin < owner. Unknown role strings
// rank below viewer (treated as no-access).
func roleAtLeast(have, want string) bool {
	return rank(have) >= rank(want) && rank(have) >= 0
}
func rank(r string) int {
	switch r {
	case "viewer":
		return 0
	case "operator":
		return 1
	case "admin":
		return 2
	case "owner":
		return 3
	}
	return -1
}
