package sqsadapter

import (
	"context"
	"sync"

	application "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/outbox"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/config"
)

type OutboxSender struct {
	broker *Broker
	name   string
	mu     sync.RWMutex
	url    string
}

func NewOutboxSender(broker *Broker, cfg config.Config) *OutboxSender {
	return &OutboxSender{broker: broker, name: cfg.SQSOutputQueue}
}

func (s *OutboxSender) Check(ctx context.Context) error {
	url, err := s.broker.QueueURL(ctx, s.name)
	if err != nil {
		return err
	}
	if err := s.broker.Ping(ctx, url); err != nil {
		return err
	}
	s.mu.Lock()
	s.url = url
	s.mu.Unlock()
	return nil
}

func (s *OutboxSender) Publish(ctx context.Context, event application.Event) error {
	s.mu.RLock()
	url := s.url
	s.mu.RUnlock()
	if url == "" {
		if err := s.Check(ctx); err != nil {
			return err
		}
		s.mu.RLock()
		url = s.url
		s.mu.RUnlock()
	}
	return s.broker.Send(ctx, url, event.ID, event.GroupID, event.Payload)
}

var _ application.Publisher = (*OutboxSender)(nil)
