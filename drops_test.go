package baton

import (
	"context"
	"maps"
	"net/http"
	"strings"
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
			s.putBack(testEvent(0))
		}},
		"refused": {dropRefused, func(t *testing.T) {
			s := fastSink(newFakeConsole(t, func(int) int { return http.StatusBadRequest }))
			s.write(testEvent(1))
			s.close(context.Background())
		}},
		"encode_failed": {dropEncodeFailed, func(t *testing.T) {
			s := fastSink(newFakeConsole(t, nil))
			s.send(Event{EventType: "tool_call_start", Payload: map[string]any{"bad": func() {}}})
			s.close(context.Background())
		}},
		"panic_while_building": {dropEncodeFailed, func(t *testing.T) {
			guard("tool_call_start", func() { panic("scrubber") })
		}},
		"after_close": {dropAfterClose, func(t *testing.T) {
			s := fastSink(newFakeConsole(t, nil))
			s.close(context.Background())
			s.write(testEvent(1))
		}},
		"unsent_at_close": {dropUnsentAtClose, func(t *testing.T) {
			s := fastSink(newFakeConsole(t, func(int) int { return http.StatusServiceUnavailable }))
			s.budget = 20 * time.Millisecond
			s.write(testEvent(1))
			s.close(context.Background())
			<-s.done // an event still being sent is counted when its send ends
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
	s := fastSink(newFakeConsole(t, func(n int) int { return []int{400, 202, 400, 202}[min(n, 3)] }))
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
	s := fastSink(newFakeConsole(t, func(int) int { <-release; return http.StatusAccepted }))
	s.write(testEvent(0))
	waitFor(t, "the send to start", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.inFlight })
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

func TestAHealthyRunCountsAndLogsNothing(t *testing.T) {
	logs := captureLogs(t)
	s := fastSink(newFakeConsole(t, nil))
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
	return len(s.queue) == 0 && !s.inFlight
}
