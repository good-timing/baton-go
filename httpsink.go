package baton

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SPEC §2.5 and §11.2 rules 3, 4 and 7 set these.
const (
	bufferCap        = 1000
	batchCap         = 100
	batchBytes       = 1 << 20
	flushInterval    = 5 * time.Second
	requestTimeout   = time.Second
	maxRetries       = 3
	backoffBase      = 100 * time.Millisecond
	backoffMax       = 5 * time.Second
	retryAfterMin    = time.Second
	retryAfterMax    = time.Minute
	breakerThreshold = 5
	breakerReset     = 30 * time.Second
	closeBudget      = 3 * time.Second
	refusalBodyCap   = 500
	answerCap        = 1 << 20
	reportEvery      = time.Minute
)

const (
	batchOpen  = `{"events":[`
	batchClose = `]}`
	batchFrame = len(batchOpen) + len(batchClose)
)

var reportSequence microClock

// queued is an event waiting to be sent, or a loss report riding the next batch.
type queued struct {
	Event
	written time.Time
	// Set by the sending goroutine, so encoding never runs on the vendor's call.
	body      []byte
	encodeErr error
	isReport  bool
	reportSum int
}

// cost is the bytes q adds to a batch body, with its comma.
func (q *queued) cost() int { return 1 + len(q.body) }

// httpSink posts batches of events from a single goroutine, one request at a
// time, so a slow or broken console never slows the vendor's tool calls.
type httpSink struct {
	origin string
	auth   string
	client *http.Client

	mu       sync.Mutex
	queue    []*queued
	inFlight []*queued
	closed   bool
	// The queue's entries from absolute position head up to sized have their
	// cost known, and sizedBytes is its sum, so a wake sizes only new events.
	head, sized, sizedBytes int

	overflowWarned sync.Once
	lateWarned     sync.Once

	wake    chan struct{}
	flushes chan chan struct{}
	stop    chan struct{}
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	// Ends a request that carries only a report: close does not wait for one.
	reportCtx     context.Context
	cancelReports context.CancelFunc
	closeOnce     sync.Once

	// Owned by the sending goroutine.
	single      bool // the collector has no batch endpoint
	failures    int
	openedAt    time.Time
	retryAt     time.Time // after a failure; a flush or close need not wait
	rateLimited time.Time // a 429's Retry-After; nothing is sent before it
	lastProblem string

	backoff    time.Duration
	reset      time.Duration
	budget     time.Duration
	flushEvery time.Duration

	// Loss reports (SPEC §11.4.7), guarded by mu.
	reportEvery    time.Duration
	lastReport     time.Time
	report         *queued // due, and not yet in a batch
	reportedSum    int
	reportsRefused bool
}

func newHTTPSink(origin, key string) *httpSink {
	ctx, cancel := context.WithCancel(context.Background())
	reportCtx, cancelReports := context.WithCancel(ctx)
	s := &httpSink{
		origin: origin,
		auth:   "Bearer " + key,
		client: &http.Client{
			Timeout: requestTimeout,
			// Following a redirect turns the POST into a GET without the events.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		wake:          make(chan struct{}, 1),
		flushes:       make(chan chan struct{}),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
		ctx:           ctx,
		cancel:        cancel,
		reportCtx:     reportCtx,
		cancelReports: cancelReports,
		backoff:       backoffBase,
		reset:         breakerReset,
		budget:        closeBudget,
		flushEvery:    flushInterval,

		reportEvery: reportEvery,
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
		processDrops.add(dropAfterClose, 1)
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
		processDrops.add(dropBufferFull, 1)
	}
	s.queue = append(s.queue, &queued{Event: e, written: time.Now()})
	return full
}

// putBack returns a failed batch, which is older than everything queued, to
// the front, and its report to s.report unless the report was sent alone.
// When that overfills the queue, the batch's oldest are the ones dropped. It
// returns how many it dropped. The caller holds s.mu.
func (s *httpSink) putBack(batch []*queued, sentAlone bool) int {
	events := make([]*queued, 0, len(batch))
	var report *queued
	for _, q := range batch {
		if q.isReport {
			report = q
		} else {
			events = append(events, q)
		}
	}
	// A report that failed alone is not kept, or it would be sent alone again
	// and again to a collector that fails it, as on the one-event path, where
	// every report goes alone. A report built after this one, when part of
	// the batch was delivered, has the newer totals.
	if report != nil && !sentAlone && s.report == nil && !s.closed {
		s.report = report
	}
	over := max(0, len(s.queue)+len(events)-bufferCap)
	processDrops.add(dropBufferFull, over)
	kept := events[over:]
	s.queue = slices.Insert(s.queue, 0, kept...)
	s.head -= len(kept)
	for _, q := range kept {
		s.sizedBytes += q.cost()
	}
	return over
}

// popFront clears the slot so the popped event's payload can be collected.
// The caller holds s.mu.
func (s *httpSink) popFront() *queued {
	q := s.queue[0]
	s.queue[0] = nil
	s.queue = s.queue[1:]
	if s.head < s.sized {
		s.sizedBytes -= q.cost()
	}
	s.head++
	s.sized = max(s.sized, s.head)
	return q
}

func (s *httpSink) warnOverflow() {
	s.overflowWarned.Do(func() {
		slog.Warn("baton: the event buffer is full; the oldest events are dropped. Further overflows are silent",
			"cap", bufferCap)
	})
}

func (s *httpSink) run() {
	defer close(s.done)
	due := time.NewTimer(time.Hour)
	due.Stop()
	for {
		var flushed chan struct{}
		select {
		case <-s.wake:
		case <-due.C:
		case flushed = <-s.flushes:
		case <-s.stop:
			s.drain(true)
			return
		}
		if wait := s.drain(flushed != nil); wait > 0 {
			due.Reset(wait)
		} else {
			due.Stop()
		}
		if flushed != nil {
			close(flushed)
		}
	}
}

// drain sends every batch that is due, or everything when force is set, and
// returns how long until it should run again; 0 means nothing is waiting.
func (s *httpSink) drain(force bool) time.Duration {
	for {
		if !s.breakerAllows() {
			return time.Until(s.openedAt.Add(s.reset))
		}
		if wait := time.Until(s.rateLimited); wait > 0 {
			return wait
		}
		if wait := time.Until(s.retryAt); wait > 0 && !force {
			return wait
		}
		batch, wait := s.nextBatch(force)
		if batch == nil {
			return wait
		}
		kept, failed := s.deliver(batch)

		s.mu.Lock()
		s.inFlight = nil
		stopped := failed && s.ctx.Err() != nil
		dropped := 0
		if failed && !stopped {
			// A 413 split can leave a report alone that was taken beside events.
			dropped = s.putBack(kept, s.single || countEvents(batch) == 0)
		}
		s.mu.Unlock()
		if stopped {
			processDrops.add(dropUnsentAtClose, countEvents(kept))
		}
		if dropped > 0 {
			s.warnOverflow()
		}
		if !failed {
			s.failures = 0
			s.openedAt = time.Time{}
			s.retryAt = time.Time{}
			continue
		}
		if time.Now().Before(s.rateLimited) {
			continue
		}
		s.failures++
		if s.failures >= breakerThreshold {
			s.openedAt = time.Now()
			slog.Warn("baton: the console is failing; events are kept and sending pauses",
				"pause", s.reset, "last_problem", s.lastProblem)
		}
		// The floor keeps a zero interval from retrying in a tight loop.
		s.retryAt = time.Now().Add(max(s.flushEvery, s.backoff))
		return time.Until(s.retryAt)
	}
}

// sizeNew encodes the queued events not sized yet, among the first batchCap,
// and any due report, outside s.mu, and adds the events' cost to sizedBytes.
func (s *httpSink) sizeNew() {
	s.mu.Lock()
	from := s.sized
	report := s.report
	var todo []*queued
	if lo, hi := from-s.head, min(len(s.queue), batchCap); lo < hi {
		todo = slices.Clone(s.queue[lo:hi])
	}
	s.mu.Unlock()
	if report != nil {
		s.encode(report)
	}
	if len(todo) == 0 {
		return
	}
	for _, q := range todo {
		s.encode(q)
	}
	s.mu.Lock()
	// Write may have dropped some meanwhile; those were never in sizedBytes.
	for i, q := range todo {
		if from+i >= s.head {
			s.sizedBytes += q.cost()
		}
	}
	s.sized = max(s.sized, from+len(todo))
	s.mu.Unlock()
}

// nextBatch takes the next batch off the queue when one is due, and otherwise
// returns how long until the oldest waiting entry makes one due.
func (s *httpSink) nextBatch(force bool) ([]*queued, time.Duration) {
	s.sizeNew()
	s.mu.Lock()
	// Once close has given up, what is queued is close's to count.
	if s.ctx.Err() != nil || len(s.queue) == 0 && s.report == nil {
		s.mu.Unlock()
		return nil, 0
	}
	oldest := s.report
	if len(s.queue) > 0 {
		oldest = s.queue[0]
	}
	full := s.sized-s.head >= batchCap || batchFrame-1+s.sizedBytes > batchBytes
	if wait := time.Until(oldest.written.Add(s.flushEvery)); !force && !full && wait > 0 {
		s.mu.Unlock()
		return nil, wait
	}

	var batch, unencodable, oversize []*queued
	size := batchFrame - 1
	// A due report takes one of the batch's places (SPEC §11.4.7), or it
	// would never go while events fill every batch.
	places, room := batchCap, batchBytes
	if r := s.report; r != nil && r.body != nil {
		places, room = batchCap-1, batchBytes-r.cost()
	}
take:
	for len(s.queue) > 0 && len(batch) < places {
		q := s.queue[0]
		switch {
		case q.body == nil && q.encodeErr == nil:
			break take // past the sized ones; the next batch takes it
		case q.body == nil:
			unencodable = append(unencodable, q)
		case batchFrame-1+q.cost() > batchBytes:
			oversize = append(oversize, q)
		case size+q.cost() > room:
			break take
		default:
			batch = append(batch, q)
			size += q.cost()
		}
		s.popFront()
	}
	if report := s.report; report != nil {
		s.report = nil
		if report.body != nil {
			batch = append(batch, report)
		}
	}
	s.inFlight = batch
	s.mu.Unlock()
	for _, q := range unencodable {
		processDrops.add(dropEncodeFailed, 1)
		slog.Warn("baton: an event could not be encoded and has been dropped; the events after it are still sent",
			"event_type", q.EventType, "error_type", fmt.Sprintf("%T", q.encodeErr))
	}
	for _, q := range oversize {
		processDrops.add(dropRefused, 1)
		slog.Warn("baton: an event is over the 1 MiB batch cap, which a collector refuses, so it has been dropped unsent",
			"event_type", q.EventType, "bytes", len(q.body))
	}
	// Every event taken was unencodable or over the cap.
	if batch == nil {
		return s.nextBatch(force)
	}
	return batch, 0
}

func (s *httpSink) encode(q *queued) {
	if q.body == nil && q.encodeErr == nil {
		q.body, q.encodeErr = encodeEvent(q.Event)
	}
}

// countEvents leaves out the loss report: a report is never counted as lost
// (SPEC §11.4.7).
func countEvents(batch []*queued) int {
	n := 0
	for _, q := range batch {
		if !q.isReport {
			n++
		}
	}
	return n
}

// deliver sends batch, splitting it on 413 and switching to one event per
// request on the batch path's 404 or 405. On a transient failure it returns
// what it did not deliver, in order.
func (s *httpSink) deliver(batch []*queued) (kept []*queued, failed bool) {
	if s.single && len(batch) > 1 {
		for i := range batch {
			if _, failed := s.deliver(batch[i : i+1]); failed {
				return batch[i:], true
			}
		}
		return nil, false
	}
	status, answer, ok := s.post(batch)
	switch {
	case !ok:
		return batch, true
	case status >= 200 && status < 300:
		s.settle(batch, answer)
	case !s.single && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed):
		s.single = true
		slog.Info("baton: the collector has no batch endpoint; sending one event per request")
		return s.deliver(batch)
	case status == http.StatusRequestEntityTooLarge && len(batch) > 1:
		half := len(batch) / 2
		if kept, failed := s.deliver(batch[:half]); failed {
			return slices.Concat(kept, batch[half:]), true
		}
		return s.deliver(batch[half:])
	default:
		s.refuse(batch, status, answer)
	}
	return nil, false
}

// jsonString is raw as a string, or "" when it is not one.
func jsonString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// jsonObject reads raw as an object. Unlike decoding into a struct, its keys
// match only in their exact case, as json.loads and JSON.parse match them.
func jsonObject(raw []byte) map[string]json.RawMessage {
	var o map[string]json.RawMessage
	_ = json.Unmarshal(raw, &o)
	return o
}

// refusals reads a batch answer's results (SPEC §2.5): the error code of each
// event_id with a "refused" result, which wins over any other result for it.
func refusals(answer []byte) map[string]string {
	codes := map[string]string{}
	var results []json.RawMessage
	_ = json.Unmarshal(jsonObject(bytes.TrimPrefix(answer, []byte("\xEF\xBB\xBF")))["results"], &results)
	for _, raw := range results {
		r := jsonObject(raw)
		id := jsonString(r["event_id"])
		if id == "" || jsonString(r["status"]) != "refused" {
			continue
		}
		codes[id] = cmp.Or(codes[id], jsonString(jsonObject(r["error"])["code"]))
	}
	return codes
}

// settle reads a 2xx answer's results by event_id. An event with no readable
// result counts as delivered (SPEC §2.5).
func (s *httpSink) settle(batch []*queued, answer []byte) {
	var codes map[string]string
	if !s.single {
		codes = refusals(answer)
	}
	var delivered *queued
	refused, code := 0, ""
	for _, q := range batch {
		refusedCode, isRefused := codes[q.EventID]
		switch {
		case q.isReport:
			s.mu.Lock()
			if isRefused {
				// A collector that predates client_report refuses it this way;
				// another code, such as quota_exhausted, is about this batch.
				s.reportsRefused = s.reportsRefused || refusedCode == "payload_malformed"
			} else {
				s.reportedSum = max(s.reportedSum, q.reportSum)
			}
			s.mu.Unlock()
		case isRefused:
			refused++
			code = cmp.Or(code, refusedCode)
		case delivered == nil:
			delivered = q
		}
	}
	if refused > 0 {
		warnRefused(refused, "code", code)
	}
	if delivered != nil {
		processDrops.logRecovery()
		s.reportDue(delivered.Event)
	}
}

func warnRefused(n int, args ...any) {
	processDrops.add(dropRefused, n)
	slog.Warn("baton: the collector REFUSED events; they have been dropped and are not sent again",
		append([]any{"events", n}, args...)...)
}

// refuse drops a batch the collector refused as a whole.
func (s *httpSink) refuse(batch []*queued, status int, answer []byte) {
	alone := s.single && len(batch) == 1 && batch[0].isReport
	if alone && (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) {
		// A collector that predates client_report refuses it this way. In a
		// batch, another event may be what the collector refused.
		s.mu.Lock()
		s.reportsRefused = true
		s.mu.Unlock()
	}
	if n := countEvents(batch); n > 0 {
		warnRefused(n, "status", status,
			"body", strings.ToValidUTF8(string(answer[:min(len(answer), refusalBodyCap)]), "?"))
	}
}

// reportDue queues a report of this process's loss totals for the next batch.
// The caller does not hold s.mu.
func (s *httpSink) reportDue(delivered Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.reportsRefused || s.report != nil || time.Since(s.lastReport) < s.reportEvery {
		return
	}
	totals := processDrops.totals()
	sum := 0
	for _, n := range totals {
		sum += n
	}
	// Totals only grow, so their sum tells whether they changed.
	if sum == s.reportedSum {
		return
	}
	s.lastReport = time.Now()
	s.report = &queued{
		Event: Event{
			EventID:        uuid7(),
			TenantID:       delivered.TenantID,
			VendorID:       delivered.VendorID,
			SessionID:      processDrops.instanceID,
			SequenceNumber: reportSequence.next(),
			CapturedAt:     time.Now().UTC().Format(time.RFC3339Nano),
			ConsentToken:   delivered.ConsentToken,
			SDKVersion:     sdkVersion,
			AgentRuntime:   "unknown",
			EventType:      "client_report",
			Payload:        clientReport{InstanceID: processDrops.instanceID, Dropped: totals},
		},
		written:   time.Now(),
		isReport:  true,
		reportSum: sum,
	}
}

func (s *httpSink) breakerAllows() bool {
	return s.openedAt.IsZero() || time.Since(s.openedAt) >= s.reset
}

type encodePanicked struct{}

func (encodePanicked) Error() string { return "encoding panicked" }

// encodeEvent turns a panic in a vendor value's MarshalJSON into an error: on
// this goroutine it would crash the vendor's process (TestAnEventThatPanicsWhenEncodedIsDropped).
func encodeEvent(e Event) (body []byte, err error) {
	defer func() {
		if recover() != nil {
			err = encodePanicked{}
		}
	}()
	return json.Marshal(e)
}

// post sends batch, retrying a transient failure with backoff, and returns
// the answer, or ok false when every try failed.
func (s *httpSink) post(batch []*queued) (status int, answer []byte, ok bool) {
	ctx := s.ctx
	if len(batch) == 1 && batch[0].isReport {
		ctx = s.reportCtx
	}
	url, body := s.origin+"/v0/events", batch[0].body
	if !s.single {
		url = s.origin + "/v0/events/batch"
		var b bytes.Buffer
		b.WriteString(batchOpen)
		for i, q := range batch {
			if i > 0 {
				b.WriteByte(',')
			}
			b.Write(q.body)
		}
		b.WriteString(batchClose)
		body = b.Bytes()
	}
	for attempt := 0; ; attempt++ {
		status, answer, wait, err := s.do(ctx, url, body)
		switch {
		case err != nil:
			s.lastProblem = err.Error()
		case status >= 200 && status < 300 || status >= 400 && status < 500 && status != http.StatusTooManyRequests:
			return status, answer, true
		default:
			s.lastProblem = fmt.Sprintf("%d %s", status, http.StatusText(status))
		}
		if wait > 0 {
			s.rateLimited = time.Now().Add(wait)
			return 0, nil, false
		}
		if attempt == maxRetries {
			return 0, nil, false
		}
		select {
		case <-time.After(min(s.backoff<<attempt, backoffMax)):
		case <-ctx.Done():
			return 0, nil, false
		}
	}
}

// do posts body and returns the status, the answer's body, and on a 429 the
// wait its Retry-After asks for. A 2xx whose body cannot be read is an error.
func (s *httpSink) do(ctx context.Context, url string, body []byte) (int, []byte, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("Authorization", s.auth)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, 0, err
	}
	answer, err := io.ReadAll(io.LimitReader(resp.Body, answerCap))
	// Reading to the end lets the connection be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if err != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return 0, nil, 0, err
	}
	var wait time.Duration
	if resp.StatusCode == http.StatusTooManyRequests {
		wait = retryAfter(resp.Header.Get("Retry-After"))
	}
	return resp.StatusCode, answer, wait, nil
}

// retryAfter reads a Retry-After as whole seconds (RFC 9110 delay-seconds),
// clamped to SPEC §2.5's range, or 0 for any other value, which makes the
// 429 a 5xx.
func retryAfter(header string) time.Duration {
	secs, err := strconv.ParseUint(strings.TrimSpace(header), 10, 64)
	if errors.Is(err, strconv.ErrRange) {
		return retryAfterMax
	}
	if err != nil {
		return 0
	}
	return max(time.Duration(min(secs, uint64(retryAfterMax/time.Second)))*time.Second, retryAfterMin)
}

// flush sends what is queued at once, and returns when that is done, at ctx's
// deadline, or after s.budget when ctx has none.
func (s *httpSink) flush(ctx context.Context) {
	ctx, cancel := s.withBudget(ctx)
	defer cancel()
	flushed := make(chan struct{})
	select {
	case s.flushes <- flushed:
	case <-s.done:
		return
	case <-ctx.Done():
		return
	}
	select {
	case <-flushed:
	case <-ctx.Done():
	}
}

func (s *httpSink) withBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, s.budget)
}

// close sends what is queued until ctx's deadline, or for s.budget when ctx
// has none. A second close waits for the first. When the console is failing,
// or during a 429's wait, close does not try again.
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
		// SPEC §11.4.7: no report at close; the collector is often why there is loss.
		s.report = nil
		s.mu.Unlock()
		s.cancelReports()
		ctx, cancel := s.withBudget(ctx)
		defer cancel()
		close(s.stop)
		select {
		case <-s.done:
		case <-ctx.Done():
		}
		s.cancel()
		s.client.CloseIdleConnections()
		s.mu.Lock()
		unsent = len(s.queue)
		inFlight := countEvents(s.inFlight)
		s.mu.Unlock()
		// Those being sent are counted by drain, when their send ends.
		processDrops.add(dropUnsentAtClose, unsent)
		unsent += inFlight
	})
}
