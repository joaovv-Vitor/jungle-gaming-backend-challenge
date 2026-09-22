package postgres

import (
	"go.uber.org/fx"

	applicationwallet "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wallet"
)

var Module = fx.Module(
	"postgres",
	fx.Provide(
		NewPool,
		NewUnitOfWork,
		NewWalletRepository,
		NewWagerRepository,
		NewLedgerRepository,
		NewOutboxRepository,
		fx.Annotate(NewWalletStore, fx.As(new(applicationwallet.Store))),
	),
	fx.Invoke(func(*UnitOfWork) {}),
)
