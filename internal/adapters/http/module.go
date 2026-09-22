package httpadapter

import "go.uber.org/fx"

var Module = fx.Module(
	"http",
	fx.Provide(newMux, newServer),
	fx.Invoke(registerLifecycle),
)
