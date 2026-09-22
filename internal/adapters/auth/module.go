package auth

import "go.uber.org/fx"

var Module = fx.Module(
	"auth",
	fx.Provide(NewVerifier, NewMiddleware),
	fx.Invoke(func(*Verifier) {}),
)
