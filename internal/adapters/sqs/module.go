package sqsadapter

import "go.uber.org/fx"

var Module = fx.Module(
	"sqs-consumer",
	fx.Provide(NewBroker, NewConsumer),
	fx.Invoke(func(*Consumer) {}),
)
