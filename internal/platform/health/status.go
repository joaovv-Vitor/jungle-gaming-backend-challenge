package health

import "sync/atomic"

type Status struct {
	ready atomic.Bool
}

func New() *Status {
	return &Status{}
}

func (s *Status) SetReady(ready bool) {
	s.ready.Store(ready)
}

func (s *Status) Ready() bool {
	return s.ready.Load()
}
