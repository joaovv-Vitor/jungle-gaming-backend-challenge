package postgres

import "go.uber.org/fx"

var Module = fx.Module(
	"postgres",
	fx.Provide(NewPool, NewUnitOfWork, NewWalletRepository, NewWagerRepository, NewLedgerRepository),
	fx.Invoke(func(*UnitOfWork) {}),
)
