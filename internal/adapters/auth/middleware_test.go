package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeVerifier struct {
	identity Identity
	err      error
}

func (f fakeVerifier) Verify(context.Context, string) (Identity, error) {
	return f.identity, f.err
}

func TestAuthenticateRejectsMissingAndInvalidTokens(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler must not be called")
	})

	for _, test := range []struct {
		name       string
		header     string
		verifier   TokenVerifier
		wantStatus int
	}{
		{name: "missing", verifier: fakeVerifier{}, wantStatus: http.StatusUnauthorized},
		{name: "malformed", header: "Basic abc", verifier: fakeVerifier{}, wantStatus: http.StatusUnauthorized},
		{name: "invalid", header: "Bearer invalid", verifier: fakeVerifier{err: ErrInvalidToken}, wantStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/protected", nil)
			request.Header.Set("Authorization", test.header)
			NewMiddlewareWithVerifier(test.verifier).Authenticate(next).ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
		})
	}
}

func TestRequireRoleAuthorizesAndPropagatesIdentity(t *testing.T) {
	identity := NewIdentity("subject-1", "internal-service", "", []string{"internal"})
	middleware := NewMiddlewareWithVerifier(fakeVerifier{identity: identity})
	handler := middleware.RequireRole("internal", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := IdentityFromContext(r.Context())
		if !ok || got.Subject != identity.Subject || got.ClientID != identity.ClientID {
			t.Fatalf("identity = %+v/%v, want %+v", got, ok, identity)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer valid")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestRequireRoleRejectsAuthenticatedIdentityWithoutRole(t *testing.T) {
	identity := NewIdentity("subject-1", "provider-a", "provider-a", []string{"provider"})
	middleware := NewMiddlewareWithVerifier(fakeVerifier{identity: identity})
	handler := middleware.RequireRole("internal", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler must not be called")
	}))
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer valid")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestVerifierErrorsRemainClassifiable(t *testing.T) {
	err := errors.Join(ErrInvalidToken, errors.New("signature"))
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatal("invalid token error is not classifiable")
	}
}
