package baton

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingSink is a vendor's Sink that keeps what it is given. fail and
// block, when set, decide each Write.
type recordingSink struct {
	mu              sync.Mutex
	batches         [][]Event
	closes          int
	wroteAfterClose bool
	fail            func(batch int) error
	block           chan struct{}
	entered         chan struct{}
	panicOnClose    bool
}

func (r *recordingSink) Write(ctx context.Context, events []Event) error {
	r.mu.Lock()
	n := len(r.batches)
	r.batches = append(r.batches, events)
	r.wroteAfterClose = r.wroteAfterClose || r.closes > 0
	fail, block, entered := r.fail, r.block, r.entered
	r.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		<-block
	}
	if fail != nil {
		return fail(n)
	}
	return nil
}

func (r *recordingSink) Close(context.Context) error {
	r.mu.Lock()
	r.closes++
	r.mu.Unlock()
	if r.panicOnClose {
		panic("close")
	}
	return nil
}

func (r *recordingSink) events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []Event
	for _, b := range r.batches {
		all = append(all, b...)
	}
	return all
}

func (r *recordingSink) closeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closes
}

func closeQueue(t *testing.T, s *queueSink) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.close(ctx)
}

func TestAQueuedSinkGetsEveryEventInOrderAndIsClosedOnce(t *testing.T) {
	logs := captureLogs(t)
	out := &recordingSink{}
	s := newQueueSink(out)
	const n = 3*batchCap + 7
	for i := range n {
		s.write(testEvent(i))
	}
	closeQueue(t, s)
	s.close(context.Background())
	got := out.events()
	if len(got) != n {
		t.Fatalf("%d events, want %d", len(got), n)
	}
	for i, e := range got {
		if e.SequenceNumber != int64(i) {
			t.Fatalf("event %d is %d", i, e.SequenceNumber)
		}
	}
	for _, b := range out.batches {
		if len(b) > batchCap {
			t.Errorf("a batch of %d", len(b))
		}
	}
	if out.closeCount() != 1 {
		t.Errorf("Close called %d times", out.closeCount())
	}
	wantCounts(t, map[dropReason]int{})
	if warnings(logs) != 0 {
		t.Errorf("logs: %s", logs)
	}
}

func TestAFailedWriteIsCountedAndTheNextBatchIsStillWritten(t *testing.T) {
	for name, fail := range map[string]func(int) error{
		"error": func(n int) error {
			if n == 0 {
				return errors.New("disk full")
			}
			return nil
		},
		"panic": func(n int) error {
			if n == 0 {
				panic("secret event content")
			}
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			out := &recordingSink{fail: fail, block: make(chan struct{}), entered: make(chan struct{}, 1)}
			s := newQueueSink(out)
			s.write(testEvent(0))
			<-out.entered
			s.write(testEvent(1))
			s.write(testEvent(2))
			close(out.block)
			closeQueue(t, s)
			wantCounts(t, map[dropReason]int{dropWriteFailed: 1})
			if len(out.batches) != 2 || len(out.batches[1]) != 2 {
				t.Errorf("batches: %v", out.batches)
			}
			if warnings(logs) != 2 || !strings.Contains(logs.String(), "write_failed 1") || strings.Contains(logs.String(), "secret") {
				t.Errorf("logs: %s", logs)
			}
		})
	}
}

func TestCloseReturnsByItsDeadlineWhenWriteHangsAndClosesTheSinkAfterIt(t *testing.T) {
	logs := captureLogs(t)
	out := &recordingSink{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	s := newQueueSink(out)
	s.write(testEvent(0))
	<-out.entered
	s.write(testEvent(1))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	s.close(ctx)
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Errorf("close took %v", took)
	}
	wantCounts(t, map[dropReason]int{dropUnsentAtClose: 2})
	if out.closeCount() != 0 {
		t.Error("Close was called while Write was running")
	}
	close(out.block)
	waitDone(t, s.done)
	if out.closeCount() != 1 || out.wroteAfterClose {
		t.Errorf("closes %d, wrote after close %v", out.closeCount(), out.wroteAfterClose)
	}
	wantCounts(t, map[dropReason]int{dropUnsentAtClose: 2})
	if !strings.Contains(logs.String(), "unsent=2") || strings.Contains(logs.String(), "delivering again") {
		t.Errorf("logs: %s", logs)
	}
}

func TestAWriteStoppedByCloseIsCountedOnceAsUnsent(t *testing.T) {
	captureLogs(t)
	out := &recordingSink{entered: make(chan struct{}, 1)}
	s := newQueueSink(out)
	out.fail = func(int) error { <-s.ctx.Done(); return context.Canceled }
	s.budget = 200 * time.Millisecond
	s.write(testEvent(0))
	<-out.entered
	s.close(context.Background())
	waitDone(t, s.done)
	wantCounts(t, map[dropReason]int{dropUnsentAtClose: 1})
}

func TestASinkThatPanicsOnCloseDoesNotCrash(t *testing.T) {
	logs := captureLogs(t)
	out := &recordingSink{panicOnClose: true}
	s := newQueueSink(out)
	closeQueue(t, s)
	waitDone(t, s.done)
	if !strings.Contains(logs.String(), "failed to close") {
		t.Errorf("logs: %s", logs)
	}
}

func TestAnEventAfterCloseOrPastTheBufferIsCounted(t *testing.T) {
	logs := captureLogs(t)
	out := &recordingSink{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	s := newQueueSink(out)
	s.write(testEvent(0))
	<-out.entered
	for i := range bufferCap + 2 {
		s.write(testEvent(i + 1))
	}
	close(out.block)
	closeQueue(t, s)
	s.write(testEvent(0))
	s.write(testEvent(0))
	wantCounts(t, map[dropReason]int{dropBufferFull: 2, dropAfterClose: 2})
	if got := out.events(); len(got) != bufferCap+1 || got[1].SequenceNumber != 3 {
		t.Errorf("%d events", len(got))
	}
	if warnings(logs) != 3 || !strings.Contains(logs.String(), "buffer_full 2") {
		t.Errorf("logs: %s", logs)
	}
}

func TestMultiSinkCountsALossOnceForEachSinkThatFailedIt(t *testing.T) {
	broken := func(int) error { return errors.New("broken") }
	for name, c := range map[string]struct {
		sinks     []*recordingSink
		want      int
		delivered bool
	}{
		"one of two fails": {[]*recordingSink{{fail: broken}, {}}, 5, true},
		"both fail":        {[]*recordingSink{{fail: broken}, {fail: broken}}, 10, false},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			sinks := make([]Sink, len(c.sinks))
			for i, r := range c.sinks {
				sinks[i] = r
			}
			s := newQueueSink(MultiSink(sinks...))
			for i := range 5 {
				s.write(testEvent(i))
			}
			closeQueue(t, s)
			wantCounts(t, map[dropReason]int{dropWriteFailed: c.want})
			for i, r := range c.sinks {
				if r.closeCount() != 1 {
					t.Errorf("sink %d closed %d times", i, r.closeCount())
				}
			}
			if strings.Contains(logs.String(), "delivering again") != c.delivered ||
				strings.Contains(logs.String(), "a sink in MultiSink failed") != c.delivered {
				t.Errorf("logs: %s", logs)
			}
		})
	}
}

func TestAMultiSinkLossThatCloseStoppedIsCountedUnsent(t *testing.T) {
	captureLogs(t)
	stopped := &recordingSink{entered: make(chan struct{}, 1)}
	s := newQueueSink(MultiSink(&recordingSink{}, stopped))
	stopped.fail = func(int) error { <-s.ctx.Done(); return context.Canceled }
	s.budget = 200 * time.Millisecond
	s.write(testEvent(0))
	<-stopped.entered
	s.close(context.Background())
	waitDone(t, s.done)
	wantCounts(t, map[dropReason]int{dropUnsentAtClose: 1})
}

func TestAnEmptyMultiSinkLosesEveryEvent(t *testing.T) {
	captureLogs(t)
	s := newQueueSink(MultiSink())
	s.write(testEvent(0))
	closeQueue(t, s)
	wantCounts(t, map[dropReason]int{dropWriteFailed: 1})
}

func TestAShortWriteEndsItsLineAndCountsOnlyTheLinesItLost(t *testing.T) {
	captureLogs(t)
	var got []byte
	first := true
	write := func(b []byte) (int, error) {
		if !first {
			got = append(got, b...)
			return len(b), nil
		}
		first = false
		line := bytes.IndexByte(b, '\n') + 1
		got = append(got, b[:line+5]...)
		return line + 5, errors.New("disk full")
	}
	err := writeLines(write, &bytes.Buffer{}, []Event{testEvent(0), testEvent(1), testEvent(2)}, &sync.Once{})
	if n := lost(err, 3); n != 2 {
		t.Errorf("lost %d, want 2", n)
	}
	if err := writeLines(write, &bytes.Buffer{}, []Event{testEvent(3)}, &sync.Once{}); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(got, []byte("\n")), []byte("\n"))
	if len(lines) != 3 || !json.Valid(lines[0]) || !json.Valid(lines[2]) {
		t.Errorf("wrote %q", got)
	}
}

func TestFileSinkAppendsJSONLinesAndCreatesTheFileOnlyWhenWritten(t *testing.T) {
	captureLogs(t)
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte("{\"kept\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := FileSink(path)
	bad := testEvent(1)
	bad.Payload = panicsWhenEncoded{}
	if err := out.Write(context.Background(), []Event{testEvent(0), bad, testEvent(2)}); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, path)
	if len(lines) != 3 || lines[0]["kept"] != true || lines[1]["sequence_number"] != 0.0 || lines[2]["sequence_number"] != 2.0 {
		t.Errorf("lines: %v", lines)
	}
	wantCounts(t, map[dropReason]int{dropEncodeFailed: 1})

	unused := filepath.Join(t.TempDir(), "unused.jsonl")
	if err := FileSink(unused).Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unused); !os.IsNotExist(err) {
		t.Errorf("an unused FileSink made a file: %v", err)
	}
}

func TestAFileSinkThatCannotOpenIsTriedAgainOnTheNextWrite(t *testing.T) {
	captureLogs(t)
	dir := filepath.Join(t.TempDir(), "later")
	path := filepath.Join(dir, "events.jsonl")
	out := FileSink(path)
	s := newQueueSink(out)
	s.write(testEvent(0))
	waitFor(t, "the failed write", func() bool { return processDrops.totals()[dropWriteFailed] == 1 })
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s.write(testEvent(1))
	closeQueue(t, s)
	if lines := readLines(t, path); len(lines) != 1 || lines[0]["sequence_number"] != 1.0 {
		t.Errorf("lines: %v", lines)
	}
	wantCounts(t, map[dropReason]int{dropWriteFailed: 1})
}

func TestAWriteThatFailsCountsAnUnencodableEventOnce(t *testing.T) {
	captureLogs(t)
	bad := testEvent(1)
	bad.Payload = panicsWhenEncoded{}
	err := writeLines(func([]byte) (int, error) { return 0, errors.New("full") }, &bytes.Buffer{},
		[]Event{testEvent(0), bad, testEvent(2)}, &sync.Once{})
	processDrops.add(dropWriteFailed, lost(err, 3))
	wantCounts(t, map[dropReason]int{dropEncodeFailed: 1, dropWriteFailed: 2})
}

func TestStdoutSinkWritesToStderr(t *testing.T) {
	captureLogs(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = old })
	if err := StdoutSink().Write(context.Background(), []Event{testEvent(7)}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var e map[string]any
	if err := json.Unmarshal([]byte(line), &e); err != nil || e["sequence_number"] != 7.0 {
		t.Errorf("stderr: %q", line)
	}
}

func TestInstallWithASinkUsesTheIdsInConfig(t *testing.T) {
	for name, c := range map[string]struct {
		tenant, want string
	}{
		"tenant set":   {"ten_0123abcd", "ten_0123abcd"},
		"tenant unset": {"", "acme"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BATON_DSN", "")
			captureLogs(t)
			path := filepath.Join(t.TempDir(), "events.jsonl")
			server := newVectorServer()
			addLookup(server)
			handle, err := Install(server, Config{Sink: FileSink(path), VendorID: "acme", TenantID: c.tenant})
			if err != nil {
				t.Fatal(err)
			}
			call(t, connect(t, server), "lookup", map[string]any{"name": "a", "user_goal": "1: x; success is y"})
			handle.Close(context.Background())
			lines := readLines(t, path)
			if len(lines) == 0 {
				t.Fatal("no events")
			}
			for _, e := range lines {
				if e["vendor_id"] != "acme" || e["tenant_id"] != c.want {
					t.Errorf("ids %v / %v", e["vendor_id"], e["tenant_id"])
				}
			}
		})
	}
}

func TestInstallPicksTheSinkFromConfig(t *testing.T) {
	out := &recordingSink{}
	for name, c := range map[string]struct {
		env     string
		cfg     Config
		wantErr string
		sink    Sink // nil: no sink is installed
		log     string
	}{
		"DSN and Sink":       {cfg: Config{DSN: testDSN, Sink: out, VendorID: "acme"}, wantErr: "DSN and also"},
		"DSN and VendorID":   {cfg: Config{DSN: testDSN, VendorID: "acme"}, wantErr: "DSN and also"},
		"DSN and TenantID":   {cfg: Config{DSN: testDSN, TenantID: "t"}, wantErr: "DSN and also"},
		"Sink, no VendorID":  {cfg: Config{Sink: out}, wantErr: "VendorID"},
		"TenantID alone":     {cfg: Config{TenantID: "t"}, wantErr: "VendorID"},
		"bad VendorID":       {cfg: Config{Sink: out, VendorID: "a b"}, wantErr: "VendorID"},
		"VendorID alone":     {cfg: Config{VendorID: "acme"}, sink: &stderrSink{}},
		"env DSN loses":      {env: testDSN, cfg: Config{Sink: out, VendorID: "acme"}, sink: out, log: "BATON_DSN is set but ignored"},
		"nothing configured": {log: "no DSN"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BATON_DSN", c.env)
			logs := captureLogs(t)
			handle, err := Install(newVectorServer(), c.cfg)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("error %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer handle.Close(context.Background())
			var got Sink
			if handle.b != nil {
				got = handle.b.sink.(*queueSink).out
			}
			switch want := c.sink.(type) {
			case nil:
				if got != nil {
					t.Errorf("installed %T", got)
				}
			case *stderrSink:
				if _, ok := got.(*stderrSink); !ok {
					t.Errorf("installed %T", got)
				}
			default:
				if got != want {
					t.Errorf("installed %T", got)
				}
			}
			if !strings.Contains(logs.String(), c.log) {
				t.Errorf("logs: %s", logs)
			}
		})
	}
}

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		lines = append(lines, m)
	}
	return lines
}
