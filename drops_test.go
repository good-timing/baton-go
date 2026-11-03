package baton

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// freshDrops starts a test from zero: the counts are one per process.
func freshDrops(t *testing.T) {
	t.Helper()
	processDrops.mu.Lock()
	processDrops.total, processDrops.sinceDelivery = map[dropReason]int{}, map[dropReason]int{}
	processDrops.lastRecoveryLog = time.Time{}
	processDrops.mu.Unlock()
}

func TestADroppedEventIsCountedOnceUnderItsReason(t *testing.T) {
	scenarios := map[string]struct {
		reason dropReason
		run    func(t *testing.T)
	}{
		"buffer_full": {dropBufferFull, func(t *testing.T) {
			s := &httpSink{wake: make(chan struct{}, 1)}
			for n := range bufferCap + 1 {
				s.write(testEvent(n))
			}
		}},
		"put_back_into_a_full_buffer": {dropBufferFull, func(t *testing.T) {
			s := &httpSink{}
			for n := range bufferCap {
				s.push(testEvent(n + 1))
			}
			s.putBack([]*queued{{Event: testEvent(0)}}, false)
		}},
		"refused": {dropRefused, func(t *testing.T) {
			s := fastSink(t, newFakeConsole(t, func(int) int { return http.StatusBadRequest }))
			s.write(testEvent(1))
			s.close(context.Background())
		}},
		"encode_failed": {dropEncodeFailed, func(t *testing.T) {
			s := fastSink(t, newFakeConsole(t, nil))
			s.write(Event{EventType: "tool_call_start", Payload: map[string]any{"bad": func() {}}})
			s.close(context.Background())
		}},
		"panic_while_building": {dropEncodeFailed, func(t *testing.T) {
			guard("tool_call_start", func() { panic("scrubber") })
		}},
		"after_close": {dropAfterClose, func(t *testing.T) {
			s := fastSink(t, newFakeConsole(t, nil))
			s.close(context.Background())
			s.write(testEvent(1))
		}},
		"unsent_at_close": {dropUnsentAtClose, func(t *testing.T) {
			s := fastSink(t, newFakeConsole(t, func(int) int { return http.StatusServiceUnavailable }))
			s.budget = 20 * time.Millisecond
			s.write(testEvent(1))
			s.close(context.Background())
			<-s.done
		}},
	}
	for name, sc := range scenarios {
		t.Run(name, func(t *testing.T) {
			freshDrops(t)
			captureLogs(t)
			sc.run(t)
			if got := processDrops.totals(); !maps.Equal(got, map[dropReason]int{sc.reason: 1}) {
				t.Errorf("counts %v, want %s 1", got, sc.reason)
			}
		})
	}
}

func TestTheFirstDeliveryAfterALossSaysWhatWasLost(t *testing.T) {
	logs := captureLogs(t)
	s := fastSink(t, newFakeConsole(t, func(n int) int { return []int{400, 202, 400, 202}[min(n, 3)] }))
	for n := range 4 {
		s.write(testEvent(n))
		waitFor(t, "a send", func() bool { return idle(s) })
	}
	if strings.Count(logs.String(), "delivering again; 1 event(s) lost before this: refused 1.") != 1 {
		t.Errorf("logs: %s", logs)
	}
	// What a suppressed warning held back is named by the next one.
	processDrops.mu.Lock()
	processDrops.lastRecoveryLog = time.Time{}
	processDrops.mu.Unlock()
	s.write(testEvent(4))
	waitFor(t, "a send", func() bool { return idle(s) })
	s.close(context.Background())
	if strings.Count(logs.String(), "lost before this: refused 1.") != 2 {
		t.Errorf("logs: %s", logs)
	}
}

func TestAnEventInFlightWhenCloseGivesUpIsCountedOnce(t *testing.T) {
	logs := captureLogs(t)
	release := make(chan struct{})
	s := fastSink(t, newFakeConsole(t, func(int) int { <-release; return http.StatusAccepted }))
	s.write(testEvent(0))
	waitFor(t, "the send to start", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.inFlight) > 0 })
	for n := range bufferCap {
		s.write(testEvent(n + 1))
	}
	s.budget = 50 * time.Millisecond
	s.close(context.Background())
	close(release)
	<-s.done
	if got := processDrops.totals(); !maps.Equal(got, map[dropReason]int{dropUnsentAtClose: bufferCap + 1}) {
		t.Errorf("counts %v; logs: %s", got, logs)
	}
}

// stalled is a collector that answers request n (from 0) with answer(n,
// the event_ids it carried), status 0 being a network error. When held(n)
// is true, the request first waits for release, ignoring its context unless
// endsOnCancel is set. With endsOnCancel it ends 20ms after the cancel, so the
// sender is still running for 20ms after close cancels.
type stalled struct {
	release      chan struct{}
	held         func(n int) bool
	answer       func(n int, ids []string) (int, string)
	endsOnCancel bool
	n            atomic.Int64
}

func (c *stalled) RoundTrip(r *http.Request) (*http.Response, error) {
	n := int(c.n.Add(1) - 1)
	var body struct {
		Events []struct {
			EventID string `json:"event_id"`
		}
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var ids []string
	for _, e := range body.Events {
		ids = append(ids, e.EventID)
	}
	if c.held(n) {
		select {
		case <-c.release:
		case <-r.Context().Done():
			if c.endsOnCancel {
				time.Sleep(20 * time.Millisecond)
				return nil, r.Context().Err()
			}
			<-c.release
		}
	}
	status, answer := c.answer(n, ids)
	if status == 0 {
		return nil, errors.New("connection reset")
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(answer)), Request: r}, nil
}

// stalledSink returns release, which ends the held requests. The test's end
// calls it before the sink's cleanup waits for the sender.
func stalledSink(t *testing.T, c *stalled) (s *httpSink, release func()) {
	c.release = make(chan struct{})
	release = sync.OnceFunc(func() { close(c.release) })
	s = stopsWithTheTest(t, newHTTPSink("http://127.0.0.1:1", testKey))
	t.Cleanup(release)
	s.client.Transport = c
	s.backoff = time.Millisecond
	s.flushEvery = time.Hour
	s.budget = 50 * time.Millisecond
	return s, release
}

func answerEach(status, code string) func(int, []string) (int, string) {
	return func(_ int, ids []string) (int, string) {
		var results []map[string]any
		for _, id := range ids {
			r := map[string]any{"event_id": id, "status": status}
			if code != "" {
				r["error"] = map[string]any{"code": code}
			}
			results = append(results, r)
		}
		b, _ := json.Marshal(map[string]any{"results": results})
		return http.StatusOK, string(b)
	}
}

// SPEC §11.2 rule 8: close counts a send it gave up on as it returns, and
// whatever the send's answer is later, nothing more.
func TestCloseCountsEachEventItGaveUpOnOnce(t *testing.T) {
	for name, c := range map[string]struct {
		collector *stalled
		events    int
		unsent    int
	}{
		"failed after close":             {&stalled{held: always, answer: networkError}, 3, 3},
		"accepted after close":           {&stalled{held: always, answer: answerEach("accepted", "")}, 3, 3},
		"refused one by one after close": {&stalled{held: always, answer: answerEach("refused", "quota_exhausted")}, 3, 3},
		"refused whole after close":      {&stalled{held: always, answer: func(int, []string) (int, string) { return http.StatusBadRequest, "" }}, 3, 3},
		"split, the first half accepted": {&stalled{held: func(n int) bool { return n == 2 }, answer: func(n int, ids []string) (int, string) {
			switch n {
			case 0:
				return http.StatusRequestEntityTooLarge, ""
			case 1:
				return answerEach("accepted", "")(n, ids)
			}
			return 0, ""
		}}, 4, 2},
	} {
		t.Run(name, func(t *testing.T) {
			captureLogs(t)
			s, release := stalledSink(t, c.collector)
			for n := range c.events {
				s.write(testEvent(n))
			}
			s.close(context.Background())
			want := map[dropReason]int{dropUnsentAtClose: c.unsent}
			wantCounts(t, want)
			release()
			<-s.done
			wantCounts(t, want)
			if n := queueLen(s); n != 0 {
				t.Errorf("%d events queued after the send ended", n)
			}
		})
	}
}

// A send that stops when cancelled has ended by the time close returns, so
// the process can exit at once.
func TestCloseWaitsForASendThatStopsWhenCancelled(t *testing.T) {
	logs := captureLogs(t)
	s, _ := stalledSink(t, &stalled{held: always, answer: networkError, endsOnCancel: true})
	s.budget = 300 * time.Millisecond // a grace well over the transport's 20ms
	for n := range 3 {
		s.write(testEvent(n))
	}
	s.close(context.Background())
	select {
	case <-s.done:
	default:
		t.Error("the sink was still sending as close returned")
	}
	wantCounts(t, map[dropReason]int{dropUnsentAtClose: 3})
	if !strings.Contains(logs.String(), "unsent=3") {
		t.Errorf("logs: %s", logs)
	}
}

// SPEC §11.2 rule 7: close returns within its budget even when the send
// ignores the cancel.
func TestCloseReturnsWithinItsBudgetWhenTheSendIgnoresTheCancel(t *testing.T) {
	for _, budget := range []time.Duration{40 * time.Millisecond, 200 * time.Millisecond} {
		t.Run(budget.String(), func(t *testing.T) {
			captureLogs(t)
			s, _ := stalledSink(t, &stalled{held: always, answer: networkError})
			s.budget = budget
			s.write(testEvent(0))
			started := time.Now()
			s.close(context.Background())
			// The slack stays under closeGrace, so the 200ms row catches a close
			// that overruns by its grace; the 40ms row's grace is smaller than it.
			if took := time.Since(started); took > budget+closeGrace*3/4 {
				t.Errorf("close took %v with a budget of %v", took, budget)
			}
			wantCounts(t, map[dropReason]int{dropUnsentAtClose: 1})
		})
	}
}

// slowJSON cannot be encoded, and says so only once release closes.
type slowJSON struct{ release chan struct{} }

func (j slowJSON) MarshalJSON() ([]byte, error) {
	<-j.release
	return nil, errors.New("slow")
}

// markedJSON cannot be encoded, and records that something tried.
type markedJSON struct{ tried *atomic.Bool }

func (j markedJSON) MarshalJSON() ([]byte, error) {
	j.tried.Store(true)
	return nil, errors.New("marked")
}

func TestNoEventIsEncodedAfterCloseReturns(t *testing.T) {
	captureLogs(t)
	s, _ := stalledSink(t, &stalled{held: never, answer: answerEach("accepted", "")})
	slow := slowJSON{make(chan struct{})}
	encoded := sync.OnceFunc(func() { close(slow.release) })
	t.Cleanup(encoded)
	var tried atomic.Bool
	s.mu.Lock() // both are measured in one pass
	s.push(Event{EventType: "tool_call_start", Payload: slow})
	s.push(Event{EventType: "tool_call_start", Payload: markedJSON{&tried}})
	s.mu.Unlock()
	s.wake <- struct{}{}
	s.close(context.Background())
	encoded()
	<-s.done
	wantCounts(t, map[dropReason]int{dropUnsentAtClose: 2})
	if tried.Load() {
		t.Error("an event was encoded after close returned")
	}
}

// heldLogs holds each log record whose message contains match until release
// is called, and closes entered when the first one arrives.
func heldLogs(t *testing.T, match string) (entered chan struct{}, release func()) {
	entered = make(chan struct{})
	held := make(chan struct{})
	release = sync.OnceFunc(func() { close(held) })
	t.Cleanup(release)
	once := sync.OnceFunc(func() { close(entered) })
	old := slog.Default()
	slog.SetDefault(slog.New(holdingHandler{old.Handler(), match, once, held}))
	t.Cleanup(func() { slog.SetDefault(old) })
	return entered, release
}

type holdingHandler struct {
	slog.Handler
	match   string
	entered func()
	held    chan struct{}
}

func (h holdingHandler) Handle(ctx context.Context, r slog.Record) error {
	if strings.Contains(r.Message, h.match) {
		h.entered()
		<-h.held
	}
	return h.Handler.Handle(ctx, r)
}

// Close may count while a log handler is slow, so a drop is counted before
// it is logged.
func TestAnOverCapEventIsCountedBeforeItIsLogged(t *testing.T) {
	captureLogs(t)
	entered, releaseLogs := heldLogs(t, "over the 1 MiB")
	c := &stalled{held: func(n int) bool { return n == 0 }, answer: answerEach("accepted", "")}
	s, release := stalledSink(t, c)
	s.flushEvery = 0
	s.write(testEvent(0))
	waitFor(t, "the send to start", func() bool { return c.n.Load() == 1 })
	for n := range 3 {
		s.write(bigEvent(n+1, batchBytes))
	}
	release()
	<-entered
	s.close(context.Background())
	wantCounts(t, map[dropReason]int{dropRefused: 3})
	releaseLogs()
}

// SPEC §2.5: an event close finds measured is counted as what the measure
// found; one not yet measured is unsent.
func TestCloseCountsAnEventMeasuredUnsendableAsSuch(t *testing.T) {
	logs := captureLogs(t)
	s := batchingSink(t, newFakeConsole(t, nil))
	s.rateLimited = time.Now().Add(time.Hour)
	s.write(bigEvent(1, batchBytes))
	s.write(Event{EventType: "tool_call_start", Payload: map[string]any{"bad": func() {}}})
	s.write(testEvent(3))
	s.sizeNew()
	s.write(bigEvent(4, batchBytes))
	s.close(context.Background())
	wantCounts(t, map[dropReason]int{dropRefused: 1, dropEncodeFailed: 1, dropUnsentAtClose: 2})
	if !strings.Contains(logs.String(), "unsent=2 refused=1 encode_failed=1") {
		t.Errorf("logs: %s", logs)
	}
}

func TestCloseLogsAMeasuredUnsendableEventWhenNothingElseIsLeft(t *testing.T) {
	logs := captureLogs(t)
	s := batchingSink(t, newFakeConsole(t, nil))
	s.rateLimited = time.Now().Add(time.Hour)
	s.write(bigEvent(1, batchBytes))
	s.sizeNew()
	s.close(context.Background())
	if !strings.Contains(logs.String(), "unsent=0 refused=1") {
		t.Errorf("logs: %s", logs)
	}
}

func wantCounts(t *testing.T, want map[dropReason]int) {
	t.Helper()
	if got := processDrops.totals(); !maps.Equal(got, want) {
		t.Errorf("counts %v, want %v", got, want)
	}
}

func always(int) bool { return true }
func never(int) bool  { return false }

func networkError(int, []string) (int, string) { return 0, "" }

func queueLen(s *httpSink) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

func TestAHealthyRunCountsAndLogsNothing(t *testing.T) {
	logs := captureLogs(t)
	s := fastSink(t, newFakeConsole(t, nil))
	for n := range 3 {
		s.write(testEvent(n))
	}
	s.close(context.Background())
	if len(processDrops.totals()) != 0 || logs.Len() != 0 {
		t.Errorf("counts %v; logs: %s", processDrops.totals(), logs)
	}
}

// idle reports whether s has nothing queued and nothing being sent.
func idle(s *httpSink) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue) == 0 && len(s.inFlight) == 0
}
