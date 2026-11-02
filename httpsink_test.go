package baton

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeConsole struct {
	mu           sync.Mutex
	requests     []*http.Request
	sizes        []int
	perRequest   []int
	active       int
	mostActive   int
	events       []map[string]any
	reports      []map[string]any
	reportsAlone int
	reportStatus int
	answer       func(n int) int
	refuse       func(event map[string]any) bool
	noBatch      int
	maxEvents    int
	retryAfter   string
	url          string
}

// reportStatus values that answer no status: the connection is dropped, or
// the request is held until the client gives up.
const (
	reportDrop = -1
	reportHang = -2
)

// newFakeConsole answers the nth request carrying events (from 0) with
// answer(n), or 2xx. A 2xx on the batch path answers each event accepted, or
// refused where refuse says so. A request that carries only client_reports
// is kept apart, in reports, and answered with reportStatus, or 2xx. The
// batch path answers noBatch when set, and 413 to a body over the cap or to
// more than maxEvents events when that is set. A 429 carries retryAfter.
// Its URL is a DSN's origin.
func newFakeConsole(t *testing.T, answer func(n int) int) *fakeConsole {
	t.Helper()
	c := &fakeConsole{answer: answer}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batch := r.URL.Path == "/v0/events/batch"
		raw, _ := io.ReadAll(r.Body)
		var events []map[string]any
		if batch {
			var body struct{ Events []map[string]any }
			_ = json.Unmarshal(raw, &body)
			events = body.Events
		} else {
			var event map[string]any
			_ = json.Unmarshal(raw, &event)
			events = []map[string]any{event}
		}
		c.mu.Lock()
		if batch && c.noBatch != 0 {
			c.mu.Unlock()
			w.WriteHeader(c.noBatch)
			return
		}
		c.active++
		c.mostActive = max(c.mostActive, c.active)
		defer func() { c.mu.Lock(); c.active--; c.mu.Unlock() }()
		reportsOnly := len(events) > 0
		for _, e := range events {
			if e["event_type"] == "client_report" {
				c.reports = append(c.reports, e)
			} else {
				reportsOnly = false
			}
		}
		status := http.StatusAccepted
		if reportsOnly {
			c.reportsAlone++
			status = cmp.Or(c.reportStatus, status)
			c.mu.Unlock()
			switch status {
			case reportDrop:
				panic(http.ErrAbortHandler)
			case reportHang:
				<-r.Context().Done()
				return
			}
		} else {
			n := len(c.requests)
			c.requests = append(c.requests, r)
			c.sizes = append(c.sizes, len(raw))
			c.perRequest = append(c.perRequest, len(events))
			for _, e := range events {
				if e["event_type"] != "client_report" {
					c.events = append(c.events, e)
				}
			}
			c.mu.Unlock()
			if c.answer != nil {
				status = c.answer(n)
			}
			if batch && (len(raw) > batchBytes || c.maxEvents > 0 && len(events) > c.maxEvents) {
				status = http.StatusRequestEntityTooLarge
			}
		}
		if status == http.StatusTooManyRequests && c.retryAfter != "" {
			w.Header().Set("Retry-After", c.retryAfter)
		}
		if !batch || status < 200 || status >= 300 {
			w.WriteHeader(status)
			return
		}
		var results []map[string]any
		for _, e := range events {
			result := map[string]any{"event_id": e["event_id"], "status": "accepted"}
			if c.refuse != nil && c.refuse(e) {
				result["status"] = "refused"
				result["error"] = map[string]any{"code": "payload_malformed", "message": "refused"}
			}
			results = append(results, result)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(ts.Close)
	c.url = ts.URL
	return c
}

func (c *fakeConsole) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func (c *fakeConsole) eventCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *fakeConsole) dsn() string {
	return strings.Replace(c.url, "http://", "http://"+testKey+"@", 1) + "/" + testWorkspace + "/echo-server"
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func testEvent(n int) Event {
	return Event{EventID: uuid7(), EventType: "tool_call_start", SequenceNumber: int64(n)}
}

// fastSink sends each event as soon as it is written, so that one write is
// one request when the test waits between writes.
func fastSink(c *fakeConsole) *httpSink {
	s := newHTTPSink(c.url, testKey)
	s.backoff = time.Millisecond
	s.flushEvery = 0
	return s
}

func TestEventsReachTheConsole(t *testing.T) {
	console := newFakeConsole(t, nil)
	server := newVectorServer()
	addLookup(server)
	handle, err := Install(server, Config{DSN: console.dsn()})
	if err != nil {
		t.Fatal(err)
	}
	call(t, connect(t, server), "lookup", map[string]any{"name": "alice", "user_goal": "find alice"})
	handle.Close(context.Background())

	if console.count() != 1 {
		t.Errorf("%d requests, want the 3 events in one", console.count())
	}
	for i, r := range console.requests {
		if r.Method != http.MethodPost || r.URL.Path != "/v0/events/batch" || r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("request %d: %s %s, Authorization %q", i, r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
	}
	var types []string
	for i, e := range console.events {
		if e["tenant_id"] != testWorkspace || e["vendor_id"] != "echo-server" {
			t.Errorf("event %d: tenant_id %v, vendor_id %v", i, e["tenant_id"], e["vendor_id"])
		}
		types = append(types, e["event_type"].(string))
	}
	if strings.Join(types, ",") != "surface_snapshot,tool_call_start,tool_call_end" {
		t.Errorf("events %v", types)
	}
}

func TestAStuckConsoleDoesNotSlowToolCalls(t *testing.T) {
	release := make(chan struct{})
	console := newFakeConsole(t, func(int) int { <-release; return http.StatusAccepted })
	t.Cleanup(func() { close(release) }) // before the server's Close, which waits for handlers
	logs := captureLogs(t)
	server := newVectorServer()
	addLookup(server)
	handle, err := Install(server, Config{DSN: console.dsn()})
	if err != nil {
		t.Fatal(err)
	}
	cs := connect(t, server)
	start := time.Now()
	for range 20 {
		call(t, cs, "lookup", map[string]any{"name": "alice"})
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("20 calls took %v", took)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start = time.Now()
	handle.Close(ctx)
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Errorf("Close took %v", took)
	}
	if !strings.Contains(logs.String(), "unsent=41") {
		t.Errorf("logs: %s", logs)
	}
}

func TestCloseUsesTheBudgetWithoutADeadline(t *testing.T) {
	release := make(chan struct{})
	console := newFakeConsole(t, func(int) int { <-release; return http.StatusAccepted })
	t.Cleanup(func() { close(release) })
	s := fastSink(console)
	if s.budget != 3*time.Second {
		t.Errorf("default budget %v", s.budget)
	}
	s.budget = 50 * time.Millisecond
	s.write(testEvent(1))
	waitFor(t, "the send", func() bool { return console.count() == 1 })
	start := time.Now()
	s.close(nil) // a vendor may pass nil
	if took := time.Since(start); took < s.budget || took > s.budget+300*time.Millisecond {
		t.Errorf("close took %v", took)
	}
	select {
	case <-s.done:
	case <-time.After(500 * time.Millisecond):
		t.Error("the send still running was not stopped")
	}
}

func TestADeadlineLongerThanTheBudgetIsKept(t *testing.T) {
	release := make(chan struct{})
	console := newFakeConsole(t, func(int) int { <-release; return http.StatusAccepted })
	t.Cleanup(func() { close(release) })
	s := fastSink(console)
	s.budget = 10 * time.Millisecond
	s.write(testEvent(1))
	waitFor(t, "the send", func() bool { return console.count() == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	s.close(ctx)
	if took := time.Since(start); took < 150*time.Millisecond {
		t.Errorf("close took %v, before ctx's deadline", took)
	}
}

func TestARefusedEventIsDroppedAndTheRestStillGo(t *testing.T) {
	console := newFakeConsole(t, nil)
	console.refuse = func(e map[string]any) bool { return e["sequence_number"] == 0.0 }
	logs := captureLogs(t)
	s := fastSink(console)
	s.flushEvery = time.Hour
	for n := range 3 {
		s.write(testEvent(n))
	}
	s.close(context.Background())
	if console.count() != 1 || console.eventCount() != 3 {
		t.Errorf("%d requests carrying %d events, want 1 carrying 3", console.count(), console.eventCount())
	}
	if got := processDrops.totals(); !maps.Equal(got, map[dropReason]int{dropRefused: 1}) {
		t.Errorf("counts %v, want refused 1", got)
	}
	if !strings.Contains(logs.String(), "REFUSED") || strings.Contains(logs.String(), "unsent") {
		t.Errorf("logs: %s", logs)
	}
}

func TestABatchRefusedAsAWholeIsCountedEventByEvent(t *testing.T) {
	logs := captureLogs(t)
	s := fastSink(newFakeConsole(t, func(int) int { return http.StatusForbidden }))
	s.flushEvery = time.Hour
	for n := range 3 {
		s.write(testEvent(n))
	}
	s.close(context.Background())
	if got := processDrops.totals(); !maps.Equal(got, map[dropReason]int{dropRefused: 3}) {
		t.Errorf("counts %v, want refused 3", got)
	}
	if strings.Count(logs.String(), "REFUSED") != 1 {
		t.Errorf("logs: %s", logs)
	}
}

func TestATransientFailureIsRetried(t *testing.T) {
	console := newFakeConsole(t, func(n int) int {
		return []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusAccepted}[min(n, 2)]
	})
	s := fastSink(console)
	s.write(testEvent(7))
	s.close(context.Background())
	if console.count() != 3 || console.events[2]["sequence_number"] != 7.0 {
		t.Errorf("%d requests, last %v", console.count(), console.events)
	}
}

func TestAFullBufferDropsTheOldest(t *testing.T) {
	logs := captureLogs(t)
	s := &httpSink{wake: make(chan struct{}, 1)}
	for n := range bufferCap + 2 {
		s.write(testEvent(n))
	}
	if len(s.queue) != bufferCap || s.queue[0].SequenceNumber != 2 {
		t.Errorf("%d queued, oldest %d", len(s.queue), s.queue[0].SequenceNumber)
	}
	if strings.Count(logs.String(), "buffer is full") != 1 {
		t.Errorf("logs: %s", logs)
	}
}

func TestAFailedEventPutBackIntoAFullBufferIsTheOneDropped(t *testing.T) {
	s := &httpSink{}
	for n := range bufferCap {
		s.push(testEvent(n + 1))
	}
	failed := []*queued{{Event: testEvent(-1)}, {Event: testEvent(0)}}
	if s.putBack(failed) != 2 || len(s.queue) != bufferCap || s.queue[0].SequenceNumber != 1 {
		t.Errorf("%d queued, oldest %d", len(s.queue), s.queue[0].SequenceNumber)
	}
	s.popFront()
	if s.putBack(failed) != 1 || s.queue[0].SequenceNumber != 0 {
		t.Errorf("oldest %d", s.queue[0].SequenceNumber)
	}
}

func TestAfterFiveFailuresNothingIsSentAndCloseDoesNotTry(t *testing.T) {
	console := newFakeConsole(t, func(int) int { return http.StatusInternalServerError })
	s := fastSink(console)
	perFailure := maxRetries + 1
	for n := range breakerThreshold {
		s.write(testEvent(n))
		waitFor(t, "the retries", func() bool { return console.count() == (n+1)*perFailure })
	}
	s.write(testEvent(99))
	time.Sleep(50 * time.Millisecond)
	s.close(context.Background())
	if got := console.count(); got != breakerThreshold*perFailure {
		t.Errorf("%d requests, want %d", got, breakerThreshold*perFailure)
	}
}

func TestAfterTheBreakerResetsTheKeptEventsAreSent(t *testing.T) {
	failing := true
	var mu sync.Mutex
	console := newFakeConsole(t, func(int) int {
		mu.Lock()
		defer mu.Unlock()
		if failing {
			return http.StatusInternalServerError
		}
		return http.StatusAccepted
	})
	s := fastSink(console)
	s.reset = 50 * time.Millisecond
	for n := range breakerThreshold {
		s.write(testEvent(n))
		waitFor(t, "the retries", func() bool { return console.count() == (n+1)*(maxRetries+1) })
	}
	mu.Lock()
	failing = false
	mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	s.write(testEvent(99))
	s.close(context.Background())
	// The kept events and the new one go in one batch.
	if got := console.count() - breakerThreshold*(maxRetries+1); got != 1 {
		t.Errorf("%d requests after the reset, want 1", got)
	}
	var last []float64
	for _, e := range console.events[console.eventCount()-breakerThreshold-1:] {
		last = append(last, e["sequence_number"].(float64))
	}
	if !slices.Equal(last, []float64{0, 1, 2, 3, 4, 99}) {
		t.Errorf("the batch after the reset held %v", last)
	}
}

func TestAnEventAfterCloseIsDropped(t *testing.T) {
	console := newFakeConsole(t, nil)
	logs := captureLogs(t)
	s := fastSink(console)
	s.close(context.Background())
	s.write(testEvent(1))
	s.write(testEvent(2))
	if console.count() != 0 || strings.Count(logs.String(), "after Close") != 1 {
		t.Errorf("%d requests; logs: %s", console.count(), logs)
	}
}

func TestASecondCloseWaitsForTheFirst(t *testing.T) {
	release := make(chan struct{})
	console := newFakeConsole(t, func(int) int { <-release; return http.StatusAccepted })
	s := fastSink(console)
	s.write(testEvent(1))
	waitFor(t, "the send", func() bool { return console.count() == 1 })
	var wg sync.WaitGroup
	var released time.Time
	finished := make([]time.Time, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.close(context.Background())
			finished[i] = time.Now()
		}()
	}
	time.Sleep(50 * time.Millisecond)
	released = time.Now()
	close(release)
	wg.Wait()
	for i, f := range finished {
		if f.Before(released) {
			t.Errorf("close %d returned before the first finished", i)
		}
	}
}

func TestWithoutADSNNothingIsInstalled(t *testing.T) {
	t.Setenv("BATON_DSN", "")
	logs := captureLogs(t)
	server := newVectorServer()
	addLookup(server)
	handle, err := Install(server, Config{ReportMode: "on"})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close(context.Background())
	res, err := connect(t, server).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 1 || strings.Contains(string(mustJSON(t, res.Tools[0].InputSchema)), userGoalParam) {
		t.Errorf("tools: %s", mustJSON(t, res.Tools))
	}
	if !strings.Contains(logs.String(), "no DSN") {
		t.Errorf("logs: %s", logs)
	}
}

func TestTheEnvironmentDSNIsUsed(t *testing.T) {
	console := newFakeConsole(t, nil)
	t.Setenv("BATON_DSN", console.dsn())
	server := newVectorServer()
	addLookup(server)
	handle, err := Install(server, Config{})
	if err != nil {
		t.Fatal(err)
	}
	call(t, connect(t, server), "lookup", map[string]any{"name": "alice"})
	handle.Close(context.Background())
	if console.count() == 0 {
		t.Error("nothing reached the console")
	}
}

func TestABadDSNIsAnErrorWithoutTheKey(t *testing.T) {
	_, err := Install(newVectorServer(), Config{DSN: strings.Replace(testDSN, "echo-server", "my.server", 1)})
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("err %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestARedirectIsNotFollowed(t *testing.T) {
	var gets int
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			mu.Lock()
			gets++
			mu.Unlock()
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	t.Cleanup(ts.Close)
	logs := captureLogs(t)
	s := newHTTPSink(ts.URL, testKey)
	s.backoff = time.Millisecond
	s.write(testEvent(1))
	s.close(context.Background())
	if gets != 0 || !strings.Contains(logs.String(), "unsent=1") {
		t.Errorf("%d GETs; logs: %s", gets, logs)
	}
}

func TestAFailedEventIsRetriedWithoutAnotherWrite(t *testing.T) {
	console := newFakeConsole(t, func(n int) int {
		if n <= maxRetries {
			return http.StatusServiceUnavailable
		}
		return http.StatusAccepted
	})
	s := fastSink(console)
	s.retryAfter = 10 * time.Millisecond
	s.write(testEvent(1))
	waitFor(t, "the retry", func() bool { return console.count() == maxRetries+2 })
	s.close(context.Background())
}

func TestTheBreakerOpeningIsLogged(t *testing.T) {
	console := newFakeConsole(t, func(int) int { return http.StatusBadGateway })
	logs := captureLogs(t)
	s := fastSink(console)
	for n := range breakerThreshold {
		s.write(testEvent(n))
		waitFor(t, "the retries", func() bool { return console.count() == (n+1)*(maxRetries+1) })
	}
	s.close(context.Background())
	if !strings.Contains(logs.String(), "console is failing") || !strings.Contains(logs.String(), "502") {
		t.Errorf("logs: %s", logs)
	}
}

// closingHandler calls close from inside logging, as a vendor's handler might.
type closingHandler struct {
	slog.Handler
	close func()
}

func (h closingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.close()
	return h.Handler.Handle(ctx, r)
}

func TestCloseCalledFromALogHandlerDoesNotWaitOnItself(t *testing.T) {
	release := make(chan struct{})
	console := newFakeConsole(t, func(int) int { <-release; return http.StatusAccepted })
	t.Cleanup(func() { close(release) })
	s := fastSink(console)
	s.budget = 10 * time.Millisecond
	old := slog.Default()
	slog.SetDefault(slog.New(closingHandler{slog.NewTextHandler(io.Discard, nil), func() { s.close(context.Background()) }}))
	t.Cleanup(func() { slog.SetDefault(old) })
	s.write(testEvent(1))
	waitFor(t, "the send", func() bool { return console.count() == 1 })
	returned := make(chan struct{})
	go func() {
		s.close(context.Background())
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("close waited on itself")
	}
}

type leakyValue struct{}

func (leakyValue) MarshalJSON() ([]byte, error) { return nil, errors.New("private-result-text") }

func TestAnUnencodableEventIsLoggedWithoutItsText(t *testing.T) {
	console := newFakeConsole(t, nil)
	logs := captureLogs(t)
	s := fastSink(console)
	s.write(Event{EventType: "tool_call_end", Payload: leakyValue{}})
	s.write(testEvent(2))
	s.close(context.Background())
	if console.count() != 1 || !strings.Contains(logs.String(), "could not be encoded") || strings.Contains(logs.String(), "private-result-text") {
		t.Errorf("%d requests; logs: %s", console.count(), logs)
	}
}

func TestAnEventThatPanicsWhenEncodedIsDropped(t *testing.T) {
	console := newFakeConsole(t, nil)
	logs := captureLogs(t)
	before := encodeFailedDrops()
	s := fastSink(console)
	s.write(Event{EventType: "tool_call_end", Payload: map[string]any{"result": panicsWhenEncoded{}}})
	s.write(testEvent(2))
	s.close(context.Background())
	if console.count() != 1 || encodeFailedDrops() != before+1 || strings.Contains(logs.String(), "cannot encode") {
		t.Errorf("%d requests, %d dropped; logs: %s", console.count(), encodeFailedDrops()-before, logs)
	}
}

func (c *fakeConsole) batchSizes() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.perRequest)
}

func (c *fakeConsole) sequences() []float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []float64
	for _, e := range c.events {
		out = append(out, e["sequence_number"].(float64))
	}
	return out
}

// batchingSink waits for the count or byte cap, Flush or Close, never the timer.
func batchingSink(c *fakeConsole) *httpSink {
	s := fastSink(c)
	s.flushEvery = time.Hour
	return s
}

func bigEvent(n, size int) Event {
	e := testEvent(n)
	e.Payload = strings.Repeat("x", size)
	return e
}

func TestABatchGoesAtOneHundredEvents(t *testing.T) {
	captureLogs(t)
	console := newFakeConsole(t, nil)
	s := batchingSink(console)
	for n := range 2*batchCap + 50 {
		s.write(testEvent(n))
	}
	waitFor(t, "two full batches", func() bool { return console.count() == 2 })
	time.Sleep(50 * time.Millisecond)
	if got := console.batchSizes(); !slices.Equal(got, []int{batchCap, batchCap}) {
		t.Errorf("batches %v before close, want two of %d", got, batchCap)
	}
	s.close(context.Background())
	if got := console.batchSizes(); !slices.Equal(got, []int{batchCap, batchCap, 50}) {
		t.Errorf("batches %v", got)
	}
}

func TestABatchGoesWhenTheNextEventWouldPassTheByteCap(t *testing.T) {
	captureLogs(t)
	console := newFakeConsole(t, nil)
	s := batchingSink(console)
	for n := range 3 {
		s.write(bigEvent(n, 400_000))
	}
	waitFor(t, "a batch", func() bool { return console.count() == 1 })
	s.close(context.Background())
	sizes := console.batchSizes()
	if !slices.Equal(sizes, []int{2, 1}) || console.sizes[0] > batchBytes {
		t.Errorf("batches %v of %v bytes, want 2 then 1 within %d", sizes, console.sizes, batchBytes)
	}
}

func TestAnEventTooBigForABatchGoesAlone(t *testing.T) {
	captureLogs(t)
	console := newFakeConsole(t, nil)
	s := batchingSink(console)
	for n := range 2 {
		s.write(bigEvent(n, 600_000))
	}
	s.close(context.Background())
	if got := console.batchSizes(); !slices.Equal(got, []int{1, 1}) || len(processDrops.totals()) != 0 {
		t.Errorf("batches %v, counts %v", got, processDrops.totals())
	}
}

func TestAnEventOverTheByteCapIsRefusedAndTheNextStillGoes(t *testing.T) {
	logs := captureLogs(t)
	console := newFakeConsole(t, nil)
	s := batchingSink(console)
	s.write(bigEvent(0, batchBytes))
	s.write(testEvent(1))
	s.close(context.Background())
	if got := processDrops.totals(); !maps.Equal(got, map[dropReason]int{dropRefused: 1}) {
		t.Errorf("counts %v, want refused 1", got)
	}
	if got := console.sequences(); !slices.Equal(got[len(got)-1:], []float64{1}) || !strings.Contains(logs.String(), "status=413") {
		t.Errorf("sequences %v; logs: %s", got, logs)
	}
}

func TestABatchGoesFlushIntervalAfterTheOldestEvent(t *testing.T) {
	console := newFakeConsole(t, nil)
	s := fastSink(console)
	s.flushEvery = 150 * time.Millisecond
	start := time.Now()
	s.write(testEvent(0))
	time.Sleep(75 * time.Millisecond)
	s.write(testEvent(1))
	waitFor(t, "the timer", func() bool { return console.count() == 1 })
	took := time.Since(start)
	if took < s.flushEvery || took > s.flushEvery+60*time.Millisecond {
		t.Errorf("sent %v after the first write, want about %v", took, s.flushEvery)
	}
	if got := console.batchSizes(); !slices.Equal(got, []int{2}) {
		t.Errorf("batches %v, want both events in one", got)
	}
	s.close(context.Background())
}

func TestFlushSendsAtOnce(t *testing.T) {
	console := newFakeConsole(t, nil)
	s := batchingSink(console)
	for n := range 3 {
		s.write(testEvent(n))
	}
	start := time.Now()
	s.flush(context.Background())
	if took := time.Since(start); took > 200*time.Millisecond || !slices.Equal(console.batchSizes(), []int{3}) {
		t.Errorf("flush took %v; batches %v", took, console.batchSizes())
	}
	s.close(context.Background())
}

func TestFlushReturnsWithinItsBudgetAgainstAHungCollector(t *testing.T) {
	release := make(chan struct{})
	console := newFakeConsole(t, func(int) int { <-release; return http.StatusAccepted })
	t.Cleanup(func() { close(release) })
	s := batchingSink(console)
	s.budget = 50 * time.Millisecond
	s.write(testEvent(0))
	start := time.Now()
	s.flush(nil)
	if took := time.Since(start); took < s.budget || took > s.budget+200*time.Millisecond {
		t.Errorf("flush took %v", took)
	}
	s.close(context.Background())
}

func TestA413SplitsTheBatchInTwo(t *testing.T) {
	captureLogs(t)
	console := newFakeConsole(t, nil)
	console.maxEvents = 2
	s := batchingSink(console)
	for n := range 5 {
		s.write(testEvent(n))
	}
	s.close(context.Background())
	// 5 is cut into 2 and 3, and the 3 into 1 and 2.
	if got := console.batchSizes(); !slices.Equal(got, []int{5, 2, 3, 1, 2}) {
		t.Errorf("batches %v", got)
	}
	if got := console.sequences(); !slices.Equal(got, []float64{0, 1, 2, 3, 4, 0, 1, 2, 3, 4, 2, 3, 4}) || len(processDrops.totals()) != 0 {
		t.Errorf("sequences %v, counts %v", got, processDrops.totals())
	}
}

func TestACollectorWithoutTheBatchEndpointGetsOneEventPerRequest(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			captureLogs(t)
			console := newFakeConsole(t, nil)
			console.noBatch = status
			s := batchingSink(console)
			for n := range 3 {
				s.write(testEvent(n))
			}
			s.flush(context.Background())
			s.write(testEvent(3))
			s.close(context.Background())
			for i, r := range console.requests {
				if r.URL.Path != "/v0/events" {
					t.Errorf("request %d went to %s", i, r.URL.Path)
				}
			}
			if got := console.sequences(); !slices.Equal(got, []float64{0, 1, 2, 3}) || len(processDrops.totals()) != 0 {
				t.Errorf("sequences %v, counts %v", got, processDrops.totals())
			}
		})
	}
}

func TestA429WaitsForItsRetryAfter(t *testing.T) {
	captureLogs(t)
	console := newFakeConsole(t, func(n int) int {
		if n == 0 {
			return http.StatusTooManyRequests
		}
		return http.StatusAccepted
	})
	console.retryAfter = "1"
	s := fastSink(console)
	start := time.Now()
	s.write(testEvent(0))
	waitFor(t, "the 429", func() bool { return console.count() == 1 })
	time.Sleep(100 * time.Millisecond)
	s.write(testEvent(1)) // a write does not cut the wait short
	time.Sleep(100 * time.Millisecond)
	if console.count() != 1 {
		t.Fatalf("%d requests during the wait, want 1", console.count())
	}
	waitFor(t, "the retry", func() bool { return console.count() == 2 })
	if took := time.Since(start); took < time.Second {
		t.Errorf("retried after %v, want 1s", took)
	}
	s.close(context.Background())
	if got := console.sequences(); !slices.Equal(got, []float64{0, 0, 1}) || len(processDrops.totals()) != 0 {
		t.Errorf("sequences %v, counts %v", got, processDrops.totals())
	}
}

func TestOneRequestIsInFlightAtATime(t *testing.T) {
	captureLogs(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	console := newFakeConsole(t, func(int) int { <-release; return http.StatusAccepted })
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	s := fastSink(console)
	for n := range 3 * batchCap {
		s.write(testEvent(n))
	}
	waitFor(t, "the first request", func() bool { return console.count() >= 1 })
	time.Sleep(50 * time.Millisecond)
	if console.count() != 1 {
		t.Fatalf("%d requests while the first is held", console.count())
	}
	releaseOnce.Do(func() { close(release) })
	s.close(context.Background())
	if console.mostActive != 1 || console.eventCount() != 3*batchCap {
		t.Errorf("%d requests at once, %d events", console.mostActive, console.eventCount())
	}
}

func TestCloseKeepsItsBudgetAgainstAHungCollector(t *testing.T) {
	logs := captureLogs(t)
	release := make(chan struct{})
	console := newFakeConsole(t, func(int) int { <-release; return http.StatusAccepted })
	t.Cleanup(func() { close(release) })
	s := batchingSink(console)
	s.budget = 100 * time.Millisecond
	for n := range 5 {
		s.write(testEvent(n))
	}
	start := time.Now()
	s.close(nil)
	if took := time.Since(start); took < s.budget || took > s.budget+200*time.Millisecond {
		t.Errorf("close took %v", took)
	}
	<-s.done
	if got := processDrops.totals(); !maps.Equal(got, map[dropReason]int{dropUnsentAtClose: 5}) || !strings.Contains(logs.String(), "unsent=5") {
		t.Errorf("counts %v; logs: %s", got, logs)
	}
}
