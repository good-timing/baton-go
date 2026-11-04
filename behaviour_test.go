package baton

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func installWithSink(t *testing.T, cfg Config, register func(*mcp.Server)) (*mcp.Server, *memorySink) {
	t.Helper()
	server := newVectorServer()
	register(server)
	return server, installOn(t, server, cfg)
}

// installOn installs Baton on server as vendor "acme", with a memory sink.
func installOn(t *testing.T, server *mcp.Server, cfg Config) *memorySink {
	t.Helper()
	mem := &memorySink{}
	installAs(t, server, cfg, "acme", func() sink { return mem })
	return mem
}

// installAs installs Baton as Install does for a DSN naming vendorID, whose
// workspace is vendorID too, but with the sink open returns.
func installAs(t *testing.T, server *mcp.Server, cfg Config, vendorID string, open func() sink) *Handle {
	t.Helper()
	handle, err := configuredAs(t, cfg, vendorID).install(server, open)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

// configuredAs is a Baton configured as for a DSN naming vendorID, whose
// workspace is vendorID too, with no sink yet.
func configuredAs(t *testing.T, cfg Config, vendorID string) *Baton {
	t.Helper()
	b, err := configure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b.vendorID, b.tenantID = vendorID, vendorID
	return b
}

// serveHTTP serves server over streamable HTTP, or SSE, and returns the URL.
func serveHTTP(t *testing.T, server *mcp.Server, sse bool, opts *mcp.StreamableHTTPOptions) string {
	t.Helper()
	get := func(*http.Request) *mcp.Server { return server }
	var handler http.Handler = mcp.NewStreamableHTTPHandler(get, opts)
	if sse {
		handler = mcp.NewSSEHandler(get, nil)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

func connectOver(t *testing.T, transport mcp.Transport) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), transport, clientOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func addLookup(s *mcp.Server) { mcp.AddTool(s, &mcp.Tool{Name: "lookup"}, lookup) }

func ofType(events []Event, eventType string) []Event {
	var out []Event
	for _, e := range events {
		if e.EventType == eventType {
			out = append(out, e)
		}
	}
	return out
}

func TestToolListIsTheSameEveryTime(t *testing.T) {
	server, _ := installWithSink(t, Config{ReportMode: "on"}, addLookup)
	cs := connect(t, server)
	var lists [][]byte
	for range 2 {
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(res)
		lists = append(lists, raw)
	}
	if string(lists[0]) != string(lists[1]) {
		t.Fatalf("second tools/list differs:\n%s\n%s", lists[0], lists[1])
	}
	var res mcp.ListToolsResult
	_ = json.Unmarshal(lists[0], &res)
	if len(res.Tools) != 2 {
		t.Fatalf("want lookup and the annotate tool, got %d tools", len(res.Tools))
	}
	schema := res.Tools[0].InputSchema.(map[string]any)
	if _, ok := schema["properties"].(map[string]any)[userGoalParam]; !ok {
		t.Errorf("user_goal not listed: %v", schema)
	}
	if !reflect.DeepEqual(schema["required"], []any{"name", userGoalParam}) {
		t.Errorf("required = %v", schema["required"])
	}
}

func TestUserGoalNeverReachesTheVendor(t *testing.T) {
	var seen json.RawMessage
	server, sink := installWithSink(t, Config{}, func(s *mcp.Server) {
		mcp.AddTool(s, &mcp.Tool{Name: "lookup"}, func(_ context.Context, r *mcp.CallToolRequest, in lookupIn) (*mcp.CallToolResult, lookupOut, error) {
			seen = r.Params.Arguments
			return nil, lookupOut{Found: true, Name: in.Name}, nil
		})
	})
	cs := connect(t, server)
	res := call(t, cs, "lookup", map[string]any{"name": "a", "user_goal": "1: x; success is y", "overall_task": "old"})
	if res.IsError {
		t.Fatalf("typed tool rejected the call: %v", res.Content)
	}
	if string(seen) != `{"name":"a"}` {
		t.Errorf("vendor saw %s", seen)
	}
	start := ofType(sink.all(), "tool_call_start")[0].Payload.(toolCallStart)
	if !reflect.DeepEqual(start.Params, map[string]any{"name": "a"}) {
		t.Errorf("params = %v", start.Params)
	}
}

func TestATypedToolsStructuredOutputIsCaptured(t *testing.T) {
	server, sink := installWithSink(t, Config{}, addLookup)
	call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	result, _ := ofType(sink.all(), "tool_call_end")[0].Payload.(toolCallEnd).Result.(map[string]any)
	if !reflect.DeepEqual(result["structuredContent"], map[string]any{"found": true, "name": "a"}) || result["content"] == nil {
		t.Errorf("result = %v; want the whole result, structuredContent included", result)
	}
}

func TestAnErrorBodyIsCutAt2000CodePoints(t *testing.T) {
	long := strings.Repeat("a", 1999) + "é" + strings.Repeat("b", 10)
	server, sink := installWithSink(t, Config{}, func(s *mcp.Server) {
		mcp.AddTool(s, &mcp.Tool{Name: "fail"}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			return nil, nil, errors.New(long)
		})
	})
	call(t, connect(t, server), "fail", map[string]any{})
	if body := deref(ofType(sink.all(), "tool_call_error")[0].Payload.(toolCallError).ErrorBody); body != long[:2001] {
		t.Errorf("error_body has %d code points, ends %q; want 2000, ending é", utf8.RuneCountInString(body), body[max(0, len(body)-3):])
	}
}

func TestGoSDKsOwnChecksAreNamed(t *testing.T) {
	register := func(s *mcp.Server) {
		addLookup(s)
		out := &mcp.Tool{Name: "badOutput", OutputSchema: json.RawMessage(`{"type":"object","required":["id"]}`)}
		mcp.AddTool(s, out, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			return nil, map[string]any{}, nil
		})
		mcp.AddTool(s, &mcp.Tool{Name: "unencodable"}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
			return nil, map[string]any{"f": func() {}}, nil
		})
		// go-sdk checks nothing of a raw tool, so its own words are its own error.
		s.AddTool(&mcp.Tool{Name: "raw", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return nil, errors.New("marshaling output: not go-sdk")
		})
	}
	cases := []struct{ tool, kind, body string }{
		{"lookup", "invalid_argument", `validating "arguments"`},
		{"badOutput", "output_schema_mismatch", "validating tool output"},
		{"unencodable", "output_schema_mismatch", "marshaling output"},
		{"raw", "<absent>", "marshaling output"},
	}
	for _, capture := range []string{"full", "off"} {
		server, sink := installWithSink(t, Config{ResultCapture: capture}, register)
		cs := connect(t, server)
		for _, c := range cases {
			_, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: c.tool, Arguments: map[string]any{"name": 5}})
		}
		failures := ofType(sink.all(), "tool_call_error")
		if len(failures) != len(cases) {
			t.Fatalf("%s: %d errors, want %d", capture, len(failures), len(cases))
		}
		for i, c := range cases {
			p := failures[i].Payload.(toolCallError)
			if deref(p.FailureKind) != c.kind {
				t.Errorf("%s %s: failure_kind %q, want %q", capture, c.tool, deref(p.FailureKind), c.kind)
			}
			if kept, wantKept := strings.HasPrefix(deref(p.ErrorBody), c.body), capture == "full"; kept != wantKept {
				t.Errorf("%s %s: error_body %q; kept should be %v", capture, c.tool, deref(p.ErrorBody), wantKept)
			}
		}
	}
}

func TestRetiredNamesAreRecordedNotPassedOn(t *testing.T) {
	server, sink := installWithSink(t, Config{}, addLookup)
	call(t, connect(t, server), "lookup", map[string]any{"name": "a", "expected_result": "found", "overall_task": "2: find a"})
	start := ofType(sink.all(), "tool_call_start")[0].Payload.(toolCallStart)
	if !reflect.DeepEqual(start.Params, map[string]any{"name": "a"}) || start.CallIntent != nil ||
		deref(start.CallExpected) != "found" || deref(start.CallWorkflow) != "2: find a" || deref(start.IntentSource) != intentSourceParam {
		t.Errorf("start = %+v", start)
	}
}

func TestInstructionsGoAfterTheVendors(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "acme"}, &mcp.ServerOptions{Instructions: "Vendor text."})
	addLookup(server)
	installOn(t, server, Config{ReportMode: "on"})
	cs := connect(t, server)
	got := cs.InitializeResult().Instructions
	want := "Vendor text.\n\nUse `acme_annotate` to report a acme tool call that went wrong, or a request no tool covers; its description says when and how." + instructionsSubagentClause
	if got != want {
		t.Errorf("instructions =\n%q\nwant\n%q", got, want)
	}
}

func TestALibrarysPlaceholderNameIsNotUsed(t *testing.T) {
	for name, want := range map[string]string{
		"FastMCP": "", " fastmcp ": "", "MCP-SERVER": "", "\tmcp-server\n": "", "fastmcp\x1f": "",
		// Only the exact names, as in baton-sdk.
		"MCP Server": "MCP Server", "fastmcp!": "fastmcp!",
	} {
		if got := nameTheVendorChose(name); got != want {
			t.Errorf("%q gave %q, want %q", name, got, want)
		}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "FastMCP"}, nil)
	addLookup(server)
	installOn(t, server, Config{ReportMode: "on"})
	cs := connect(t, server)
	if got := cs.InitializeResult().Instructions; strings.Contains(got, "FastMCP") || !strings.Contains(got, "acme_annotate") {
		t.Errorf("instructions = %q", got)
	}
}

func TestAnUnusedAnnotationToolNameWarns(t *testing.T) {
	logs := captureLogs(t)
	server := newVectorServer()
	addLookup(server)
	installOn(t, server, Config{AnnotationToolName: "acme_report"})
	installOn(t, newVectorServer(), Config{AnnotationToolName: "acme_report", ReportMode: "on"})
	if strings.Count(logs.String(), "has no effect") != 1 {
		t.Errorf("want one warning, for ReportMode off only:\n%s", logs)
	}
}

// SPEC §3.4 rung 4: the Mcp-Session-Id a stateful server issues is the session.
func TestStatefulHTTPUsesTheSessionID(t *testing.T) {
	server, sink := installWithSink(t, Config{}, addLookup)
	cs := connectOver(t, &mcp.StreamableClientTransport{Endpoint: serveHTTP(t, server, false, nil)})
	if cs.ID() == "" {
		t.Skip("no Mcp-Session-Id was issued")
	}
	for range 2 {
		call(t, cs, "lookup", map[string]any{"name": "a"})
	}
	for _, e := range ofType(sink.all(), "tool_call_start") {
		if e.SessionID != cs.ID() || e.SessionIDOrigin == nil || *e.SessionIDOrigin != "observed" {
			t.Errorf("session %q (%v), want %q observed", e.SessionID, e.SessionIDOrigin, cs.ID())
		}
	}
}

// go-sdk's stateless mode issues an Mcp-Session-Id the client echoes, unless
// GetSessionID returns "" or go-sdk is v1.7+, which issues none. Both must work.
func TestStatelessHTTP(t *testing.T) {
	for name, c := range map[string]struct {
		getSessionID func() string
		sessions     int
		origin       string
	}{
		"no session id":     {func() string { return "" }, 4, "minted"},
		"server issues one": {nil, 1, "observed"},
	} {
		t.Run(name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "spec-vector-generator"}, &mcp.ServerOptions{GetSessionID: c.getSessionID})
			addLookup(server)
			sink := installOn(t, server, Config{ReportMode: "on"})
			cs := connectOver(t, &mcp.StreamableClientTransport{Endpoint: serveHTTP(t, server, false, &mcp.StreamableHTTPOptions{Stateless: true})})
			if c.getSessionID == nil && goSDKv17() {
				c.sessions, c.origin = 4, "minted"
			}

			tools, err := cs.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(tools.Tools) != 2 || tools.Tools[1].Name != "spec-vector-generator_annotate" {
				t.Fatalf("tools = %v", tools.Tools)
			}
			for range 2 {
				call(t, cs, "lookup", map[string]any{"name": "a", "user_goal": "1: x; success is y"})
			}
			call(t, cs, "spec-vector-generator_annotate", map[string]any{"user_goal": "1: x", "what_happened": "bad", "tool_name": "lookup"})

			events := sink.all()
			sessions := map[string][]string{}
			for _, e := range events {
				if e.EventType == "surface_snapshot" {
					continue
				}
				if e.TransportObserved == nil || *e.TransportObserved != "http" || e.SessionIDOrigin == nil || *e.SessionIDOrigin != c.origin {
					t.Errorf("%s: transport %v origin %v", e.EventType, e.TransportObserved, e.SessionIDOrigin)
				}
				sessions[e.SessionID] = append(sessions[e.SessionID], e.EventType)
			}
			// SPEC §3.4: rung 4 when the header is there, else rung 5, one
			// session per request (the listing, two calls, the annotation),
			// with each request's start and end together.
			if len(sessions) != c.sessions {
				t.Fatalf("want %d sessions, got %v", c.sessions, sessions)
			}
			if n := len(ofType(events, "annotation")); n != 1 {
				t.Errorf("want 1 annotation, got %d", n)
			}
			for _, s := range ofType(events, "tool_call_start") {
				if p := s.Payload.(toolCallStart); p.CallIntent == nil {
					t.Errorf("no call_intent over stateless HTTP")
				}
			}
		})
	}
}

func TestResultCaptureOff(t *testing.T) {
	server, sink := installWithSink(t, Config{ResultCapture: "off"}, func(s *mcp.Server) {
		addLookup(s)
		mcp.AddTool(s, &mcp.Tool{Name: "secret"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return nil, nil, errors.New("row 42 belongs to bob")
		})
		mcp.AddTool(s, &mcp.Tool{Name: "public"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return nil, nil, &PublicError{Message: "try a shorter name"}
		})
	})
	cs := connect(t, server)
	call(t, cs, "lookup", map[string]any{"name": "a"})
	call(t, cs, "secret", map[string]any{})
	call(t, cs, "public", map[string]any{})
	events := sink.all()
	end := ofType(events, "tool_call_end")[0].Payload.(toolCallEnd)
	if end.Result != nil || end.ResultCapture == nil || *end.ResultCapture != "off" {
		t.Errorf("end = %+v", end)
	}
	errs := ofType(events, "tool_call_error")
	secret, public := errs[0].Payload.(toolCallError), errs[1].Payload.(toolCallError)
	if *secret.ErrorBody != "" || secret.ResultCapture == nil {
		t.Errorf("secret message sent: %+v", secret)
	}
	if *public.ErrorBody != "try a shorter name" || public.ResultCapture != nil {
		t.Errorf("public message withheld: %+v", public)
	}
}

func TestUnknownTool(t *testing.T) {
	server, sink := installWithSink(t, Config{}, addLookup)
	cs := connect(t, server)
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "nope"}); err == nil {
		t.Fatal("want a JSON-RPC error")
	}
	failure := ofType(sink.all(), "tool_call_error")[0].Payload.(toolCallError)
	if failure.FailureKind == nil || *failure.FailureKind != "unknown_tool" {
		t.Errorf("failure = %+v", failure)
	}
}

func TestResolverFailureNeverBreaksTheCall(t *testing.T) {
	for name, hook := range map[string]ResolvePrincipal{
		"error": func(context.Context, Caller) (*Principal, error) { return nil, errors.New("down") },
		"panic": func(context.Context, Caller) (*Principal, error) { panic("boom") },
		"blank": func(context.Context, Caller) (*Principal, error) {
			return &Principal{ID: " \u00a0\u2003"}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			server, sink := installWithSink(t, Config{ResolvePrincipal: hook}, addLookup)
			res := call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
			if res.IsError {
				t.Fatal("call failed")
			}
			if start := ofType(sink.all(), "tool_call_start"); len(start) != 1 || start[0].Principal != nil {
				t.Errorf("start = %+v", start)
			}
		})
	}
}

func TestTheResolverSeesTheRequest(t *testing.T) {
	var got Caller
	hook := func(_ context.Context, c Caller) (*Principal, error) { got = c; return nil, nil }
	server, _ := installWithSink(t, Config{ResolvePrincipal: hook}, addLookup)
	cs := connectOver(t, &mcp.StreamableClientTransport{Endpoint: serveHTTP(t, server, false, nil)})
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "lookup", Arguments: map[string]any{"name": "a", "user_goal": "1: find a"}, Meta: mcp.Meta{"trace": "t1"},
	}); err != nil {
		t.Fatal(err)
	}
	if got.ToolName != "lookup" || !reflect.DeepEqual(got.Arguments, map[string]any{"name": "a"}) ||
		got.Meta["trace"] != "t1" || got.Header.Get("Content-Type") == "" || got.Token != nil {
		t.Errorf("caller = %+v", got)
	}
}

func TestOnlyTimedOutResolversCountTowardTheCeiling(t *testing.T) {
	// Resolvers another test gave up on count too; wait for them to end.
	for deadline := time.Now().Add(5 * time.Second); stuckHooks.Load() > 0; {
		if time.Now().After(deadline) {
			t.Fatalf("%d resolvers from other tests are still stuck", stuckHooks.Load())
		}
		time.Sleep(time.Millisecond)
	}
	logs := captureLogs(t)
	var called atomic.Int64
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	t.Cleanup(stop)
	var arrived sync.WaitGroup
	arrived.Add(hookCeiling + 6)
	var phase atomic.Int32 // 0 all at once, 1 stuck, 2 plain
	hook := func(context.Context, Caller) (*Principal, error) {
		called.Add(1)
		switch phase.Load() {
		case 0:
			arrived.Done()
			arrived.Wait() // every call is in its hook at once
		case 1:
			<-release
		}
		return &Principal{ID: "u"}, nil
	}
	server, sink := installWithSink(t, Config{ResolvePrincipal: hook}, addLookup)
	cs := connect(t, server)
	var calls sync.WaitGroup
	for range hookCeiling + 6 {
		calls.Go(func() { call(t, cs, "lookup", map[string]any{"name": "a"}) })
	}
	calls.Wait()
	for _, e := range ofType(sink.all(), "tool_call_start") {
		if e.Principal == nil {
			t.Fatalf("%d hooks at once, healthy, and one call lost its principal", hookCeiling+6)
		}
	}

	phase.Store(1)
	hookTimeout = 10 * time.Millisecond
	t.Cleanup(func() { hookTimeout = 5 * time.Second })
	called.Store(0)
	for range hookCeiling + 2 {
		call(t, cs, "lookup", map[string]any{"name": "a"})
	}
	for deadline := time.Now().Add(time.Second); called.Load() < hookCeiling && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if n := called.Load(); n != hookCeiling || strings.Count(logs.String(), "not called until one ends") != 1 {
		t.Errorf("resolver called %d times, want %d, and one warning:\n%s", n, hookCeiling, logs)
	}

	// Once they end, the resolver is called again.
	phase.Store(2)
	stop()
	hookTimeout = 5 * time.Second
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		call(t, cs, "lookup", map[string]any{"name": "a"})
		if starts := ofType(sink.all(), "tool_call_start"); starts[len(starts)-1].Principal != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Error("the resolver was not called again after the timed-out ones ended")
}

func TestTheResolverGetsCopies(t *testing.T) {
	var got []Caller
	hook := func(_ context.Context, c Caller) (*Principal, error) {
		got = append(got, c)
		if c.Header != nil {
			c.Header.Set("User-Agent", "changed by the hook")
		}
		return nil, nil
	}
	server, sink := installWithSink(t, Config{ResolvePrincipal: hook, ReportMode: "on"}, addLookup)
	cs := connectOver(t, &mcp.StreamableClientTransport{Endpoint: serveHTTP(t, server, false, nil)})
	call(t, cs, "lookup", map[string]any{"name": "a"})
	call(t, cs, slugAnnotateName("spec-vector-generator"), map[string]any{"user_goal": "1: g", "tool_name": "lookup", "what_happened": "w"})
	if raw, _ := json.Marshal(ofType(sink.all(), "tool_call_start")[0].ClientObserved); strings.Contains(string(raw), "changed by the hook") {
		t.Errorf("client_observed took the hook's edit: %s", raw)
	}
	if len(got) != 2 || got[1].Arguments["user_goal"] != nil || got[1].Arguments["what_happened"] != "w" {
		t.Errorf("callers = %+v", got)
	}
}

func TestTheResolversTokenIsACopy(t *testing.T) {
	type claims struct{ Role string }
	token := &auth.TokenInfo{Scopes: []string{"read"}, Extra: map[string]any{"sub": "u-1", "claims": &claims{"admin"}}}
	c := callerOf(&mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "lookup"}}, nil, token, nil)
	if _, typed := c.Token.Extra["claims"].(*claims); !typed {
		t.Errorf("a claim lost its type: %T", c.Token.Extra["claims"])
	}
	c.Token.Extra["sub"], c.Token.Scopes[0] = "changed", "changed"
	if token.Extra["sub"] != "u-1" || token.Scopes[0] != "read" {
		t.Errorf("the hook's edit reached the request's token: %+v", token)
	}
}

func TestAResolverThatStopsForItsContextIsATimeout(t *testing.T) {
	logs := captureLogs(t)
	hookTimeout = 20 * time.Millisecond
	t.Cleanup(func() { hookTimeout = 5 * time.Second })
	hook := func(ctx context.Context, _ Caller) (*Principal, error) { <-ctx.Done(); return nil, ctx.Err() }
	server, _ := installWithSink(t, Config{ResolvePrincipal: hook}, addLookup)
	cs := connect(t, server)
	for range 3 {
		call(t, cs, "lookup", map[string]any{"name": "a"})
	}
	if strings.Contains(logs.String(), "ResolvePrincipal failed") || strings.Count(logs.String(), "took longer than") != 1 {
		t.Errorf("want one timeout warning and no failure:\n%s", logs)
	}
}

func TestACancelledRequestDoesNotCallTheResolver(t *testing.T) {
	logs := captureLogs(t)
	called := make(chan struct{}, 1)
	b := &installed{Baton: &Baton{cfg: Config{ResolvePrincipal: func(context.Context, Caller) (*Principal, error) { called <- struct{}{}; return nil, nil }}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.resolveCaller(ctx, func() Caller { return Caller{} })
	select {
	case <-called:
		t.Error("the resolver was called for a cancelled request")
	case <-time.After(50 * time.Millisecond):
	}
	if strings.Contains(logs.String(), "took longer") {
		t.Errorf("a cancel was logged as slow:\n%s", logs)
	}
}

func TestTheResolverOnARequestThatIsNotACall(t *testing.T) {
	var got []Caller
	hook := func(_ context.Context, c Caller) (*Principal, error) { got = append(got, c); return nil, nil }
	server, _ := installWithSink(t, Config{ResolvePrincipal: hook}, addLookup)
	if _, err := connect(t, server).ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ToolName != "" || got[0].Arguments == nil || len(got[0].Arguments) != 0 || got[0].Header != nil {
		t.Errorf("callers = %+v", got)
	}
}

func TestTheOAuthHelpers(t *testing.T) {
	token := func(extra map[string]any) Caller {
		return Caller{Token: &auth.TokenInfo{UserID: "client-app", Extra: extra}}
	}
	for name, c := range map[string]struct {
		hook   ResolvePrincipal
		caller Caller
		want   *Principal
	}{
		"sub":           {PrincipalFromOAuthSub, token(map[string]any{"sub": "u-1"}), &Principal{ID: "u-1"}},
		"no sub":        {PrincipalFromOAuthSub, token(map[string]any{"sub": " "}), nil},
		"no token":      {PrincipalFromOAuthSub, Caller{}, nil},
		"email":         {PrincipalFromOAuthEmail, token(map[string]any{"email": "jane@acme.com"}), &Principal{ID: "jane@acme.com", DisplayName: "jane"}},
		"no local part": {PrincipalFromOAuthEmail, token(map[string]any{"email": "@acme.com"}), &Principal{ID: "@acme.com"}},
		"not a string":  {PrincipalFromOAuthEmail, token(map[string]any{"email": 7}), nil},
	} {
		if p, err := c.hook(context.Background(), c.caller); err != nil || !reflect.DeepEqual(p, c.want) {
			t.Errorf("%s: %+v, %v; want %+v", name, p, err, c.want)
		}
	}
}

func TestASlowResolverIsCutOff(t *testing.T) {
	logs := captureLogs(t)
	hookTimeout = 50 * time.Millisecond
	t.Cleanup(func() { hookTimeout = 5 * time.Second })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	slow := func(context.Context, Caller) (*Principal, error) {
		<-release
		return &Principal{ID: "late"}, nil
	}
	server, sink := installWithSink(t, Config{ResolvePrincipal: slow}, addLookup)
	began := time.Now()
	call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	if took := time.Since(began); took > time.Second {
		t.Errorf("the call took %v; a slow resolver must not hold it", took)
	}
	if start := ofType(sink.all(), "tool_call_start"); len(start) != 1 || start[0].Principal != nil {
		t.Errorf("start = %+v", start)
	}
	if !strings.Contains(logs.String(), "took longer than") {
		t.Errorf("no warning logged:\n%s", logs)
	}
}

func TestScrubberFixedSets(t *testing.T) {
	for in, want := range map[string]string{
		"mail a@b.co":                   "mail [REDACTED:email]",
		"Bearer\u00a0abcdefghijklmnop1": "[REDACTED:bearer]",
		"call 555-123-4567":             "call [REDACTED:phone]",
		"card 4111111111111111":         "card [REDACTED:cc]",
		"id 4111111111111112":           "id 4111111111111112",
		"٥٥٥١٢٣٤٥٦٧":                    "٥٥٥١٢٣٤٥٦٧",
		"Bearer\u2003abcdefghijklmnop1": "Bearer\u2003abcdefghijklmnop1",
	} {
		if got := DefaultScrubber(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	got := DefaultScrubber(map[string]any{"Password": "hunter2", "n": 1})
	if !reflect.DeepEqual(got, map[string]any{"Password": "[REDACTED:field-password]", "n": 1}) {
		t.Errorf("field rule: %v", got)
	}
}

func TestTheOffSwitchInstallsNothingAndChecksNothing(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", " ON "} {
		t.Setenv("BATON_DISABLED", v)
		if handle, err := Install(nil, Config{DSN: "not a dsn", ResultCapture: "bogus"}); err != nil || handle == nil {
			t.Errorf("BATON_DISABLED=%q: handle %v, err %v", v, handle, err)
		}
		server := newVectorServer()
		addLookup(server)
		handle, err := Install(server, Config{DSN: testDSN, ReportMode: "on"})
		if err != nil {
			t.Fatal(err)
		}
		stopsWithTheHandle(t, handle)
		handle.Close(context.Background())
		cs := connect(t, server)
		if got := cs.InitializeResult().Instructions; got != "" {
			t.Errorf("BATON_DISABLED=%q added instructions: %q", v, got)
		}
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Tools) != 1 || strings.Contains(string(mustJSON(t, res.Tools[0].InputSchema)), userGoalParam) {
			t.Errorf("BATON_DISABLED=%q changed the tools: %s", v, mustJSON(t, res.Tools))
		}
	}
	t.Setenv("BATON_DISABLED", "")
	os.Unsetenv("BATON_DISABLED")
	for _, v := range []string{"unset", "", "0", "false", "no", "off"} {
		if v != "unset" {
			t.Setenv("BATON_DISABLED", v)
		}
		if _, err := Install(newVectorServer(), Config{DSN: "not a dsn"}); err == nil {
			t.Errorf("BATON_DISABLED=%q switched capture off", v)
		}
	}
}
