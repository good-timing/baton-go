package baton

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// gate stops "locked" with an error and "noCredits" with an error result, as
// a vendor's auth or credits middleware does, and passes everything else.
func gate(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if r, ok := req.(*mcp.CallToolRequest); ok && method == "tools/call" {
			switch r.Params.Name {
			case "locked":
				return nil, errors.New("not signed in as row-42")
			case "noCredits":
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "out of credits for row-42"}}}, nil
			}
		}
		return next(ctx, method, req)
	}
}

func addGatedTools(s *mcp.Server) {
	addLookup(s)
	for _, name := range []string{"locked", "noCredits"} {
		mcp.AddTool(s, &mcp.Tool{Name: name}, lookup)
	}
	s.AddTool(&mcp.Tool{Name: "fails", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("the tool's own error")
	})
}

func TestACallTheVendorsMiddlewareStopsIsRefused(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		kind string
	}{
		{"locked", nil, "refused"},
		{"noCredits", nil, "refused"},
		{"fails", nil, "<absent>"},
		{"nope", nil, "unknown_tool"},
		{"lookup", map[string]any{"name": 5}, "invalid_argument"},
	}
	for _, capture := range []string{"full", "off"} {
		server, sink := installWithSink(t, Config{ResultCaptureMode: capture, Middleware: []mcp.Middleware{gate}}, addGatedTools)
		cs := connect(t, server)
		for _, c := range cases {
			_, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
		}
		events := sink.all()
		if starts := ofType(events, "tool_call_start"); len(starts) != len(cases) {
			t.Fatalf("%s: %d starts, want %d", capture, len(starts), len(cases))
		}
		failures := ofType(events, "tool_call_error")
		if len(failures) != len(cases) {
			t.Fatalf("%s: %d errors, want %d", capture, len(failures), len(cases))
		}
		for i, c := range cases {
			p := failures[i].Payload.(toolCallError)
			if deref(p.FailureKind) != c.kind {
				t.Errorf("%s %s: failure_kind %q, want %q", capture, c.tool, deref(p.FailureKind), c.kind)
			}
			if c.kind != "refused" {
				continue
			}
			if leaked := strings.Contains(deref(p.ErrorBody), "row-42"); leaked != (capture == "full") {
				t.Errorf("%s %s: error_body %q", capture, c.tool, deref(p.ErrorBody))
			}
			if capture == "off" && (p.Result != nil || deref(p.ResultCapture) != "off") {
				t.Errorf("%s %s: result not withheld: %+v", capture, c.tool, p)
			}
		}
	}
}

func TestMiddlewareTheVendorAddsItselfIsNotNamed(t *testing.T) {
	server, sink := installWithSink(t, Config{}, func(s *mcp.Server) {
		addGatedTools(s)
		s.AddReceivingMiddleware(gate)
	})
	_, _ = connect(t, server).CallTool(context.Background(), &mcp.CallToolParams{Name: "locked"})
	failures := ofType(sink.all(), "tool_call_error")
	if len(failures) != 1 {
		t.Fatalf("%d errors, want 1", len(failures))
	}
	if p := failures[0].Payload.(toolCallError); p.FailureKind != nil {
		t.Errorf("failure_kind = %q, want none", *p.FailureKind)
	}
}

func TestTheVendorsMiddlewareRunsInsideBaton(t *testing.T) {
	var seen []string
	record := func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if r, ok := req.(*mcp.CallToolRequest); ok && method == "tools/call" {
				seen = append(seen, string(r.Params.Arguments))
			}
			return next(ctx, method, req)
		}
	}
	server, sink := installWithSink(t, Config{Middleware: []mcp.Middleware{record}}, addLookup)
	call(t, connect(t, server), "lookup", map[string]any{"name": "a", "user_goal": "4: find a"})
	if len(seen) != 1 || strings.Contains(seen[0], "user_goal") {
		t.Errorf("the vendor's middleware saw %q; want one call, with user_goal already removed", seen)
	}
	if p := ofType(sink.all(), "tool_call_end"); len(p) != 1 {
		t.Errorf("%d ends, want 1", len(p))
	}
}

func TestANilMiddlewareIsAConfigError(t *testing.T) {
	if _, err := configure(testImpl, nil, Config{Middleware: []mcp.Middleware{gate, nil}}); err == nil {
		t.Error("want an error")
	}
}

// stopsAll refuses every tools/call, as a closed auth check does.
func stopsAll(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method == "tools/call" {
			return nil, errors.New("signed out")
		}
		return next(ctx, method, req)
	}
}

func TestTheVendorsMiddlewareRunsWhenBatonIsNotInstalled(t *testing.T) {
	cases := map[string]struct {
		env map[string]string
		cfg Config
	}{
		"BATON_DISABLED": {env: map[string]string{"BATON_DISABLED": "1"}},
		"no DSN":         {env: map[string]string{"BATON_DSN": ""}},
		"a bad config":   {env: map[string]string{"BATON_DSN": "https://baton_pk_x@baton.example/ten_x/s"}, cfg: Config{ResultCaptureMode: "none"}},
		"a bad VendorID": {cfg: Config{VendorID: "not a vendor id", Sink: &recordingSink{}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			server := newVectorServer()
			addLookup(server)
			c.cfg.Middleware = []mcp.Middleware{stopsAll}
			_, _ = Install(server, testImpl, nil, c.cfg)
			if _, err := connect(t, server).CallTool(context.Background(), &mcp.CallToolParams{Name: "lookup", Arguments: map[string]any{"name": "a"}}); err == nil {
				t.Error("the call got through: the vendor's middleware was not added")
			}
		})
	}
}

func TestTheAnnotationToolGoesThroughTheVendorsMiddleware(t *testing.T) {
	server, _ := installWithSink(t, Config{ReportMode: "on", AnnotationToolName: "report_it", Middleware: []mcp.Middleware{stopsAll}}, addLookup)
	if _, err := connect(t, server).CallTool(context.Background(), &mcp.CallToolParams{Name: "report_it", Arguments: map[string]any{"user_goal": "1: x"}}); err == nil {
		t.Error("the annotation tool answered past the vendor's middleware")
	}
	open, _ := installWithSink(t, Config{ReportMode: "on", AnnotationToolName: "report_it", Middleware: []mcp.Middleware{gate}}, addLookup)
	if res := call(t, connect(t, open), "report_it", map[string]any{"user_goal": "1: x"}); res.IsError {
		t.Errorf("annotation tool failed: %+v", res)
	}
}

func TestBatonsOwnListingSkipsTheVendorsMiddleware(t *testing.T) {
	var lists atomic.Int32
	count := func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				lists.Add(1)
			}
			return next(ctx, method, req)
		}
	}
	server, _ := installWithSink(t, Config{Middleware: []mcp.Middleware{count}}, addLookup)
	call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	if n := lists.Load(); n != 0 {
		t.Errorf("the vendor's middleware saw %d listings the caller never sent", n)
	}
}

func TestAMiddlewareThatDropsTheCtxDoesNotMakeCallsRefused(t *testing.T) {
	dropsCtx := func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(_ context.Context, method string, req mcp.Request) (mcp.Result, error) {
			return next(context.Background(), method, req)
		}
	}
	server, sink := installWithSink(t, Config{Middleware: []mcp.Middleware{dropsCtx}}, addGatedTools)
	_, _ = connect(t, server).CallTool(context.Background(), &mcp.CallToolParams{Name: "fails"})
	failures := ofType(sink.all(), "tool_call_error")
	if len(failures) != 1 || failures[0].Payload.(toolCallError).FailureKind != nil {
		t.Errorf("failures = %+v, want one with no failure_kind", failures)
	}
}

func TestAMiddlewareThatListsBeforeRefusingIsStillRefused(t *testing.T) {
	listsThenStops := func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				_, _ = next(ctx, "tools/list", &mcp.ListToolsRequest{Session: req.GetSession().(*mcp.ServerSession), Params: &mcp.ListToolsParams{}})
				return nil, errors.New("not allowed")
			}
			return next(ctx, method, req)
		}
	}
	server, sink := installWithSink(t, Config{Middleware: []mcp.Middleware{listsThenStops}}, addLookup)
	_, _ = connect(t, server).CallTool(context.Background(), &mcp.CallToolParams{Name: "lookup", Arguments: map[string]any{"name": "a"}})
	failures := ofType(sink.all(), "tool_call_error")
	if len(failures) != 1 || deref(failures[0].Payload.(toolCallError).FailureKind) != "refused" {
		t.Errorf("failures = %+v, want one refused", failures)
	}
}

func TestTheVendorsMiddlewareRunsInTheOrderGiven(t *testing.T) {
	var order []string
	named := func(name string) mcp.Middleware {
		return func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method == "tools/call" {
					order = append(order, name)
				}
				return next(ctx, method, req)
			}
		}
	}
	server, _ := installWithSink(t, Config{Middleware: []mcp.Middleware{named("first"), named("second")}}, addLookup)
	call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	if strings.Join(order, ",") != "first,second" {
		t.Errorf("order = %q", order)
	}
}

func TestARefusalMarkedPublicIsKeptUnderOff(t *testing.T) {
	public := func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				return nil, &PublicError{Message: "sign in first"}
			}
			return next(ctx, method, req)
		}
	}
	server, sink := installWithSink(t, Config{ResultCaptureMode: "off", Middleware: []mcp.Middleware{public}}, addLookup)
	_, _ = connect(t, server).CallTool(context.Background(), &mcp.CallToolParams{Name: "lookup", Arguments: map[string]any{"name": "a"}})
	failures := ofType(sink.all(), "tool_call_error")
	if len(failures) != 1 {
		t.Fatalf("%d errors, want 1", len(failures))
	}
	if p := failures[0].Payload.(toolCallError); deref(p.FailureKind) != "refused" || deref(p.ErrorBody) != "sign in first" {
		t.Errorf("failure = %+v", p)
	}
}

type userKey struct{}

func TestResolvePrincipalSeesWhatTheVendorsMiddlewareSet(t *testing.T) {
	signIn := func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			return next(context.WithValue(ctx, userKey{}, "u1"), method, req)
		}
	}
	resolve := func(ctx context.Context, _ Caller) (*Principal, error) {
		if id, ok := ctx.Value(userKey{}).(string); ok {
			return &Principal{ID: id}, nil
		}
		return nil, nil
	}
	server, sink := installWithSink(t, Config{ResolvePrincipal: resolve, Middleware: []mcp.Middleware{signIn}}, addLookup)
	call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	events := asJSON(t, append(ofType(sink.all(), "tool_call_start"), ofType(sink.all(), "tool_call_end")...))
	if len(events) != 2 {
		t.Fatalf("%d events, want a start and an end", len(events))
	}
	for _, e := range events {
		if p, _ := e["principal"].(map[string]any); p["id"] != "u1" {
			t.Errorf("%s: principal %v, want u1", e["event_type"], e["principal"])
		}
	}
}

func TestARefusedCallSendsItsStartFirst(t *testing.T) {
	server, sink := installWithSink(t, Config{Middleware: []mcp.Middleware{stopsAll}}, addLookup)
	_, _ = connect(t, server).CallTool(context.Background(), &mcp.CallToolParams{Name: "lookup", Arguments: map[string]any{"name": "a"}})
	var events []Event
	for _, e := range sink.all() {
		if strings.HasPrefix(e.EventType, "tool_call_") {
			events = append(events, e)
		}
	}
	if len(events) != 2 || events[0].EventType != "tool_call_start" || events[1].EventType != "tool_call_error" ||
		deref(events[0].CallID) == "<absent>" || deref(events[0].CallID) != deref(events[1].CallID) {
		t.Errorf("events = %+v, want a start then an error of one call", events)
	}
}

func TestAMiddlewareThatRetriesRecordsTheCallOnce(t *testing.T) {
	retries := func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				_, _ = next(ctx, method, req)
			}
			return next(ctx, method, req)
		}
	}
	server, sink := installWithSink(t, Config{Middleware: []mcp.Middleware{retries}}, addLookup)
	call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	if starts, ends := ofType(sink.all(), "tool_call_start"), ofType(sink.all(), "tool_call_end"); len(starts) != 1 || len(ends) != 1 {
		t.Errorf("%d starts and %d ends, want one of each", len(starts), len(ends))
	}
}

func TestResolvePrincipalRunsOnceForACallThatGetsThrough(t *testing.T) {
	var calls atomic.Int32
	resolve := func(context.Context, Caller) (*Principal, error) {
		calls.Add(1)
		return nil, nil
	}
	server, _ := installWithSink(t, Config{ResolvePrincipal: resolve, Middleware: []mcp.Middleware{gate}}, addLookup)
	cs := connect(t, server)
	before := calls.Load()
	call(t, cs, "lookup", map[string]any{"name": "a"})
	if n := calls.Load() - before; n != 1 {
		t.Errorf("ResolvePrincipal ran %d times for one call, want 1", n)
	}
}

func TestARefusedCallReachesTheVendorsSink(t *testing.T) {
	out := &recordingSink{}
	server := newVectorServer()
	addLookup(server)
	handle, err := Install(server, testImpl, nil, Config{VendorID: "acme", Sink: out, Middleware: []mcp.Middleware{stopsAll}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = connect(t, server).CallTool(context.Background(), &mcp.CallToolParams{Name: "lookup", Arguments: map[string]any{"name": "a"}})
	handle.Close(context.Background())
	out.mu.Lock()
	defer out.mu.Unlock()
	var kinds []string
	for _, batch := range out.batches {
		for _, e := range ofType(batch, "tool_call_error") {
			kinds = append(kinds, deref(e.Payload.(toolCallError).FailureKind))
		}
	}
	if strings.Join(kinds, ",") != "refused" {
		t.Errorf("failure kinds = %q, want one refused", kinds)
	}
}

func TestAMiddlewareThatAnswersTheAnnotationToolWithNothingGetsAnEmptyResult(t *testing.T) {
	nothing := func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				return nil, nil
			}
			return next(ctx, method, req)
		}
	}
	server, _ := installWithSink(t, Config{ReportMode: "on", AnnotationToolName: "report_it", Middleware: []mcp.Middleware{nothing}}, addLookup)
	res := call(t, connect(t, server), "report_it", map[string]any{"user_goal": "1: x"})
	if res == nil || res.IsError {
		t.Errorf("result = %+v, want an empty one", res)
	}
}
