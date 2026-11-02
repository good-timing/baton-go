package baton

import (
	"context"
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
	s := fastSink(console)
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

// reportIDs is how many different reports arrived: a failed one is retried
// under its own event_id.
func (c *fakeConsole) reportIDs() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := map[any]bool{}
	for _, r := range c.reports {
		ids[r["event_id"]] = true
	}
	return len(ids)
}

func TestAFailedReportIsNotCountedAndIsSentAgain(t *testing.T) {
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

func TestACollectorThatRefusesReportsGetsNoMore(t *testing.T) {
	for _, status := range []int{400, 422} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			logs := captureLogs(t)
			s, console := reportSink(t, []int{400, 202, 400, 202}, status)
			s.reportEvery = 0
			sendAll(t, s, 4)
			if console.reportCount() != 1 || processDrops.totals()[dropRefused] != 2 {
				t.Errorf("%d reports, counts %v", console.reportCount(), processDrops.totals())
			}
			if n := strings.Count(logs.String(), "REFUSED"); n != 2 {
				t.Errorf("%d refusals logged, want 2", n)
			}
		})
	}
}

func TestAReportRefusedInABatchStopsReports(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 202, 400, 202}, 0)
	s.reportEvery = 0
	console.refuse = func(e map[string]any) bool { return e["event_type"] == "client_report" }
	sendAll(t, s, 1)
	writeBesideAReport(t, s, console)
	s.flushEvery = 0
	sendAll(t, s, 2)
	if console.count() != 5 || console.reportCount() != 1 || console.reportsAlone != 0 {
		t.Errorf("%d requests, %d reports, %d alone; want 5, 1, 0", console.count(), console.reportCount(), console.reportsAlone)
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

func TestAReportRidesTheNextBatch(t *testing.T) {
	captureLogs(t)
	s, console := reportSink(t, []int{400, 202, 202}, 0)
	sendAll(t, s, 1)
	writeBesideAReport(t, s, console)
	if console.count() != 3 || console.reportCount() != 1 || console.reportsAlone != 0 {
		t.Errorf("%d requests, %d reports, %d alone; want 3, 1, 0", console.count(), console.reportCount(), console.reportsAlone)
	}
}
