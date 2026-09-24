package sqsadapter

import (
	"go.uber.org/fx"

	applicationoutbox "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/outbox"
)

var Module = fx.Module(
	"sqs-consumer",
	fx.Provide(NewBroker, NewConsumer, NewOutboxWorker,
		fx.Annotate(NewOutboxSender, fx.As(new(applicationoutbox.Publisher)))),
	fx.Invoke(func(*Consumer, *OutboxWorker) {}),
)
