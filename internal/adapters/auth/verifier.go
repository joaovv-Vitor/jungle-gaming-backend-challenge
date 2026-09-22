package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
	"go.uber.org/fx"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/health"
)

var (
	ErrMissingToken = errors.New("bearer token is required")
	ErrInvalidToken = errors.New("bearer token is invalid")
	ErrForbidden    = errors.New("authenticated identity is not authorized")
)

type TokenVerifier interface {
	Verify(context.Context, string) (Identity, error)
}

type Verifier struct {
	verifier *oidc.IDTokenVerifier
}

func NewVerifier(
	lifecycle fx.Lifecycle,
	cfg config.Config,
	status *health.Status,
	logger *slog.Logger,
) *Verifier {
	keySet := oidc.NewRemoteKeySet(context.Background(), cfg.OIDCJWKSURL)
	verifier := &Verifier{verifier: oidc.NewVerifier(cfg.OIDCIssuer, keySet, &oidc.Config{
		ClientID:             cfg.OIDCAudience,
		SupportedSigningAlgs: []string{oidc.RS256},
	})}

	check := func(parent context.Context) error {
		ctx, cancel := context.WithTimeout(parent, cfg.OIDCPingTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.OIDCJWKSURL, nil)
		if err != nil {
			return err
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("OIDC JWKS endpoint returned %s", response.Status)
		}
		return nil
	}
	status.Register("oidc", check)
	lifecycle.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if err := check(ctx); err != nil {
			return fmt.Errorf("check OIDC provider: %w", err)
		}
		logger.Info("OIDC verifier started", "issuer", cfg.OIDCIssuer, "audience", cfg.OIDCAudience)
		return nil
	}})
	return verifier
}

func (v *Verifier) Verify(ctx context.Context, rawToken string) (Identity, error) {
	if rawToken == "" {
		return Identity{}, ErrMissingToken
	}
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	var claims struct {
		AuthorizedParty string `json:"azp"`
		ProviderID      string `json:"provider_id"`
		RealmAccess     struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
	}
	if err := token.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("%w: decode claims: %v", ErrInvalidToken, err)
	}
	if token.Subject == "" || claims.AuthorizedParty == "" {
		return Identity{}, fmt.Errorf("%w: subject and authorized party are required", ErrInvalidToken)
	}
	return NewIdentity(token.Subject, claims.AuthorizedParty, claims.ProviderID, claims.RealmAccess.Roles), nil
}

var _ TokenVerifier = (*Verifier)(nil)
