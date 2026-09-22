package referenceadapter

import "go.uber.org/fx"

var Module = fx.Module("reference-worker", fx.Provide(NewWorker), fx.Invoke(func(*Worker) {}))
