package baton

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newTestBaton is New for a DSN naming "acme", with a memory sink.
func newTestBaton(t *testing.T, cfg Config) (*Baton, *memorySink) {
	t.Helper()
	b, mem := configuredAs(t, cfg, "acme"), &memorySink{}
	b.sink = mem
	return b, mem
}

func wrapped(t *testing.T, b *Baton, register func(*mcp.Server)) *mcp.Server {
	t.Helper()
	server := newVectorServer()
	register(server)
	if err := b.Wrap(server); err != nil {
		t.Fatal(err)
	}
	return server
}

func wrappedCount(b *Baton) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.wrapped)
}

// countProbes counts the initialize requests Baton's probe sends to server.
func countProbes(server *mcp.Server) *atomic.Int32 {
	var n atomic.Int32
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if p, ok := req.GetParams().(*mcp.InitializeParams); ok && p.ClientInfo != nil && p.ClientInfo.Name == "baton-probe" {
				n.Add(1)
			}
			return next(ctx, method, req)
		}
	})
	return &n
}

// unchanged reports whether server lists its tools as the vendor added them.
func unchanged(t *testing.T, server *mcp.Server) bool {
	t.Helper()
	cs := connect(t, server)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return len(res.Tools) == 1 && !strings.Contains(string(mustJSON(t, res.Tools[0].InputSchema)), userGoalParam) &&
		cs.InitializeResult().Instructions == ""
}

func TestWrappingAServerTwiceAddsBatonOnce(t *testing.T) {
	b, sink := newTestBaton(t, Config{})
	server := newVectorServer()
	addLookup(server)
	probes := countProbes(server)
	for range 2 {
		if err := b.Wrap(server); err != nil {
			t.Fatal(err)
		}
	}
	if n := probes.Load(); n != 1 {
		t.Errorf("probed %d times, want 1", n)
	}
	call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	if n := len(ofType(sink.all(), "tool_call_start")); n != 1 {
		t.Errorf("%d tool_call_start events, want 1", n)
	}
}

func TestWrapAfterCloseWarnsOnceAndAddsNothing(t *testing.T) {
	logs := captureLogs(t)
	t.Setenv("BATON_DSN", "")
	rec := &recordingSink{}
	b, err := New(Config{VendorID: "acme", Sink: rec})
	if err != nil {
		t.Fatal(err)
	}
	b.Close(context.Background())
	for range 2 {
		server := newVectorServer()
		addLookup(server)
		if err := b.Wrap(server); err != nil {
			t.Fatal(err)
		}
		if !unchanged(t, server) {
			t.Error("a Wrap after Close changed the server")
		}
		call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	}
	if n := strings.Count(logs.String(), "Wrap after Close"); n != 1 {
		t.Errorf("%d warnings, want 1:\n%s", n, logs)
	}
	if got := rec.events(); len(got) != 0 {
		t.Errorf("%d events after Close", len(got))
	}
}

// observedAs gives every tool call on server the Mcp-Session-Id id. Added
// after Wrap, it runs before Baton's middleware.
func observedAs(server *mcp.Server, id string) {
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if r, ok := req.(*mcp.CallToolRequest); ok {
				r.Extra = &mcp.RequestExtra{Header: http.Header{"Mcp-Session-Id": {id}}}
			}
			return next(ctx, method, req)
		}
	})
}

func TestServersShareTheCountOfAnObservedSession(t *testing.T) {
	const stopped = int64(1_800_000_000_000_000)
	b, sink := newTestBaton(t, Config{})
	b.seq.now = func() int64 { return stopped }
	for range 2 {
		server := wrapped(t, b, addLookup)
		observedAs(server, "client-session")
		call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	}
	var got []int64
	for _, e := range sink.all() {
		if e.SessionID == "client-session" {
			got = append(got, e.SequenceNumber)
		}
	}
	if len(got) != 4 {
		t.Fatalf("want 4 events on the observed session, got %v", got)
	}
	for i, n := range got {
		if n != stopped+int64(i) {
			t.Errorf("numbered %v, want %d onward by one", got, stopped)
			break
		}
	}
}

func TestIdenticalServersSendOneSurfaceSnapshot(t *testing.T) {
	b, sink := newTestBaton(t, Config{})
	for range 3 {
		call(t, connect(t, wrapped(t, b, addLookup)), "lookup", map[string]any{"name": "a"})
	}
	if n := len(ofType(sink.all(), "surface_snapshot")); n != 1 {
		t.Errorf("%d surface_snapshot events, want 1", n)
	}
}

func TestANewServerPerHTTPRequest(t *testing.T) {
	b, sink := newTestBaton(t, Config{})
	var built atomic.Int32
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		built.Add(1)
		return wrapped(t, b, addLookup)
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	cs := connectOver(t, &mcp.StreamableClientTransport{Endpoint: ts.URL})
	for range 2 {
		call(t, cs, "lookup", map[string]any{"name": "a"})
	}
	events := sink.all()
	starts, ends := ofType(events, "tool_call_start"), ofType(events, "tool_call_end")
	if len(starts) != 2 || len(ends) != 2 || len(ofType(events, "surface_snapshot")) != 1 || len(events) != 5 {
		t.Errorf("want 2 starts, 2 ends and 1 surface_snapshot, got %d events from %d servers: %v", len(events), built.Load(), eventTypes(events))
	}
}

func TestNewWithNothingConfiguredIsInert(t *testing.T) {
	logs := captureLogs(t)
	t.Setenv("BATON_DSN", "")
	b, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logs.String(), "no DSN"); n != 1 {
		t.Errorf("%d warnings, want 1:\n%s", n, logs)
	}
	server := newVectorServer()
	addLookup(server)
	if err := b.Wrap(server); err != nil {
		t.Fatal(err)
	}
	if !unchanged(t, server) {
		t.Error("an unconfigured Baton changed the server")
	}
	if err := b.Wrap(nil); err == nil {
		t.Error("Wrap(nil) returned no error")
	}
	b.Close(context.Background())
	if n := strings.Count(logs.String(), "no DSN"); n != 1 {
		t.Errorf("Wrap or Close warned again:\n%s", logs)
	}
}

func TestTheOffSwitchMakesAnInertBaton(t *testing.T) {
	t.Setenv("BATON_DISABLED", "1")
	b, err := New(Config{DSN: "not a dsn"})
	if err != nil {
		t.Fatal(err)
	}
	server := newVectorServer()
	addLookup(server)
	if err := b.Wrap(server); err != nil || b.Wrap(nil) != nil {
		t.Errorf("Wrap checked something: %v", err)
	}
	if !unchanged(t, server) {
		t.Error("BATON_DISABLED let Wrap change the server")
	}
	b.Close(context.Background())
}

func TestTheWrappedSetForgetsCollectedServers(t *testing.T) {
	b, _ := newTestBaton(t, Config{})
	const n = 50
	servers := make([]*mcp.Server, n)
	for i := range servers {
		servers[i] = wrapped(t, b, addLookup)
		cs := connect(t, servers[i])
		call(t, cs, "lookup", map[string]any{"name": "a"})
		cs.Close()
	}
	if got := wrappedCount(b); got != n {
		t.Fatalf("%d servers in the wrapped set while all are alive, want %d", got, n)
	}
	runtime.KeepAlive(servers)
	waitFor(t, "collected servers to leave the wrapped set", func() bool {
		runtime.GC()
		return wrappedCount(b) <= n/2
	})
}

func TestANilBatonDoesNotPanic(t *testing.T) {
	var b *Baton
	server := newVectorServer()
	addLookup(server)
	if err := b.Wrap(server); err != nil {
		t.Errorf("Wrap on a nil Baton: %v", err)
	}
	b.Close(context.Background())
	if !unchanged(t, server) {
		t.Error("a nil Baton changed the server")
	}
}

func TestAServersOwnSessionIsNotInTheSharedCounter(t *testing.T) {
	b, sink := newTestBaton(t, Config{})
	cs := connect(t, wrapped(t, b, addLookup))
	for range 2 {
		call(t, cs, "lookup", map[string]any{"name": "a"})
	}
	events := sink.all()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	for i, e := range events {
		if e.SessionID != events[0].SessionID || e.SequenceNumber != int64(i+1) {
			t.Errorf("event %d: session %s numbered %d, want one session numbered from 1", i, e.SessionID, e.SequenceNumber)
		}
	}
	b.seq.counts.mu.Lock()
	defer b.seq.counts.mu.Unlock()
	if _, ok := b.seq.counts.index[events[0].SessionID]; ok {
		t.Error("the server's own session is in the shared counter")
	}
}

func TestWrapGoesOnWithoutTheReportToolWhenItCannotNameIt(t *testing.T) {
	logs := captureLogs(t)
	b, sink := newTestBaton(t, Config{ReportMode: "on"})
	long := strings.Repeat("x", 3000)
	for range 2 {
		server := mcp.NewServer(&mcp.Implementation{Name: long}, nil)
		addLookup(server)
		if err := b.Wrap(server); err != nil {
			t.Fatal(err)
		}
		cs := connect(t, server)
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Tools) != 1 {
			t.Errorf("tools %v, want the vendor's only", res.Tools)
		}
		call(t, cs, "lookup", map[string]any{"name": "a"})
	}
	if n := strings.Count(logs.String(), "could not name the report tool"); n != 1 {
		t.Errorf("%d warnings, want 1:\n%s", n, logs)
	}
	if n := len(ofType(sink.all(), "tool_call_start")); n != 2 {
		t.Errorf("%d tool_call_start events, want 2", n)
	}
}

func TestWrapAddsTheVendorsMiddlewareOnceOnEveryPath(t *testing.T) {
	cases := map[string]struct {
		env     map[string]string
		cfg     Config
		close   bool
		wantErr bool
	}{
		"recording":      {env: map[string]string{"BATON_DSN": ""}, cfg: Config{VendorID: "acme", Sink: &recordingSink{}}},
		"no DSN":         {env: map[string]string{"BATON_DSN": ""}},
		"BATON_DISABLED": {env: map[string]string{"BATON_DISABLED": "1"}},
		"after Close":    {env: map[string]string{"BATON_DSN": ""}, cfg: Config{VendorID: "acme", Sink: &recordingSink{}}, close: true},
		"config error":   {env: map[string]string{"BATON_DISABLED": ""}, cfg: Config{ResultCapture: "none"}, wantErr: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			var runs atomic.Int32
			c.cfg.Middleware = []mcp.Middleware{func(next mcp.MethodHandler) mcp.MethodHandler {
				return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
					if method == "tools/call" {
						runs.Add(1)
					}
					return next(ctx, method, req)
				}
			}}
			b, err := New(c.cfg)
			if (err != nil) != c.wantErr {
				t.Fatalf("New returned error %v; want an error: %v", err, c.wantErr)
			}
			if c.close {
				b.Close(context.Background())
			}
			server := newVectorServer()
			addLookup(server)
			for range 2 {
				if err := b.Wrap(server); err != nil {
					t.Fatal(err)
				}
			}
			call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
			if n := runs.Load(); n != 1 {
				t.Errorf("the vendor's middleware ran %d times for one call, want 1", n)
			}
			b.Close(context.Background())
		})
	}
}

func TestASecondWrapWaitsForTheFirstToAddTheVendorsMiddleware(t *testing.T) {
	var runs atomic.Int32
	b, _ := newTestBaton(t, Config{Middleware: []mcp.Middleware{func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				runs.Add(1)
			}
			return next(ctx, method, req)
		}
	}}})
	server := newVectorServer()
	addLookup(server)
	probing, release := make(chan struct{}), make(chan struct{})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if p, ok := req.GetParams().(*mcp.InitializeParams); ok && p.ClientInfo != nil && p.ClientInfo.Name == "baton-probe" {
				close(probing)
				<-release
			}
			return next(ctx, method, req)
		}
	})
	go b.Wrap(server)
	<-probing
	time.AfterFunc(100*time.Millisecond, func() { close(release) })
	if err := b.Wrap(server); err != nil {
		t.Fatal(err)
	}
	call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	if n := runs.Load(); n != 1 {
		t.Errorf("the vendor's middleware ran %d times for a call after the second Wrap returned, want 1", n)
	}
}

func TestWrapReturnsWhenAMiddlewareHoldsTheProbe(t *testing.T) {
	captureLogs(t)
	b, _ := newTestBaton(t, Config{})
	server := newVectorServer()
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if p, ok := req.GetParams().(*mcp.InitializeParams); ok && p.ClientInfo != nil && p.ClientInfo.Name == "baton-probe" {
				<-hold
			}
			return next(ctx, method, req)
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() { _ = b.Wrap(server) })
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wrap is still waiting on a probe a middleware holds")
	}
}
