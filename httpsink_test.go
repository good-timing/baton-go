package baton

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeConsole struct {
	mu           sync.Mutex
	requests     []*http.Request
	events       []map[string]any
	reports      []map[string]any
	reportStatus int
	answer       func(n int) int
	url          string
}

// reportStatus values that answer no status: the connection is dropped, or
// the request is held until the client gives up.
const (
	reportDrop = -1
	reportHang = -2
)

// newFakeConsole answers the nth event request (from 0) with answer(n), or
// 202. A client_report is kept apart, in reports, and answered with
// reportStatus, or 202. Its URL is a DSN's origin.
func newFakeConsole(t *testing.T, answer func(n int) int) *fakeConsole {
	t.Helper()
	c := &fakeConsole{answer: answer}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event map[string]any
		_ = json.NewDecoder(r.Body).Decode(&event)
		c.mu.Lock()
		if event["event_type"] == "client_report" {
			c.reports = append(c.reports, event)
			status := cmp.Or(c.reportStatus, http.StatusAccepted)
			c.mu.Unlock()
			switch status {
			case reportDrop:
				panic(http.ErrAbortHandler)
			case reportHang:
				<-r.Context().Done()
				return
			}
			w.WriteHeader(status)
			return
		}
		n := len(c.requests)
		c.requests = append(c.requests, r)
		c.events = append(c.events, event)
		c.mu.Unlock()
		status := http.StatusAccepted
		if c.answer != nil {
			status = c.answer(n)
		}
		w.WriteHeader(status)
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
	return Event{EventType: "tool_call_start", SequenceNumber: int64(n)}
}

func fastSink(c *fakeConsole) *httpSink {
	s := newHTTPSink(c.url, testKey)
	s.backoff = time.Millisecond
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

	var types []string
	for i, r := range console.requests {
		if r.Method != http.MethodPost || r.URL.Path != "/v0/events" || r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("request %d: %s %s, Authorization %q", i, r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		e := console.events[i]
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
	console := newFakeConsole(t, func(n int) int {
		if n == 0 {
			return http.StatusBadRequest
		}
		return http.StatusAccepted
	})
	logs := captureLogs(t)
	s := fastSink(console)
	for n := range 3 {
		s.write(testEvent(n))
	}
	s.close(context.Background())
	if console.count() != 3 {
		t.Errorf("%d requests, want 3", console.count())
	}
	if !strings.Contains(logs.String(), "REFUSED") || strings.Contains(logs.String(), "unsent") {
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
	if !s.putBack(testEvent(0)) || len(s.queue) != bufferCap || s.queue[0].SequenceNumber != 1 {
		t.Errorf("%d queued, oldest %d", len(s.queue), s.queue[0].SequenceNumber)
	}
	s.popFront()
	if s.putBack(testEvent(0)) || s.queue[0].SequenceNumber != 0 {
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
	if got := console.count() - breakerThreshold*(maxRetries+1); got != breakerThreshold+1 {
		t.Errorf("%d requests after the reset, want %d", got, breakerThreshold+1)
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
