package auth

import "context"

type Identity struct {
	Subject    string
	ClientID   string
	ProviderID string
	roles      map[string]struct{}
}

func NewIdentity(subject, clientID, providerID string, roles []string) Identity {
	identity := Identity{
		Subject: subject, ClientID: clientID, ProviderID: providerID,
		roles: make(map[string]struct{}, len(roles)),
	}
	for _, role := range roles {
		identity.roles[role] = struct{}{}
	}
	return identity
}

func (i Identity) HasRole(role string) bool {
	_, ok := i.roles[role]
	return ok
}

type identityContextKey struct{}

func withIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, identity)
}

func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(Identity)
	return identity, ok
}
