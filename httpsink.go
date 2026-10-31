package baton

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// The numbers are baton-sdk's HttpSink defaults (SPEC §11.2 rules 3, 4, 7).
const (
	bufferCap        = 1000
	requestTimeout   = time.Second
	maxRetries       = 3
	backoffBase      = 100 * time.Millisecond
	backoffMax       = 5 * time.Second
	breakerThreshold = 5
	breakerReset     = 30 * time.Second
	closeBudget      = 3 * time.Second
	refusalBodyCap   = 500
)

type outcome int

const (
	sent outcome = iota
	refused
	unencodable
	transient
)

// httpSink posts one event per request from a single goroutine, so a slow or
// broken console never slows the vendor's tool calls.
type httpSink struct {
	url    string
	auth   string
	client *http.Client

	mu       sync.Mutex
	queue    []Event
	inFlight bool
	closed   bool

	overflowWarned sync.Once
	lateWarned     sync.Once

	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once

	failures    int
	openedAt    time.Time
	lastProblem string
	backoff     time.Duration
	reset       time.Duration
	retryAfter  time.Duration
	budget      time.Duration
}

func newHTTPSink(origin, key string) *httpSink {
	ctx, cancel := context.WithCancel(context.Background())
	s := &httpSink{
		url:  origin + "/v0/events",
		auth: "Bearer " + key,
		client: &http.Client{
			Timeout: requestTimeout,
			// Following a redirect turns the POST into a GET without the event.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		wake:       make(chan struct{}, 1),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
		backoff:    backoffBase,
		reset:      breakerReset,
		retryAfter: backoffMax,
		budget:     closeBudget,
	}
	go s.run()
	return s
}

func (s *httpSink) write(e Event) {
	s.mu.Lock()
	closed := s.closed
	dropped := !closed && s.push(e)
	s.mu.Unlock()
	if closed {
		s.lateWarned.Do(func() {
			slog.Warn("baton: an event was recorded after Close and has been dropped; later ones are dropped silently")
		})
		return
	}
	if dropped {
		s.warnOverflow()
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// push adds e to the back of the queue, dropping the oldest when full, and
// reports whether it dropped one. The caller holds s.mu.
func (s *httpSink) push(e Event) bool {
	full := len(s.queue) >= bufferCap
	if full {
		s.popFront()
	}
	s.queue = append(s.queue, e)
	return full
}

// putBack returns e, which is older than everything queued, to the front.
// When the queue is full, e is the oldest and is the one dropped.
func (s *httpSink) putBack(e Event) bool {
	if len(s.queue) >= bufferCap {
		return true
	}
	s.queue = slices.Insert(s.queue, 0, e)
	return false
}

// popFront clears the slot so the popped event's payload can be collected.
func (s *httpSink) popFront() Event {
	e := s.queue[0]
	s.queue[0] = Event{}
	s.queue = s.queue[1:]
	return e
}

func (s *httpSink) warnOverflow() {
	s.overflowWarned.Do(func() {
		slog.Warn("baton: the event buffer is full; the oldest events are dropped. Further overflows are silent",
			"cap", bufferCap)
	})
}

func (s *httpSink) run() {
	defer close(s.done)
	var retry <-chan time.Time
	for {
		select {
		case <-s.wake:
		case <-retry:
		case <-s.stop:
			s.drain()
			return
		}
		retry = nil
		if !s.drain() {
			retry = time.After(s.retryAfter)
		}
	}
}

// drain sends until the queue is empty, and reports whether it got there.
func (s *httpSink) drain() bool {
	for s.breakerAllows() {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return true
		}
		e := s.popFront()
		s.inFlight = true
		s.mu.Unlock()

		result := s.send(e)

		s.mu.Lock()
		s.inFlight = false
		dropped := result == transient && s.putBack(e)
		s.mu.Unlock()
		if dropped {
			s.warnOverflow()
		}

		switch result {
		case sent, refused:
			s.failures = 0
			s.openedAt = time.Time{}
		case transient:
			s.failures++
			if s.failures >= breakerThreshold {
				s.openedAt = time.Now()
				slog.Warn("baton: the console is failing; events are kept and sending pauses",
					"pause", s.reset, "last_problem", s.lastProblem)
			}
			return false
		}
	}
	return false
}

func (s *httpSink) breakerAllows() bool {
	return s.openedAt.IsZero() || time.Since(s.openedAt) >= s.reset
}

func (s *httpSink) send(e Event) outcome {
	body, err := json.Marshal(e)
	if err != nil {
		slog.Warn("baton: an event could not be encoded and has been dropped; the events after it are still sent",
			"event_type", e.EventType, "error_type", fmt.Sprintf("%T", err))
		return unencodable
	}
	for attempt := 0; ; attempt++ {
		if result := s.post(body); result != transient {
			return result
		}
		if attempt == maxRetries {
			return transient
		}
		select {
		case <-time.After(min(s.backoff<<attempt, backoffMax)):
		case <-s.ctx.Done():
			return transient
		}
	}
}

func (s *httpSink) post(body []byte) outcome {
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return transient
	}
	req.Header.Set("Authorization", s.auth)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		s.lastProblem = err.Error()
		return transient
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, refusalBodyCap))
	// Reading to the end lets the connection be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	switch status := resp.StatusCode; {
	case status >= 200 && status < 300:
		return sent
	case status >= 400 && status < 500 && status != http.StatusTooManyRequests:
		slog.Warn("baton: the collector REFUSED an event and it has been dropped; nothing retries a 4xx",
			"status", status, "body", strings.ToValidUTF8(string(detail), "?"))
		return refused
	}
	s.lastProblem = resp.Status
	return transient
}

// close sends what is queued until ctx's deadline, or for s.budget when ctx
// has none. A second close waits for the first. When the console is failing,
// close does not try again.
func (s *httpSink) close(ctx context.Context) {
	unsent := 0
	// The warning is logged outside Do: a log handler that calls Close would
	// otherwise wait on itself.
	defer func() {
		if unsent > 0 {
			slog.Warn("baton: closed with events unsent", "unsent", unsent)
		}
	}()
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		if ctx == nil {
			ctx = context.Background()
		}
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, s.budget)
			defer cancel()
		}
		close(s.stop)
		select {
		case <-s.done:
		case <-ctx.Done():
		}
		s.cancel()
		s.client.CloseIdleConnections()
		s.mu.Lock()
		unsent = len(s.queue)
		if s.inFlight {
			unsent++
		}
		s.mu.Unlock()
	})
}
