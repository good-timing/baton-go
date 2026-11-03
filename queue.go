package baton

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// queueSink holds events for a vendor's Sink and writes them from one
// goroutine, so a slow or failing Sink never slows the vendor's tool calls.
type queueSink struct {
	out Sink

	mu    sync.Mutex
	queue []Event
	// inFlight is the size of the batch being written. Whichever of drain
	// and close sets it to 0 first counts that batch.
	inFlight int
	closed   bool

	overflowWarned sync.Once
	lateWarned     sync.Once
	failWarned     sync.Once

	wake chan struct{}
	// stop carries close's context, which out.Close gets.
	stop      chan context.Context
	done      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	budget    time.Duration
}

func newQueueSink(out Sink) *queueSink {
	ctx, cancel := context.WithCancel(context.Background())
	s := &queueSink{
		out:    out,
		wake:   make(chan struct{}, 1),
		stop:   make(chan context.Context, 1),
		done:   make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
		budget: closeBudget,
	}
	go s.run()
	return s
}

func (s *queueSink) write(e Event) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		dropAfterCloseOnce(&s.lateWarned)
		return
	}
	full := len(s.queue) >= bufferCap
	if full {
		s.queue[0] = Event{}
		s.queue = s.queue[1:]
	}
	s.queue = append(s.queue, e)
	s.mu.Unlock()
	if full {
		processDrops.add(dropBufferFull, 1)
		warnOverflow(&s.overflowWarned)
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *queueSink) run() {
	defer close(s.done)
	for {
		select {
		case <-s.wake:
			s.drain()
		case ctx := <-s.stop:
			s.drain()
			if err := safeClose(ctx, s.out); err != nil {
				slog.Warn("baton: the sink failed to close", "error", err)
			}
			return
		}
	}
}

// drain writes what is queued, batchCap events at a time, until the queue is
// empty or close stops sending.
func (s *queueSink) drain() {
	for {
		var batch []Event
		s.mu.Lock()
		if s.ctx.Err() == nil {
			n := min(len(s.queue), batchCap)
			batch = slices.Clone(s.queue[:n])
			clear(s.queue[:n])
			s.queue = s.queue[n:]
			s.inFlight = n
		}
		s.mu.Unlock()
		if len(batch) == 0 {
			return
		}
		err := safeWrite(s.ctx, s.out, batch)
		s.mu.Lock()
		taken := s.inFlight
		s.inFlight = 0
		s.mu.Unlock()
		switch {
		case taken == 0:
		case err != nil && s.ctx.Err() != nil:
			processDrops.add(dropUnsentAtClose, lost(err, taken))
		case err != nil:
			processDrops.add(dropWriteFailed, lost(err, taken))
			s.failWarned.Do(func() {
				slog.Warn("baton: the sink failed to write events; they have been dropped and are not written again. Later failures are not logged",
					"error", err)
			})
		default:
			processDrops.logRecovery()
		}
	}
}

// close writes what is queued until closeGrace before ctx's deadline, or
// before s.budget when ctx has none, then cancels the write and counts what
// is left; it returns by that deadline. A second close waits for the first.
func (s *queueSink) close(ctx context.Context) {
	unsent := 0
	// Logged outside Do: a log handler that calls Close would wait on itself.
	defer func() {
		if unsent > 0 {
			slog.Warn("baton: closed with events not delivered", "unsent", unsent)
		}
	}()
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		ctx, cancel := withBudget(ctx, s.budget)
		defer cancel()
		deadline, _ := ctx.Deadline()
		grace := max(0, min(closeGrace, time.Until(deadline)/2))
		sending, stopSending := context.WithDeadline(ctx, deadline.Add(-grace))
		defer stopSending()
		s.stop <- ctx
		select {
		case <-s.done:
		case <-sending.Done():
		}
		s.cancel()
		select {
		case <-s.done:
		case <-ctx.Done():
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		unsent = len(s.queue) + s.inFlight
		s.queue, s.inFlight = nil, 0
		processDrops.add(dropUnsentAtClose, unsent)
	})
}

// withBudget is ctx, or ctx limited to budget when it has no deadline.
func withBudget(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, budget)
}

// countedError is a failed Write whose sink settled some of the batch's
// events itself: delivered them, or counted them under a reason it knew.
// The rest are lost.
type countedError struct {
	err     error
	settled int
}

func (e *countedError) Error() string { return e.err.Error() }
func (e *countedError) Unwrap() error { return e.err }

// lost is how many of n events a failed Write leaves to count.
func lost(err error, n int) int {
	var c *countedError
	if errors.As(err, &c) {
		return max(0, n-c.settled)
	}
	return n
}

// panicked is a Write or Close that panicked. Only the panic's type is kept:
// its value may hold an event's content.
type panicked struct{ value any }

func (p panicked) Error() string { return fmt.Sprintf("the sink panicked (%T)", p.value) }

func safeWrite(ctx context.Context, out Sink, events []Event) error {
	return catchPanic(func() error { return out.Write(ctx, events) })
}

func safeClose(ctx context.Context, out Sink) error {
	return catchPanic(func() error { return out.Close(ctx) })
}

func catchPanic(f func() error) (err error) {
	// A flag, not recover() != nil: under panicnil=1 a panic(nil) recovers as nil.
	returned := false
	defer func() {
		if !returned {
			err = panicked{recover()}
		}
	}()
	err = f()
	returned = true
	return err
}
