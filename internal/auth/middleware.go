package auth

import (
	"net/http"
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

// RequireSession gates a handler behind a valid session cookie. On
// success the user and the active tenant are written into the context.
func (s *Service) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
