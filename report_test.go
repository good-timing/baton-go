package baton

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// reportSink is a sink whose events are answered by statuses in order and
// whose reports by reportStatus.
func reportSink(t *testing.T, statuses []int, reportStatus int) (*httpSink, *fakeConsole) {
	t.Helper()
	freshDrops(t)
	console := newFakeConsole(t, func(n int) int { return statuses[n] })
	console.reportStatus = reportStatus
	s := fastSink(t, console)
	t.Cleanup(func() { s.close(context.Background()) })
	return s, console
}

func tenantEvent(n int) Event {
	e := testEvent(n)
	e.TenantID, e.VendorID, e.ConsentToken = "t", "v", "ct"
	return e
}

func reportIdle(s *httpSink) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.report == nil
}

// sendAll writes n events, letting each send and any report it starts end.
func sendAll(t *testing.T, s *httpSink, n int) {
	t.Helper()
	for i := range n {
		s.write(tenantEvent(i))
		waitFor(t, "a send", func() bool { return idle(s) && reportIdle(s) })
	}
}

func (c *fakeConsole) reportAt(i int) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reports[i]
}

func (c *fakeConsole) reportCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reports)
}

func TestADeliveryAfterALossSendsTheTotals(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202}, 0)
	sendAll(t, s, 2)
	if console.reportCount() != 1 {
		t.Fatalf("%d reports, want 1", console.reportCount())
	}
	r := console.reportAt(0)
	payload := r["payload"].(map[string]any)
	dropped := payload["dropped"].(map[string]any)
	if payload["instance_id"] != processDrops.instanceID || r["session_id"] != processDrops.instanceID ||
		len(dropped) != 1 || dropped["refused"] != float64(1) {
		t.Errorf("report %v", r)
	}
	if r["tenant_id"] != "t" || r["vendor_id"] != "v" || r["consent_token"] != "ct" || r["sequence_number"].(float64) <= 0 {
		t.Errorf("envelope %v", r)
	}
	for _, absent := range []string{
		"call_id", "principal", "organization", "client_observed", "transport_observed", "session_id_origin", "runtime_meta",
	} {
		if r[absent] != nil {
			t.Errorf("%s = %v, want null", absent, r[absent])
		}
	}
}

func TestAHealthyRunSendsNoReport(t *testing.T) {
	s, console := reportSink(t, []int{202, 202, 202}, 0)
	sendAll(t, s, 3)
	if console.reportCount() != 0 {
		t.Errorf("%d reports, want 0", console.reportCount())
	}
}

func TestAtMostOneReportAMinute(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 400, 202}, 0)
	sendAll(t, s, 4)
	if console.reportCount() != 1 {
		t.Errorf("%d reports, want 1", console.reportCount())
	}
}

func TestANewLossIsReportedOnceTheIntervalPassed(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 202, 400, 202}, 0)
	s.reportEvery = 0
	sendAll(t, s, 5)
	if console.reportCount() != 2 {
		t.Fatalf("%d reports, want 2", console.reportCount())
	}
	last := console.reportAt(1)["payload"].(map[string]any)["dropped"].(map[string]any)
	if last["refused"] != float64(2) {
		t.Errorf("second report %v", last)
	}
}

// reportIDs is how many different reports arrived: a report kept with its
// batch is retried under its own event_id.
func (c *fakeConsole) reportIDs() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := map[any]bool{}
	for _, r := range c.reports {
		ids[r["event_id"]] = true
	}
	return len(ids)
}

func TestAFailedLoneReportIsNotCountedAndANewOneFollows(t *testing.T) {
	for _, status := range []int{500, 401, reportDrop} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			captureLogs(t)
			s, console := reportSink(t, []int{400, 202, 202}, status)
			s.reportEvery = 0
			sendAll(t, s, 3)
			if console.reportIDs() != 2 || processDrops.totals()[dropRefused] != 1 || len(processDrops.totals()) != 1 {
				t.Errorf("%d reports, counts %v", console.reportIDs(), processDrops.totals())
			}
		})
	}
}

// On the one-event path a report goes alone even when it was taken beside an
// event; one that fails must not pause events the collector accepts.
func TestALoneReportThatFailsDoesNotPauseEvents(t *testing.T) {
	for _, status := range []int{500, reportHang} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			captureLogs(t)
			statuses := make([]int, 1+2*breakerThreshold)
			statuses[0] = 400
			for i := 1; i < len(statuses); i++ {
				statuses[i] = 202
			}
			s, console := reportSink(t, statuses, status)
			console.noBatch = http.StatusNotFound
			s.client.Timeout = 20 * time.Millisecond
			sendAll(t, s, 1)
			s.flushEvery = time.Hour
			s.reportDue(tenantEvent(0))
			for n := 1; n <= 2*breakerThreshold; n++ {
				s.write(tenantEvent(n))
				s.flush(context.Background())
				if console.eventCount() != n+1 {
					t.Fatalf("event %d not sent, after %d report tries", n, console.reportCount())
				}
			}
		})
	}
}

// A report waiting when close begins is not sent (SPEC §11.4.7), even
// beside the events close sends.
func TestCloseDropsAWaitingReport(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202}, 0)
	sendAll(t, s, 1)
	s.flushEvery = time.Hour
	s.reportDue(tenantEvent(1))
	if reportIdle(s) {
		t.Fatal("no report is waiting")
	}
	s.write(tenantEvent(1))
	s.close(context.Background())
	if console.eventCount() != 2 || console.reportCount() != 0 {
		t.Errorf("%d events, %d reports sent at close; want 2, 0", console.eventCount(), console.reportCount())
	}
}

// A whole-batch 400 or 422 stops reports only on the one-event path
// (TestACollectorWithoutTheBatchEndpointCanStillRefuseReports).
func TestAWholeBatchRefusalOfALoneReportDoesNotStopReports(t *testing.T) {
	for _, status := range []int{400, 422} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			logs := captureLogs(t)
			s, console := reportSink(t, []int{400, 202, 400, 202}, status)
			s.reportEvery = 0
			sendAll(t, s, 4)
			if console.reportCount() != 2 || processDrops.totals()[dropRefused] != 2 {
				t.Errorf("%d reports, counts %v; want 2 and 2 refused", console.reportCount(), processDrops.totals())
			}
			if n := strings.Count(logs.String(), "REFUSED"); n != 1 {
				t.Errorf("%d refusals logged, want 1 a minute", n)
			}
		})
	}
}

// A report refused for another reason is sent again after the next delivery.
// Of several refusals of it, the last one's code counts.
func TestAReportRefusedInABatchStopsReportsOnlyAsMalformed(t *testing.T) {
	for _, c := range []struct {
		codes   []string
		reports int
	}{
		{[]string{"payload_malformed"}, 1},
		{[]string{"quota_exhausted"}, 3},
		{[]string{"payload_malformed", "quota_exhausted"}, 3},
		{[]string{"quota_exhausted", "payload_malformed"}, 1},
	} {
		t.Run(strings.Join(c.codes, ","), func(t *testing.T) {
			captureLogs(t)
			s, console := reportSink(t, []int{400, 202, 202, 400, 202}, 0)
			s.reportEvery = 0
			console.refuse = func(e map[string]any) bool { return e["event_type"] == "client_report" }
			console.refuseCodes = c.codes
			sendAll(t, s, 1)
			writeBesideAReport(t, s, console)
			s.flushEvery = 0
			sendAll(t, s, 2)
			if console.count() != 5 || console.reportCount() != c.reports {
				t.Errorf("%d requests, %d reports; want 5, %d", console.count(), console.reportCount(), c.reports)
			}
		})
	}
}

// Another event in the batch may be what the collector refused.
func TestAWholeBatchRefusalOfEventsWithAReportDoesNotStopReports(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 400, 400, 202}, 0)
	s.reportEvery = 0
	sendAll(t, s, 1)
	writeBesideAReport(t, s, console)
	s.flushEvery = 0
	sendAll(t, s, 2)
	if console.reportCount() != 2 || console.reportsAlone != 1 {
		t.Errorf("%d reports, %d alone; want 2, 1", console.reportCount(), console.reportsAlone)
	}
}

func TestAReportRidesABatchWhileMoreEventsWait(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 202, 202}, 0)
	sendAll(t, s, 1)
	s.flushEvery = time.Second
	s.write(tenantEvent(1))
	waitFor(t, "the delivery", func() bool { return !reportIdle(s) })
	// Two of these fill a batch by bytes, so the third waits for the next.
	for i := range 3 {
		s.write(bigEvent(2+i, 400_000))
	}
	waitFor(t, "both batches", func() bool { return console.count() == 4 && idle(s) })
	if got := console.batchSizes(); !slices.Equal(got, []int{1, 1, 3, 1}) {
		t.Errorf("entries per request %v, want [1 1 3 1]: the report in the first batch with room", got)
	}
}

func TestABatchKeptForALaterTryKeepsItsReport(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 500, 500, 500, 500, 202}, 0)
	sendAll(t, s, 1)
	s.flushEvery = 200 * time.Millisecond
	s.write(tenantEvent(1))
	waitFor(t, "the delivery", func() bool { return !reportIdle(s) })
	s.write(tenantEvent(2))
	waitFor(t, "the retry", func() bool { return console.count() == 7 && idle(s) })
	if got := console.batchSizes(); got[6] != 2 || console.reportIDs() != 1 {
		t.Errorf("entries per request %v, %d report ids; want the retry to carry the one report", got, console.reportIDs())
	}
}

// writeBesideAReport delivers one event by the timer, so the report it makes
// due waits too, then writes another, which goes in the report's batch.
func writeBesideAReport(t *testing.T, s *httpSink, console *fakeConsole) {
	t.Helper()
	s.flushEvery = 200 * time.Millisecond
	before := console.count()
	s.write(tenantEvent(1))
	waitFor(t, "the delivery", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.report != nil })
	s.write(tenantEvent(2))
	waitFor(t, "the batch", func() bool { return console.count() == before+2 && idle(s) })
}

func TestCloseEndsAReportBeingSent(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 202}, reportHang)
	s.reportEvery = 0
	sendAll(t, s, 1)
	s.write(tenantEvent(1))
	waitFor(t, "the report", func() bool { return console.reportCount() == 1 })
	s.write(tenantEvent(2))
	time.Sleep(50 * time.Millisecond)
	if console.count() != 2 {
		t.Fatalf("%d requests while the report hangs, want 2: one in flight at a time", console.count())
	}
	start := time.Now()
	s.close(context.Background())
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("close took %v", took)
	}
	if console.count() != 3 || processDrops.totals()[dropRefused] != 1 || len(processDrops.totals()) != 1 {
		t.Errorf("%d requests, counts %v", console.count(), processDrops.totals())
	}
}

func TestAReportThatGetsNoAnswerTimesOut(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 202}, reportHang)
	s.reportEvery = 0
	s.client.Timeout = 20 * time.Millisecond
	sendAll(t, s, 3)
	if console.reportIDs() != 2 {
		t.Errorf("%d reports, want 2", console.reportIDs())
	}
}

func TestNoReportStartsOnceCloseBegan(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400}, 0)
	s.reportEvery = 0
	sendAll(t, s, 1)
	s.close(context.Background())
	s.reportDue(testEvent(2))
	if !reportIdle(s) || console.reportCount() != 0 {
		t.Errorf("a report started after close")
	}
}

func TestACollectorWithoutTheBatchEndpointCanStillRefuseReports(t *testing.T) {
	logs := captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 400, 202}, 400)
	console.noBatch = http.StatusNotFound
	s.reportEvery = 0
	sendAll(t, s, 4)
	if console.reportCount() != 1 || processDrops.totals()[dropRefused] != 2 {
		t.Errorf("%d reports, counts %v", console.reportCount(), processDrops.totals())
	}
	if n := strings.Count(logs.String(), "REFUSED"); n != 1 {
		t.Errorf("%d refusals logged, want 1 a minute", n)
	}
}

func TestAReportRidesTheNextBatch(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 202}, 0)
	sendAll(t, s, 1)
	writeBesideAReport(t, s, console)
	if console.count() != 3 || console.reportCount() != 1 || console.reportsAlone != 0 {
		t.Errorf("%d requests, %d reports, %d alone; want 3, 1, 0", console.count(), console.reportCount(), console.reportsAlone)
	}
}

func TestABatchKeptAfterCloseBeganLosesItsReport(t *testing.T) {
	captureLogs(t)
	release := make(chan struct{})
	s, console := reportSink(t, nil, 0)
	console.answer = func(n int) int {
		switch {
		case n == 0:
			return 400
		case n == 2:
			<-release
		}
		if n >= 2 && n <= 5 {
			return 500
		}
		return 202
	}
	sendAll(t, s, 1)
	s.flushEvery = 200 * time.Millisecond
	s.write(tenantEvent(1))
	waitFor(t, "the delivery", func() bool { return !reportIdle(s) })
	s.write(tenantEvent(2))
	waitFor(t, "the batch with the report", func() bool { return console.count() == 3 })
	closed := make(chan struct{})
	go func() { s.close(context.Background()); close(closed) }()
	waitFor(t, "close", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.closed })
	close(release)
	<-closed
	if got := console.batchSizes(); len(got) != 7 || got[6] != 1 {
		t.Errorf("entries per request %v; want the last try at close without the report", got)
	}
}

func TestADueReportTakesAPlaceInABatchFullByCount(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 202, 202, 202, 202}, 0)
	sendAll(t, s, 1)
	s.flushEvery = time.Second
	s.write(tenantEvent(1))
	waitFor(t, "the delivery", func() bool { return !reportIdle(s) })
	for i := range 3 * batchCap {
		s.write(tenantEvent(2 + i))
	}
	waitFor(t, "every event", func() bool { return console.eventCount() == 2+3*batchCap && idle(s) && reportIdle(s) })
	if console.reportCount() != 1 || console.reportsAlone != 0 || console.batchSizes()[2] != batchCap {
		t.Errorf("%d reports, %d alone, entries per request %v; want the report in the first full batch",
			console.reportCount(), console.reportsAlone, console.batchSizes())
	}
}

func TestADueReportKeepsByteRoomInABatch(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 202, 202, 202}, 0)
	sendAll(t, s, 1)
	s.flushEvery = time.Second
	s.write(tenantEvent(1))
	waitFor(t, "the delivery", func() bool { return !reportIdle(s) })
	// Two of these fill a batch to 10 bytes under the cap, with no room for the report.
	probe, _ := encodeEvent(bigEvent(2, 0))
	size := (batchBytes-(batchFrame-1)-10)/2 - 1 - len(probe)
	s.write(bigEvent(2, size))
	s.write(bigEvent(3, size))
	waitFor(t, "every event", func() bool { return console.eventCount() == 4 && idle(s) && reportIdle(s) })
	if got := console.batchSizes(); !slices.Equal(got, []int{1, 1, 2, 1}) {
		t.Errorf("entries per request %v, want [1 1 2 1]: one event beside the report, then the other", got)
	}
}

// The report built when the first half of a split batch is delivered has the
// newer totals; the older one the second half carried must not replace it.
func TestASplitBatchKeepsTheNewerReport(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, nil, 0)
	console.answer = func(n int) int {
		switch {
		case n == 0:
			return 400
		case n >= 4 && n <= 7:
			return 500
		}
		return 202
	}
	console.maxEvents = 4
	console.refuse = func(e map[string]any) bool { return e["sequence_number"] == 2.0 }
	s.reportEvery = 0
	sendAll(t, s, 1)
	s.flushEvery = 200 * time.Millisecond
	s.write(tenantEvent(1))
	waitFor(t, "the delivery", func() bool { return !reportIdle(s) })
	for n := 2; n <= 5; n++ {
		s.write(tenantEvent(n))
	}
	waitFor(t, "the retry", func() bool { return console.count() == 9 && idle(s) })
	last := console.reportAt(console.reportCount() - 1)["payload"].(map[string]any)["dropped"].(map[string]any)
	if got := console.batchSizes(); got[8] != 3 || last["refused"] != float64(2) {
		t.Errorf("entries per request %v, last report %v; want the retry to carry refused 2", got, last)
	}
}

// A 413 split leaves the report alone in a request of its own; it was taken
// beside an event, so its failure keeps it.
func TestAReportSplitOffItsEventsIsKept(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 413, 202, 202}, 500)
	sendAll(t, s, 1)
	s.flushEvery = time.Second
	s.write(tenantEvent(1))
	waitFor(t, "the delivery", func() bool { return !reportIdle(s) })
	s.write(tenantEvent(2))
	waitFor(t, "the split", func() bool { return console.reportCount() == 2+maxRetries && idle(s) })
	console.mu.Lock()
	console.reportStatus = 0
	console.mu.Unlock()
	s.write(tenantEvent(3))
	waitFor(t, "the next batch", func() bool { return console.count() == 5 && idle(s) && reportIdle(s) })
	if got := console.batchSizes(); got[4] != 2 || console.reportIDs() != 1 {
		t.Errorf("entries per request %v, %d report ids; want the report beside the next event", got, console.reportIDs())
	}
}
