package baton

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestEachKindOfRefusalIsLoggedAtMostOnceAMinute(t *testing.T) {
	var r refusalLog
	start := time.Now()
	steps := []struct {
		kind         string
		n            int
		after        time.Duration
		wantLog      bool
		wantUnlogged int
	}{
		{"status 403", 2, 0, true, 0},
		{"status 403", 3, time.Second, false, 0},
		{"code quota_exhausted", 1, time.Second, true, 0},
		{"status 403", 1, refusalLogEvery, true, 3},
		{"status 403", 4, refusalLogEvery + time.Second, false, 0},
	}
	for i, step := range steps {
		unlogged, log := r.take(step.kind, step.n, start.Add(step.after))
		if log != step.wantLog || unlogged != step.wantUnlogged {
			t.Errorf("step %d: log %v, unlogged %d; want %v, %d", i, log, unlogged, step.wantLog, step.wantUnlogged)
		}
	}
	if n := r.unlogged(); n != 4 {
		t.Errorf("unlogged %d, want 4", n)
	}
	if n := r.unlogged(); n != 0 {
		t.Errorf("unlogged again %d, want 0", n)
	}
}

func TestACollectorRefusingEveryBatchIsLoggedOnceThenSummedAtClose(t *testing.T) {
	logs := captureLogs(t)
	s := batchingSink(t, newFakeConsole(t, func(int) int { return http.StatusForbidden }))
	for n := range 3 {
		s.write(testEvent(n))
		s.flush(context.Background())
	}
	s.close(context.Background())
	if n := strings.Count(logs.String(), "REFUSED"); n != 1 {
		t.Errorf("%d refusal warnings, want 1: %s", n, logs)
	}
	if !strings.Contains(logs.String(), "more events were refused since the last such warning\" events=2") {
		t.Errorf("no summary of the 2 unlogged at close: %s", logs)
	}
	wantCounts(t, map[dropReason]int{dropRefused: 3})
}

func TestANewRefusalCodeInABatchGetsItsOwnWarning(t *testing.T) {
	logs := captureLogs(t)
	url, _ := answering(t, func(_ int, events []map[string]any, w http.ResponseWriter) {
		results := []any{}
		for i, e := range events {
			code := "quota_exhausted"
			if i > 0 {
				code = "payload_malformed"
			}
			results = append(results, map[string]any{"event_id": e["event_id"], "status": "refused", "error": map[string]any{"code": code}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	s := stopsWithTheTest(t, newHTTPSink(url, testKey))
	s.flushEvery = time.Hour
	s.write(testEvent(0))
	s.flush(context.Background())
	for n := 1; n < 3; n++ {
		s.write(testEvent(n))
	}
	s.close(context.Background())
	if !strings.Contains(logs.String(), "events=1 code=payload_malformed") {
		t.Errorf("payload_malformed was not logged: %s", logs)
	}
	wantCounts(t, map[dropReason]int{dropRefused: 3})
}
