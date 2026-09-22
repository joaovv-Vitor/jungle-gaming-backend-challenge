package sqsadapter

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.uber.org/fx"

	application "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/ingestion"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/health"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/metrics"
)

type messageBroker interface {
	QueueURL(context.Context, string) (string, error)
	Ping(context.Context, string) error
	Receive(context.Context, string, int32, int32, int32) ([]Message, error)
	Delete(context.Context, string, string) error
	Release(context.Context, string, string) error
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
	return c.broker.Ping(ctx, queueURL)
}

func (c *Consumer) start(ctx context.Context) error {
	queueURL, err := c.broker.QueueURL(ctx, c.cfg.SQSInputQueue)
	if err != nil {
		return err
	}
	if err := c.broker.Ping(ctx, queueURL); err != nil {
		return err
	}
	c.mu.Lock()
	c.queueURL = queueURL
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
	for {
		messages, err := c.broker.Receive(
			pollCtx, c.currentQueueURL(), c.cfg.SQSReceiveBatch,
			int32(c.cfg.SQSLongPoll/time.Second), int32(c.cfg.SQSVisibility/time.Second),
		)
		if err != nil {
			if errors.Is(pollCtx.Err(), context.Canceled) {
				return
			}
			c.logger.Error("SQS receive failed", "error", err)
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

func (c *Consumer) handle(parent context.Context, received Message) {
	startedAt := time.Now()
	metricResult := metrics.SQSResultProcessingError
	defer func() {
		c.instrumentation.ObserveSQSProcessing(metricResult, time.Since(startedAt))
	}()
	message, err := application.DecodeMessage([]byte(received.Body))
	if err != nil {
		metricResult = metrics.SQSResultInvalid
		c.logger.Warn("invalid SQS message left for redrive", "brokerMessageId", received.ID, "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(parent, c.cfg.SQSProcessing)
	defer cancel()
	result, err := c.ingestion.Consume(ctx, c.cfg.SQSConsumerName, message)
	if err != nil {
		c.logger.Error("SQS message processing failed", "brokerMessageId", received.ID, "messageId", message.ID, "error", err)
		if ctx.Err() != nil {
			c.release(received)
		}
		return
	}
	deleteCtx, deleteCancel := context.WithTimeout(context.Background(), c.cfg.SQSPingTimeout)
	defer deleteCancel()
	if err := c.broker.Delete(deleteCtx, c.currentQueueURL(), received.ReceiptHandle); err != nil {
		metricResult = metrics.SQSResultDeleteError
		c.logger.Error("SQS message committed but delete failed", "brokerMessageId", received.ID, "messageId", message.ID, "transactionId", result.WagerResult.Transaction.ID(), "error", err)
		return
	}
	metricResult = metrics.SQSResultProcessed
	if result.Duplicate || result.WagerResult.Replay {
		metricResult = metrics.SQSResultReplay
	}
	c.logger.Info("SQS message processed", "brokerMessageId", received.ID, "messageId", message.ID, "transactionId", result.WagerResult.Transaction.ID(), "duplicate", result.Duplicate || result.WagerResult.Replay)
}

func (c *Consumer) release(received Message) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.SQSPingTimeout)
	defer cancel()
	if err := c.broker.Release(ctx, c.currentQueueURL(), received.ReceiptHandle); err != nil {
		c.logger.Warn("failed to release SQS message visibility", "brokerMessageId", received.ID, "error", err)
	}
}

func (c *Consumer) stop(ctx context.Context) error {
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
