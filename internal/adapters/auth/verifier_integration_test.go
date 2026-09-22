//go:build integration

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

func TestKeycloakClientCredentialsToken(t *testing.T) {
	issuer := envOr("APP_OIDC_ISSUER", "http://localhost:8081/realms/wagering")
	jwksURL := envOr("APP_OIDC_JWKS_URL", issuer+"/protocol/openid-connect/certs")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"provider-a"},
		"client_secret": {"provider-a-local"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&tokenResponse); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || tokenResponse.AccessToken == "" {
		t.Fatalf("token endpoint returned %s", response.Status)
	}

	verifier := &Verifier{verifier: oidc.NewVerifier(issuer, oidc.NewRemoteKeySet(ctx, jwksURL), &oidc.Config{
		ClientID: "wager-api", SupportedSigningAlgs: []string{oidc.RS256},
	})}
	identity, err := verifier.Verify(ctx, tokenResponse.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ClientID != "provider-a" || identity.ProviderID != "provider-a" || !identity.HasRole("provider") {
		t.Fatalf("identity = %+v", identity)
	}
	if _, err := verifier.Verify(ctx, tokenResponse.AccessToken+"tampered"); err == nil {
		t.Fatal("tampered token was accepted")
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
