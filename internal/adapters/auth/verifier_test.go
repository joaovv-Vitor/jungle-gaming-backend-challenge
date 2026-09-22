package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

type staticKeySet struct {
	payload []byte
}

func (s staticKeySet) VerifySignature(context.Context, string) ([]byte, error) {
	return s.payload, nil
}

func TestVerifierRejectsExpiredToken(t *testing.T) {
	now := time.Now().UTC()
	claims, err := json.Marshal(map[string]any{
		"iss": "https://issuer.example", "sub": "subject-1", "aud": "wager-api",
		"iat": now.Add(-2 * time.Hour).Unix(), "exp": now.Add(-time.Hour).Unix(),
		"azp": "internal-service", "realm_access": map[string]any{"roles": []string{"internal"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test"}`))
	payload := base64.RawURLEncoding.EncodeToString(claims)
	verifier := &Verifier{verifier: oidc.NewVerifier("https://issuer.example", staticKeySet{payload: claims}, &oidc.Config{
		ClientID: "wager-api", SupportedSigningAlgs: []string{oidc.RS256},
	})}
	_, err = verifier.Verify(context.Background(), header+"."+payload+".signature")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify(expired) error = %v, want %v", err, ErrInvalidToken)
	}
}
