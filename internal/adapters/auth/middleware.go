package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

type Middleware struct {
	verifier TokenVerifier
}

func NewMiddleware(verifier *Verifier) *Middleware {
	return &Middleware{verifier: verifier}
}

func NewMiddlewareWithVerifier(verifier TokenVerifier) *Middleware {
	return &Middleware{verifier: verifier}
}

func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawToken, err := bearerToken(r.Header.Get("Authorization"))
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED")
			return
		}
		identity, err := m.verifier.Verify(r.Context(), rawToken)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED")
			return
		}
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), identity)))
	})
}

func (m *Middleware) RequireRole(role string, next http.Handler) http.Handler {
	return m.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFromContext(r.Context())
		if !ok || !identity.HasRole(role) {
			writeAuthError(w, http.StatusForbidden, "FORBIDDEN")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func bearerToken(header string) (string, error) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", ErrMissingToken
	}
	return parts[1], nil
}

func writeAuthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}
