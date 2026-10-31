package baton

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func installWithSink(t *testing.T, cfg Config, register func(*mcp.Server)) (*mcp.Server, *memorySink) {
	t.Helper()
	server := newVectorServer()
	register(server)
	sink := &memorySink{}
	cfg.sink = sink
	if cfg.VendorID == "" {
		cfg.VendorID = "acme"
	}
	if _, err := Install(server, cfg); err != nil {
		t.Fatal(err)
	}
	return server, sink
}

func connectOver(t *testing.T, transport mcp.Transport) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), transport, nil)
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

func TestInstructionsGoAfterTheVendors(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "acme"}, &mcp.ServerOptions{Instructions: "Vendor text."})
	addLookup(server)
	if _, err := Install(server, Config{VendorID: "acme", ReportMode: "on", sink: &memorySink{}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, server)
	got := cs.InitializeResult().Instructions
	want := "Vendor text.\n\nUse `acme_annotate` to report a acme tool call that went wrong, or a request no tool covers; its description says when and how." + instructionsSubagentClause
	if got != want {
		t.Errorf("instructions =\n%q\nwant\n%q", got, want)
	}
}

// go-sdk's stateless mode still issues an Mcp-Session-Id the client echoes,
// unless the vendor's GetSessionID returns "". Both must work.
func TestStatelessHTTP(t *testing.T) {
	for name, c := range map[string]struct {
		getSessionID func() string
		sessions     int
		origin       string
	}{
		"no session id":     {func() string { return "" }, 3, "minted"},
		"server issues one": {nil, 1, "observed"},
	} {
		t.Run(name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "spec-vector-generator"}, &mcp.ServerOptions{GetSessionID: c.getSessionID})
			addLookup(server)
			sink := &memorySink{}
			if _, err := Install(server, Config{VendorID: "acme", ReportMode: "on", sink: sink}); err != nil {
				t.Fatal(err)
			}
			ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true}))
			t.Cleanup(ts.Close)
			cs := connectOver(t, &mcp.StreamableClientTransport{Endpoint: ts.URL})

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
				if e.TransportObserved == nil || *e.TransportObserved != "http" || e.SessionIDOrigin != c.origin {
					t.Errorf("%s: transport %v origin %s", e.EventType, e.TransportObserved, e.SessionIDOrigin)
				}
				sessions[e.SessionID] = append(sessions[e.SessionID], e.EventType)
			}
			// SPEC §3.4: rung 4 when the header is there, else rung 5, one
			// session per request, with each call's start and end together.
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
		"error": func(context.Context, *auth.TokenInfo) (*Principal, error) { return nil, errors.New("down") },
		"panic": func(context.Context, *auth.TokenInfo) (*Principal, error) { panic("boom") },
		"blank": func(context.Context, *auth.TokenInfo) (*Principal, error) {
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
