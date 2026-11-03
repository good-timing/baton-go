package baton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"runtime/debug"
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

// floodJSON cannot be encoded, and records next more like it as the sink
// tries, until stop is set: a flood of unsendable events that never lets up.
type floodJSON struct {
	s       *httpSink
	next    int
	stop    *atomic.Bool
	written *atomic.Int64
}

func (f floodJSON) MarshalJSON() ([]byte, error) {
	for range f.next {
		if !f.stop.Load() {
			f.written.Add(1)
			f.s.write(Event{EventType: "tool_call_start", Payload: f})
		}
	}
	return nil, errors.New("unencodable")
}

// SPEC §11.2 rule 1: a flood of events that each make more does not grow the
// sending goroutine's stack until it kills the vendor's process. A flush in
// it waits until its deadline, and every event is counted by close.
func TestAFloodOfUnencodableEventsDoesNotGrowTheStack(t *testing.T) {
	// next=1 adds one event per pass; next=2 grows the queue until it is full.
	for _, next := range []int{1, 2} {
		t.Run(fmt.Sprint(next), func(t *testing.T) {
			freshDrops(t)
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })
			s := batchingSink(t, newFakeConsole(t, nil))
			// Small enough that a call per try overflows within flush's and close's budgets.
			// Set after the sink and server, so it is restored before their
			// cleanups; the limit is the process's, so this test is not parallel.
			oldStack := debug.SetMaxStack(1 << 16)
			t.Cleanup(func() { debug.SetMaxStack(oldStack) })
			s.budget = 200 * time.Millisecond
			var stop atomic.Bool
			t.Cleanup(func() { stop.Store(true) })
			written := &atomic.Int64{}
			written.Add(1)
			s.write(Event{EventType: "tool_call_start", Payload: floodJSON{s, next, &stop, written}})

			started := time.Now()
			s.flush(nil)
			if took := time.Since(started); took < s.budget || took > s.budget+200*time.Millisecond {
				t.Errorf("flush took %v with a budget of %v", took, s.budget)
			}
			started = time.Now()
			s.close(nil)
			if took := time.Since(started); took > s.budget+200*time.Millisecond {
				t.Errorf("close took %v with a budget of %v", took, s.budget)
			}
			<-s.done
			counted := 0
			for _, n := range processDrops.totals() {
				counted += n
			}
			if n := written.Load(); int64(counted) != n {
				t.Errorf("%d counted, %d written", counted, n)
			}
		})
	}
}

// A send close cancelled says nothing about the console.
func TestASendCloseCancelledIsNotAFailingConsole(t *testing.T) {
	logs := captureLogs(t)
	s, _ := stalledSink(t, &stalled{held: always, answer: networkError, endsOnCancel: true})
	s.budget = 300 * time.Millisecond // a grace well over the transport's 20ms
	s.failures = breakerThreshold - 1
	s.write(testEvent(0))
	s.close(context.Background())
	if strings.Contains(logs.String(), "the console is failing") {
		t.Errorf("logs: %s", logs)
	}
}

// Close cancels a report sent alone at once; that is not a console failure,
// and the events behind it are still sent.
func TestALoneReportCloseCancelledDoesNotStopTheEventsBehindIt(t *testing.T) {
	logs := captureLogs(t)
	c := &stalled{held: func(n int) bool { return n == 1 }, answer: answerEach("accepted", ""), endsOnCancel: true}
	s, _ := stalledSink(t, c)
	s.budget = 500 * time.Millisecond
	s.flushEvery = 0
	s.reportEvery = 0
	processDrops.add(dropBufferFull, 1) // so there is something to report
	s.write(tenantEvent(0))
	waitFor(t, "the report's send", func() bool { return c.n.Load() == 2 })
	for n := range 3 {
		s.write(tenantEvent(n + 1))
	}
	s.failures = breakerThreshold - 1
	s.close(context.Background())
	wantCounts(t, map[dropReason]int{dropBufferFull: 1})
	if c.n.Load() != 3 || strings.Contains(logs.String(), "the console is failing") {
		t.Errorf("%d requests; logs: %s", c.n.Load(), logs)
	}
}

// Close cancels reports as it starts; a failed send of events is still a
// failure then, so close does not send it again at once.
func TestCloseDoesNotResendAFailedBatch(t *testing.T) {
	captureLogs(t)
	c := &stalled{held: never, answer: func(int, []string) (int, string) { return http.StatusServiceUnavailable, "" }}
	s, _ := stalledSink(t, c)
	s.budget = 300 * time.Millisecond
	s.write(testEvent(0))
	s.close(context.Background())
	if n := c.n.Load(); n != maxRetries+1 {
		t.Errorf("%d requests, want %d", n, maxRetries+1)
	}
}

// panicky panics on every request with value, which must not reach a log.
type panicky struct{ value any }

func (p panicky) RoundTrip(*http.Request) (*http.Response, error) { panic(p.value) }

// SPEC §11.2 rule 1: a panic on the sending goroutine does not crash the
// vendor's process. Sending stops, and every event from then on is counted.
func TestAPanicOnTheSendingGoroutineStopsSendingAndCountsTheRest(t *testing.T) {
	for _, c := range []struct {
		name  string
		value any
	}{{"a value", "secret payload"}, {"nil", nil}} {
		t.Run(c.name, func(t *testing.T) {
			if c.value == nil {
				t.Setenv("GODEBUG", "panicnil=1") // without it, panic(nil) is a *runtime.PanicNilError
			}
			logs := captureLogs(t)
			s := stopsWithTheTest(t, newHTTPSink("http://127.0.0.1:1", testKey))
			s.client.Transport = panicky{c.value}
			s.flushEvery = 0
			s.mu.Lock() // a full batch, and one left queued
			for n := range batchCap + 1 {
				s.push(testEvent(n))
			}
			s.mu.Unlock()
			s.wake <- struct{}{}
			waitDone(t, s)
			s.write(testEvent(batchCap + 1))
			started := time.Now()
			s.close(context.Background())
			if took := time.Since(started); took > 100*time.Millisecond {
				t.Errorf("close took %v", took)
			}
			wantCounts(t, map[dropReason]int{dropWriteFailed: batchCap + 2})
			if n := warnings(logs); n != 1 || strings.Contains(logs.String(), "secret") {
				t.Errorf("%d warnings; logs: %s", n, logs)
			}
			s.write(testEvent(batchCap + 2))
			wantCounts(t, map[dropReason]int{dropWriteFailed: batchCap + 2, dropAfterClose: 1})
		})
	}
}

func waitDone(t *testing.T, s *httpSink) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sending goroutine is still running")
	}
}

// panickingHandler panics on each record whose message contains match.
type panickingHandler struct {
	slog.Handler
	match []string
}

func (h panickingHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, m := range h.match {
		if strings.Contains(r.Message, m) {
			panic("log handler")
		}
	}
	return h.Handler.Handle(ctx, r)
}

// The vendor's log handler may be what panicked; logging that sending
// stopped must not panic again.
func TestALogHandlerThatPanicsOnTheSendingGoroutineDoesNotCrash(t *testing.T) {
	logs := captureLogs(t)
	slog.SetDefault(slog.New(panickingHandler{slog.NewTextHandler(logs, nil), []string{"REFUSED", "sending goroutine"}}))
	s := stopsWithTheTest(t, newHTTPSink("http://127.0.0.1:1", testKey))
	s.client.Transport = &stalled{held: never, answer: func(int, []string) (int, string) { return http.StatusBadRequest, "" }}
	s.flushEvery = 0
	s.mu.Lock() // a full batch refused, and one left for die to count and log
	for n := range batchCap + 1 {
		s.push(testEvent(n))
	}
	s.mu.Unlock()
	s.wake <- struct{}{}
	waitDone(t, s)
	s.write(testEvent(batchCap + 1))
	s.close(context.Background())
	wantCounts(t, map[dropReason]int{dropRefused: batchCap, dropWriteFailed: 2})
}

// A flush the goroutine took before it stopped returns at once.
func TestAFlushReturnsWhenTheSendingGoroutineStops(t *testing.T) {
	captureLogs(t)
	s := stopsWithTheTest(t, newHTTPSink("http://127.0.0.1:1", testKey))
	s.client.Transport = panicky{"x"}
	s.flushEvery = time.Hour
	s.budget = 2 * time.Second
	s.write(testEvent(0))
	started := time.Now()
	s.flush(nil)
	if took := time.Since(started); took > 500*time.Millisecond {
		t.Errorf("flush took %v", took)
	}
}

// An event the goroutine had measured unsendable is counted as such when it
// stops, as close counts it.
func TestAStoppedSinkCountsAMeasuredUnsendableEventAsSuch(t *testing.T) {
	captureLogs(t)
	s := &httpSink{ctx: context.Background()}
	s.push(bigEvent(1, batchBytes))
	s.push(Event{EventType: "tool_call_start", Payload: map[string]any{"bad": func() {}}})
	s.push(testEvent(3))
	s.sizeNew()
	s.push(bigEvent(4, batchBytes))
	s.die("x")
	wantCounts(t, map[dropReason]int{dropRefused: 1, dropEncodeFailed: 1, dropWriteFailed: 2})
}

// A panic inside a section that holds s.mu still releases it, so the sink
// stops sending rather than hang every write. The test overfills the queue
// past the buffer's bound, so putting a failed batch back slices out of range.
// That leaves the counts of what was held off by the update the panic cut
// short, so only a later write's count is checked.
func TestAPanicHoldingTheLockIsRecovered(t *testing.T) {
	captureLogs(t)
	s, _ := stalledSink(t, &stalled{held: never, answer: networkError})
	s.flushEvery = 0
	extra := bufferCap + batchCap + 50
	s.mu.Lock()
	for n := range extra {
		s.queue = append(s.queue, &queued{Event: testEvent(n), written: time.Now()})
	}
	s.mu.Unlock()
	s.wake <- struct{}{}
	waitDone(t, s)
	if !s.mu.TryLock() {
		t.Fatal("s.mu is still held")
	}
	s.mu.Unlock()
	before := processDrops.totals()[dropWriteFailed]
	s.write(testEvent(extra))
	if n := processDrops.totals()[dropWriteFailed] - before; n != 1 {
		t.Errorf("a later write counted %d", n)
	}
	started := time.Now()
	s.close(context.Background())
	if took := time.Since(started); took > 100*time.Millisecond {
		t.Errorf("close took %v", took)
	}
}

// What the goroutine held when it stopped is counted as close counts it: an
// event it had measured unsendable as refused or encode_failed.
func TestAPanicOnTheSendingGoroutineCountsMeasuredEventsAsSuch(t *testing.T) {
	captureLogs(t)
	s := stopsWithTheTest(t, newHTTPSink("http://127.0.0.1:1", testKey))
	s.client.Transport = panicky{"x"}
	s.flushEvery = 0
	s.mu.Lock() // sent alone, as the next does not fit beside it
	s.push(bigEvent(0, batchBytes*6/10))
	s.push(bigEvent(1, batchBytes*6/10))
	s.push(bigEvent(2, batchBytes))
	s.push(Event{EventType: "tool_call_start", Payload: map[string]any{"bad": func() {}}})
	s.mu.Unlock()
	s.wake <- struct{}{}
	waitDone(t, s)
	s.write(testEvent(4))
	s.close(context.Background())
	wantCounts(t, map[dropReason]int{dropWriteFailed: 3, dropRefused: 1, dropEncodeFailed: 1})
}

func TestASinkThatStopsHoldingNothingLogsNothing(t *testing.T) {
	logs := captureLogs(t)
	(&httpSink{ctx: context.Background()}).die("x")
	if n := warnings(logs); n != 0 {
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
