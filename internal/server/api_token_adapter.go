package server

import (
	"context"

	"mqConnector/internal/auth"
	"mqConnector/internal/storage"
)

// apiTokenAdapter bridges the storage.APITokenRepo to the
// auth.APITokenLookup interface. Lives in the server package so the
// auth package stays storage-free and the storage package stays
// auth-free — neither has to know the other exists.
type apiTokenAdapter struct{ repo *storage.APITokenRepo }

// Lookup satisfies auth.APITokenLookup. Translates storage.ErrNotFound
// into a nil-with-error so the auth middleware treats it as
// "fall through to cookie auth" rather than "explode".
func (a apiTokenAdapter) Lookup(ctx context.Context, secret string) (*auth.APITokenInfo, error) {
	t, err := a.repo.Lookup(ctx, secret)
	if err != nil {
		return nil, err
	}
	return &auth.APITokenInfo{
		ID:       t.ID,
		TenantID: t.TenantID,
		UserSub:  t.UserSub,
		Name:     t.Name,
		Role:     t.Role,
	}, nil
}
