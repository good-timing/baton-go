package baton

import (
	"context"
	"slices"
	"sync"
)

type memorySink struct {
	mu     sync.Mutex
	events []Event
}

func (s *memorySink) write(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *memorySink) close(context.Context) error { return nil }

func (s *memorySink) all() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}
