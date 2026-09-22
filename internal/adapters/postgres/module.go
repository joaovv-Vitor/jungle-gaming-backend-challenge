package postgres

import (
	"go.uber.org/fx"

	applicationingestion "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/ingestion"
	applicationoutbox "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/outbox"
	applicationreference "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/reference"
	applicationwagering "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wagering"
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
		NewInboxRepository,
		fx.Annotate(NewOutboxStore, fx.As(new(applicationoutbox.Store))),
		fx.Annotate(NewReferenceStore, fx.As(new(applicationreference.Store))),
		fx.Annotate(NewWalletStore, fx.As(new(applicationwallet.Store))),
		fx.Annotate(NewWagerStore, fx.As(new(applicationwagering.Store))),
		fx.Annotate(NewIngestionStore, fx.As(new(applicationingestion.Store))),
	),
	fx.Invoke(func(*UnitOfWork) {}),
)
