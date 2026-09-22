package health

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

type Check func(context.Context) error

type Status struct {
	ready  atomic.Bool
	mu     sync.RWMutex
	checks map[string]Check
}

func New() *Status {
	return &Status{checks: make(map[string]Check)}
}

func (s *Status) SetReady(ready bool) {
	s.ready.Store(ready)
}

func (s *Status) Register(name string, check Check) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks[name] = check
}

func (s *Status) Ready(ctx context.Context) error {
	if !s.ready.Load() {
		return errors.New("service is not accepting traffic")
	}
	s.mu.RLock()
	checks := make([]Check, 0, len(s.checks))
	for _, check := range s.checks {
		checks = append(checks, check)
	}
	s.mu.RUnlock()
	for _, check := range checks {
		if err := check(ctx); err != nil {
			return err
		}
	}
	return nil
}
