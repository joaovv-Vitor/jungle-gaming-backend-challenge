package sqsadapter

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.uber.org/fx"

	application "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/ingestion"
	applicationwagering "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wagering"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/health"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/metrics"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/safeerror"
)

type messageBroker interface {
	QueueURL(context.Context, string) (string, error)
	Ping(context.Context, string) error
	ApproximateMessages(context.Context, string) (int64, error)
	Receive(context.Context, string, int32, int32, int32) ([]Message, error)
	Delete(context.Context, string, string) error
	Release(context.Context, string, string) error
	ChangeVisibility(context.Context, string, string, int32) error
}

type messageIngester interface {
	Consume(context.Context, string, application.Message) (application.Result, error)
}

type Consumer struct {
	broker          messageBroker
	ingestion       messageIngester
	instrumentation *metrics.Metrics
	cfg             config.Config
	logger          *slog.Logger

	mu         sync.RWMutex
	queueURL   string
	dlqURL     string
	pollCancel context.CancelFunc
	workCancel context.CancelFunc
	loopDone   chan struct{}
	workers    sync.WaitGroup
	semaphore  chan struct{}
}

func NewConsumer(
	lifecycle fx.Lifecycle,
	broker *Broker,
	ingestion *application.Service,
	cfg config.Config,
	status *health.Status,
	logger *slog.Logger,
	instrumentation *metrics.Metrics,
) *Consumer {
	consumer := newConsumer(broker, ingestion, cfg, logger, instrumentation)
	status.Register("sqs", consumer.check)
	lifecycle.Append(fx.Hook{OnStart: consumer.start, OnStop: consumer.stop})
	return consumer
}

func newConsumer(
	broker messageBroker,
	ingestion messageIngester,
	cfg config.Config,
	logger *slog.Logger,
	instrumentation *metrics.Metrics,
) *Consumer {
	return &Consumer{
		broker: broker, ingestion: ingestion, cfg: cfg, logger: logger,
		instrumentation: instrumentation,
		semaphore:       make(chan struct{}, cfg.SQSConcurrency),
	}
}

func (c *Consumer) check(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, c.cfg.SQSPingTimeout)
	defer cancel()
	queueURL := c.currentQueueURL()
	if queueURL == "" {
		var err error
		queueURL, err = c.broker.QueueURL(ctx, c.cfg.SQSInputQueue)
		if err != nil {
			return err
		}
	}
	if err := c.broker.Ping(ctx, queueURL); err != nil {
		return err
	}
	if c.cfg.SQSDLQQueue == "" {
		return nil
	}
	dlqURL := c.currentDLQURL()
	if dlqURL == "" {
		var err error
		dlqURL, err = c.broker.QueueURL(ctx, c.cfg.SQSDLQQueue)
		if err != nil {
			return err
		}
	}
	return c.broker.Ping(ctx, dlqURL)
}

func (c *Consumer) start(ctx context.Context) error {
	queueURL, err := c.broker.QueueURL(ctx, c.cfg.SQSInputQueue)
	if err != nil {
		return err
	}
	if err := c.broker.Ping(ctx, queueURL); err != nil {
		return err
	}
	var dlqURL string
	if c.cfg.SQSDLQQueue != "" {
		dlqURL, err = c.broker.QueueURL(ctx, c.cfg.SQSDLQQueue)
		if err != nil {
			return err
		}
		if err := c.broker.Ping(ctx, dlqURL); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.queueURL = queueURL
	c.dlqURL = dlqURL
	pollCtx, pollCancel := context.WithCancel(context.Background())
	workCtx, workCancel := context.WithCancel(context.Background())
	c.pollCancel = pollCancel
	c.workCancel = workCancel
	c.loopDone = make(chan struct{})
	c.mu.Unlock()
	c.logger.Info("SQS consumer started", "queue", c.cfg.SQSInputQueue, "concurrency", c.cfg.SQSConcurrency)
	go c.run(pollCtx, workCtx)
	return nil
}

func (c *Consumer) run(pollCtx, workCtx context.Context) {
	defer close(c.loopDone)
	var lastDLQCheck time.Time
	for {
		if c.currentDLQURL() != "" && time.Since(lastDLQCheck) >= 15*time.Second {
			c.observeDLQ(pollCtx)
			lastDLQCheck = time.Now()
		}
		batch, ok := c.availableBatch(pollCtx)
		if !ok {
			return
		}
		messages, err := c.broker.Receive(
			pollCtx, c.currentQueueURL(), batch,
			int32(c.cfg.SQSLongPoll/time.Second), int32(c.cfg.SQSVisibility/time.Second),
		)
		if err != nil {
			if errors.Is(pollCtx.Err(), context.Canceled) {
				return
			}
			c.logger.Error("SQS receive failed", "reason", safeerror.Reason(err))
			timer := time.NewTimer(time.Second)
			select {
			case <-pollCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		for _, message := range messages {
			c.instrumentation.RecordSQSReceived(message.ReceiveCount)
			select {
			case c.semaphore <- struct{}{}:
			case <-pollCtx.Done():
				return
			}
			c.workers.Add(1)
			go func(message Message) {
				defer c.workers.Done()
				defer func() { <-c.semaphore }()
				c.handle(workCtx, message)
			}(message)
		}
	}
}

func (c *Consumer) observeDLQ(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, c.cfg.SQSPingTimeout)
	defer cancel()
	count, err := c.broker.ApproximateMessages(ctx, c.currentDLQURL())
	if err != nil {
		if parent.Err() == nil {
			c.logger.Error("SQS DLQ depth query failed", "reason", safeerror.Reason(err))
		}
		return
	}
	c.instrumentation.SetDLQDepth(count)
}

func (c *Consumer) availableBatch(ctx context.Context) (int32, bool) {
	available := cap(c.semaphore) - len(c.semaphore)
	if available == 0 {
		select {
		case c.semaphore <- struct{}{}:
			<-c.semaphore
		case <-ctx.Done():
			return 0, false
		}
		available = cap(c.semaphore) - len(c.semaphore)
	}
	batch := int32(available)
	if batch > c.cfg.SQSReceiveBatch {
		batch = c.cfg.SQSReceiveBatch
	}
	return batch, true
}

func (c *Consumer) handle(parent context.Context, received Message) {
	startedAt := time.Now()
	metricResult := metrics.SQSResultProcessingError
	defer func() {
		c.instrumentation.ObserveSQSProcessing(metricResult, time.Since(startedAt))
	}()
	message, err := application.DecodeMessage([]byte(received.Body))
	if err != nil {
		metricResult = metrics.SQSResultInvalid
		c.logger.Warn("invalid SQS message left for redrive", "brokerMessageId", received.ID, "reason", "invalid_message")
		return
	}
	logger := c.logger.With("brokerMessageId", received.ID, "messageId", message.ID,
		"correlationId", message.Input.CorrelationID, "providerId", message.Input.ProviderID,
		"walletId", message.Input.WalletID, "externalTransactionId", message.Input.ExternalTransactionID)
	ctx, cancel := context.WithTimeout(parent, c.cfg.SQSProcessing)
	defer cancel()
	result, err := c.ingestion.Consume(ctx, c.cfg.SQSConsumerName, message)
	if err != nil {
		metricCode := "error"
		switch {
		case errors.Is(err, application.ErrMessageConflict):
			metricCode = "message_conflict"
		case errors.Is(err, applicationwagering.ErrIdempotencyConflict):
			metricCode = "idempotency_conflict"
		case errors.Is(err, applicationwagering.ErrExternalIDConflict):
			metricCode = "external_id_conflict"
		case errors.Is(err, applicationwagering.ErrConcurrentWrite), errors.Is(err, applicationwagering.ErrIdentityRace):
			metricCode = "concurrent_write"
		}
		c.instrumentation.RecordWagerError("sqs", metricCode)
		logger.Error("SQS message processing failed", "reason", metricCode, "cause", safeerror.Reason(err))
		if ctx.Err() != nil {
			c.release(received, logger)
		} else if retryableProcessingError(err) {
			c.deferRetry(received, logger)
		}
		return
	}
	c.instrumentation.RecordWager("sqs", string(result.WagerResult.Transaction.Kind()),
		string(result.WagerResult.Transaction.Status()), string(result.WagerResult.Transaction.FailureCode()),
		result.Duplicate || result.WagerResult.Replay, time.Since(startedAt))
	deleteCtx, deleteCancel := context.WithTimeout(context.Background(), c.cfg.SQSPingTimeout)
	defer deleteCancel()
	if err := c.broker.Delete(deleteCtx, c.currentQueueURL(), received.ReceiptHandle); err != nil {
		metricResult = metrics.SQSResultDeleteError
		logger.Error("SQS message committed but delete failed", "transactionId", result.WagerResult.Transaction.ID(), "reason", safeerror.Reason(err))
		return
	}
	metricResult = metrics.SQSResultProcessed
	if result.Duplicate || result.WagerResult.Replay {
		metricResult = metrics.SQSResultReplay
	}
	logger.Info("SQS message processed", "transactionId", result.WagerResult.Transaction.ID(),
		"duplicate", result.Duplicate || result.WagerResult.Replay)
}

func retryableProcessingError(err error) bool {
	return !errors.Is(err, application.ErrMessageConflict) &&
		!errors.Is(err, applicationwagering.ErrIdempotencyConflict) &&
		!errors.Is(err, applicationwagering.ErrExternalIDConflict) &&
		!errors.Is(err, applicationwagering.ErrInvalidInput) &&
		!errors.Is(err, applicationwagering.ErrWalletMismatch) &&
		!errors.Is(err, applicationwagering.ErrWalletNotFound)
}

func retryVisibilitySeconds(receiveCount int) int32 {
	seconds := int32(5)
	for attempt := 1; attempt < receiveCount && seconds < 300; attempt++ {
		seconds *= 2
		if seconds > 300 {
			seconds = 300
		}
	}
	return seconds
}

func (c *Consumer) deferRetry(received Message, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.SQSPingTimeout)
	defer cancel()
	seconds := retryVisibilitySeconds(received.ReceiveCount)
	if err := c.broker.ChangeVisibility(ctx, c.currentQueueURL(), received.ReceiptHandle, seconds); err != nil {
		logger.Warn("failed to defer SQS message retry", "reason", safeerror.Reason(err))
	}
}

func (c *Consumer) release(received Message, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.SQSPingTimeout)
	defer cancel()
	if err := c.broker.Release(ctx, c.currentQueueURL(), received.ReceiptHandle); err != nil {
		logger.Warn("failed to release SQS message visibility", "reason", safeerror.Reason(err))
	}
}

func (c *Consumer) stop(ctx context.Context) error {
	started := time.Now()
	defer func() { c.instrumentation.ObserveShutdown("sqs", time.Since(started)) }()
	c.mu.RLock()
	pollCancel, workCancel, loopDone := c.pollCancel, c.workCancel, c.loopDone
	c.mu.RUnlock()
	if pollCancel == nil {
		return nil
	}
	pollCancel()
	select {
	case <-loopDone:
	case <-ctx.Done():
		workCancel()
		return ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		c.workers.Wait()
		close(done)
	}()
	shutdownTimer := time.NewTimer(c.cfg.SQSShutdown)
	defer shutdownTimer.Stop()
	select {
	case <-done:
		workCancel()
		c.logger.Info("SQS consumer stopped")
		return nil
	case <-shutdownTimer.C:
		workCancel()
		<-done
		return errors.New("SQS consumer shutdown timeout")
	case <-ctx.Done():
		workCancel()
		<-done
		return ctx.Err()
	}
}

func (c *Consumer) currentQueueURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.queueURL
}

func (c *Consumer) currentDLQURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dlqURL
}
