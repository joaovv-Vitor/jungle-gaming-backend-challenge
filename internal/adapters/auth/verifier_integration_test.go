//go:build integration

package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

func TestKeycloakIssuedTokenIsRejectedAfterExpiry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	issuer := envOr("APP_OIDC_ISSUER", "http://localhost:8081/realms/wagering")
	baseURL, _, ok := strings.Cut(issuer, "/realms/")
	if !ok {
		t.Fatalf("OIDC issuer has no /realms/ segment: %s", issuer)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	adminToken := keycloakToken(t, ctx, client, baseURL+"/realms/master", url.Values{
		"grant_type": {"password"}, "client_id": {"admin-cli"},
		"username": {envOr("KEYCLOAK_ADMIN", "admin")},
		"password": {envOr("KEYCLOAK_ADMIN_PASSWORD", "admin_local")},
	})
	realm := fmt.Sprintf("wager-expiry-%d", time.Now().UnixNano())
	representation := map[string]any{
		"realm": realm, "enabled": true, "accessTokenLifespan": 2,
		"clients": []any{
			map[string]any{"clientId": "wager-api", "enabled": true, "bearerOnly": true, "protocol": "openid-connect"},
			map[string]any{
				"clientId": "expiry-client", "enabled": true, "protocol": "openid-connect",
				"publicClient": false, "serviceAccountsEnabled": true, "secret": "expiry-client-local",
				"protocolMappers": []any{map[string]any{
					"name": "wager-api-audience", "protocol": "openid-connect",
					"protocolMapper": "oidc-audience-mapper", "consentRequired": false,
					"config": map[string]string{"included.client.audience": "wager-api", "access.token.claim": "true"},
				}},
			},
		},
	}
	body, err := json.Marshal(representation)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/admin/realms", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+adminToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create temporary Keycloak realm: status %d", response.StatusCode)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		request, err := http.NewRequestWithContext(cleanupCtx, http.MethodDelete, baseURL+"/admin/realms/"+realm, nil)
		if err != nil {
			t.Error(err)
			return
		}
		request.Header.Set("Authorization", "Bearer "+adminToken)
		response, err := client.Do(request)
		if err != nil {
			t.Errorf("delete temporary Keycloak realm: %v", err)
			return
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Errorf("delete temporary Keycloak realm: status %d", response.StatusCode)
		}
	})

	testIssuer := baseURL + "/realms/" + realm
	rawToken := keycloakToken(t, ctx, client, testIssuer, url.Values{
		"grant_type": {"client_credentials"}, "client_id": {"expiry-client"},
		"client_secret": {"expiry-client-local"},
	})
	verifier := &Verifier{verifier: oidc.NewVerifier(testIssuer,
		oidc.NewRemoteKeySet(ctx, testIssuer+"/protocol/openid-connect/certs"),
		&oidc.Config{ClientID: "wager-api", SupportedSigningAlgs: []string{oidc.RS256}})}
	identity, err := verifier.Verify(ctx, rawToken)
	if err != nil || identity.ClientID != "expiry-client" {
		t.Fatalf("fresh Keycloak token: identity=%+v error=%v", identity, err)
	}
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		t.Fatal("Keycloak returned a malformed JWT")
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		ExpiresAt int64 `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(time.Unix(claims.ExpiresAt, 0)); remaining <= 0 || remaining > 5*time.Second {
		t.Fatalf("temporary realm token lifetime = %s, want 1–5 seconds", remaining)
	}
	wait := time.Until(time.Unix(claims.ExpiresAt, 0).Add(time.Second))
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := verifier.Verify(ctx, rawToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired Keycloak token error = %v, want %v", err, ErrInvalidToken)
	}
}

func keycloakToken(t *testing.T, ctx context.Context, client *http.Client, issuer string, form url.Values) string {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
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
		t.Fatalf("Keycloak token endpoint returned status %d", response.StatusCode)
	}
	return tokenResponse.AccessToken
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
