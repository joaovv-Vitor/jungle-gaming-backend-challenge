package health

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

type Check func(context.Context) error

type DependencyError struct {
	Name string
	Err  error
}

func (e *DependencyError) Error() string {
	return fmt.Sprintf("%s readiness failed: %v", e.Name, e.Err)
}
func (e *DependencyError) Unwrap() error { return e.Err }

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
	checks := make(map[string]Check, len(s.checks))
	for name, check := range s.checks {
		checks[name] = check
	}
	s.mu.RUnlock()
	for name, check := range checks {
		if err := check(ctx); err != nil {
			return &DependencyError{Name: name, Err: err}
		}
	}
	return nil
}
