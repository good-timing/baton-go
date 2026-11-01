package baton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// go-sdk's typed AddTool rounds such a number itself, so this uses a raw
// handler, which sees the arguments exactly as sent.
func TestLargeIntegersReachTheVendorExactly(t *testing.T) {
	var got json.RawMessage
	server, sink := installWithSink(t, Config{}, func(s *mcp.Server) {
		s.AddTool(&mcp.Tool{Name: "row", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			got = r.Params.Arguments
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
		})
	})
	cs := connect(t, server)
	raw := json.RawMessage(`{"id":9007199254740993,"user_goal":"1: x; success is y"}`)
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "row", Arguments: raw}); err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"id":9007199254740993}` {
		t.Errorf("vendor got %s", got)
	}
	start, _ := json.Marshal(ofType(sink.all(), "tool_call_start")[0].Payload)
	if !strings.Contains(string(start), `"id":9007199254740993`) {
		t.Errorf("event params: %s", start)
	}
}

func TestSSEClientsGetTheirOwnSessions(t *testing.T) {
	server, sink := installWithSink(t, Config{}, addLookup)
	url := serveHTTP(t, server, true, nil)
	for range 2 {
		cs := connectOver(t, &mcp.SSEClientTransport{Endpoint: url})
		call(t, cs, "lookup", map[string]any{"name": "a"})
		call(t, cs, "lookup", map[string]any{"name": "b"})
	}
	sessions := map[string]bool{}
	for _, e := range ofType(sink.all(), "tool_call_start") {
		sessions[e.SessionID] = true
	}
	if len(sessions) != 2 {
		t.Fatalf("want one session per SSE client, got %v", sessions)
	}
}

func TestSequenceNumbers(t *testing.T) {
	seq := newSequencer()
	minted := callSession{id: "sdk-x", origin: "minted"}
	if a, b := seq.next(minted), seq.next(minted); a != 1 || b != 2 {
		t.Errorf("minted session numbered %d, %d", a, b)
	}
	// An observed id can span processes, so it is numbered by time too.
	observed := callSession{id: "client-session", origin: "observed"}
	if n := seq.next(observed); n < 1_000_000_000_000 {
		t.Errorf("observed session numbered %d", n)
	}
	small := newSequencer()
	small.counts = newLRU[string, int64](2)
	small.next(minted)
	for i := range 5 {
		small.next(minted)
		small.next(callSession{id: fmt.Sprint("other", i), origin: "minted"})
	}
	if n := small.next(minted); n != 7 {
		t.Errorf("a session in use was dropped: numbered %d, want 7", n)
	}
}

func TestMetaCoordinatesAreRounded(t *testing.T) {
	server, sink := installWithSink(t, Config{Scrubber: NoScrub}, addLookup)
	cs := connect(t, server)
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "lookup",
		Arguments: map[string]any{"name": "a"},
		Meta:      mcp.Meta{"openai/userLocation": map[string]any{"latitude": 37.774929, "longitude": "-122.419416", "city": "SF", "zoom": 12}},
	})
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(ofType(sink.all(), "tool_call_start")[0].RuntimeMeta)
	want := `{"openai/userLocation":{"city":"SF","latitude":37.8,"longitude":"-122.4","zoom":12}}`
	if string(meta) != want {
		t.Errorf("runtime_meta = %s, want %s", meta, want)
	}
}

func TestAVendorToolWithTheAnnotateNameWins(t *testing.T) {
	ran := false
	server, sink := installWithSink(t, Config{ReportMode: "on", AnnotationToolName: "lookup"}, func(s *mcp.Server) {
		mcp.AddTool(s, &mcp.Tool{Name: "lookup"}, func(_ context.Context, r *mcp.CallToolRequest, in lookupIn) (*mcp.CallToolResult, lookupOut, error) {
			ran = true
			return nil, lookupOut{Found: true, Name: in.Name}, nil
		})
	})
	cs := connect(t, server)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 {
		t.Errorf("want only the vendor's tool, got %d", len(tools.Tools))
	}
	call(t, cs, "lookup", map[string]any{"name": "a"})
	if !ran || len(ofType(sink.all(), "annotation")) != 0 {
		t.Errorf("vendor ran: %v; events %v", ran, sink.all())
	}
}

func TestWrappedPublicErrorKeepsItsMessage(t *testing.T) {
	server, sink := installWithSink(t, Config{ResultCapture: "off"}, func(s *mcp.Server) {
		mcp.AddTool(s, &mcp.Tool{Name: "p"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return nil, nil, fmt.Errorf("lookup row 42: %w", &PublicError{Message: "try a shorter name"})
		})
	})
	call(t, connect(t, server), "p", map[string]any{})
	failure := ofType(sink.all(), "tool_call_error")[0].Payload.(toolCallError)
	if *failure.ErrorBody != "try a shorter name" || failure.ResultCapture != nil {
		t.Errorf("failure = %+v, body %q", failure, *failure.ErrorBody)
	}
}

func TestInstructionsCapCountsCharacters(t *testing.T) {
	name := strings.Repeat("名", 480)
	if _, err := reportPointer(name, "acme_annotate"); err != nil {
		t.Errorf("480 characters rejected: %v", err)
	}
}

// The listing Baton makes for each call carries the caller's Extra, so a
// vendor's auth middleware sees the same caller.
func TestVendorMiddlewareSeesTheCallersExtra(t *testing.T) {
	server := newVectorServer()
	addLookup(server)
	sawAnonymous := false
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/list" && req.GetExtra() == nil {
				sawAnonymous = true
			}
			return next(ctx, method, req)
		}
	})
	installOn(t, server, Config{})
	call(t, connectOver(t, &mcp.StreamableClientTransport{Endpoint: serveHTTP(t, server, false, nil)}), "lookup", map[string]any{"name": "a"})
	if sawAnonymous {
		t.Error("vendor middleware saw a tools/list with no Extra")
	}
}

func TestInstructionsFollowWhatIsServed(t *testing.T) {
	declaresGoal := func(s *mcp.Server) {
		s.AddTool(&mcp.Tool{Name: "own", InputSchema: json.RawMessage(`{"type":"object","properties":{"user_goal":{"type":"string"}}}`)},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
	}
	for name, c := range map[string]struct {
		cfg      Config
		register func(*mcp.Server)
		pointer  bool
		clause   bool
	}{
		"report on":                 {Config{ReportMode: "on"}, addLookup, true, true},
		"report off":                {Config{}, addLookup, false, true},
		"vendor declares user_goal": {Config{ReportMode: "on"}, declaresGoal, true, false},
		"vendor owns the name":      {Config{ReportMode: "on", AnnotationToolName: "lookup"}, addLookup, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := installWithSink(t, c.cfg, c.register)
			got := connect(t, server).InitializeResult().Instructions
			if strings.Contains(got, "to report a") != c.pointer || strings.Contains(got, "subagent") != c.clause {
				t.Errorf("instructions = %q", got)
			}
		})
	}
}

// A vendor auth middleware that refuses callers with no token refuses Baton's
// listing too, since it carries the caller's Extra. Baton must then conclude
// nothing from the listing, and must not answer the annotate tool for a
// refused caller. Refusing the probe's initialize must not stop Install.
func TestARefusedListingConcludesNothing(t *testing.T) {
	server := newVectorServer()
	addLookup(server)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if init, ok := req.(*mcp.ServerRequest[*mcp.InitializeParams]); ok && init.Params.ClientInfo.Name == "baton-probe" {
				return nil, errors.New("unauthenticated")
			}
			if method == "tools/list" && (req.GetExtra() == nil || req.GetExtra().TokenInfo == nil) {
				return nil, errors.New("unauthenticated")
			}
			return next(ctx, method, req)
		}
	})
	sink := installOn(t, server, Config{ReportMode: "on"})
	cs := connect(t, server)
	if res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "acme_annotate", Arguments: map[string]any{"what_happened": "x"}}); err == nil && !res.IsError {
		t.Error("Baton answered the annotate tool for a refused caller")
	}
	call(t, cs, "lookup", map[string]any{"name": "a", "user_goal": "1: x; success is y"})
	events := sink.all()
	if len(ofType(events, "surface_snapshot")) != 0 || len(ofType(events, "annotation")) != 0 {
		t.Errorf("events = %v", events)
	}
	for _, e := range ofType(events, "tool_call_error") {
		if f := e.Payload.(toolCallError).FailureKind; f != nil {
			t.Errorf("failure_kind %q sent with no listing", *f)
		}
	}
	if len(ofType(events, "tool_call_end")) != 1 {
		t.Errorf("lookup did not end normally: %v", events)
	}
}

func TestAHandlerReturningNothingStillEnds(t *testing.T) {
	server, sink := installWithSink(t, Config{}, func(s *mcp.Server) {
		s.AddTool(&mcp.Tool{Name: "empty", InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil })
	})
	cs := connect(t, server)
	_, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "empty"})
	if n := len(ofType(sink.all(), "tool_call_end")) + len(ofType(sink.all(), "tool_call_error")); n != 1 {
		t.Errorf("want one end or error, got %d", n)
	}
}

func TestCoordinatesRoundLikePython(t *testing.T) {
	for in, want := range map[string]string{"37.25": "37.2", "0.15": "0.1", "37.774929": "37.8", "-122.45": "-122.5"} {
		got, _ := roundCoordinate(json.Number(in))
		if got != json.Number(want) {
			t.Errorf("%s → %v, want %s", in, got, want)
		}
	}
}

func TestSnapshotIsOnTheProcessSession(t *testing.T) {
	server, sink := installWithSink(t, Config{}, addLookup)
	call(t, connectOver(t, &mcp.StreamableClientTransport{Endpoint: serveHTTP(t, server, false, nil)}), "lookup", map[string]any{"name": "a"})
	snap := ofType(sink.all(), "surface_snapshot")[0]
	start := ofType(sink.all(), "tool_call_start")[0]
	if snap.SessionIDOrigin == nil || *snap.SessionIDOrigin != "minted" || snap.SessionID == start.SessionID || snap.SequenceNumber != 1 {
		t.Errorf("snapshot session %s (%v) #%d, call session %s", snap.SessionID, snap.SessionIDOrigin, snap.SequenceNumber, start.SessionID)
	}
}

func TestTransportObserved(t *testing.T) {
	inMemory := func(t *testing.T, s *mcp.Server) *mcp.ClientSession { return connect(t, s) }
	overSSE := func(t *testing.T, s *mcp.Server) *mcp.ClientSession {
		return connectOver(t, &mcp.SSEClientTransport{Endpoint: serveHTTP(t, s, true, nil)})
	}
	overHTTP := func(t *testing.T, s *mcp.Server) *mcp.ClientSession {
		return connectOver(t, &mcp.StreamableClientTransport{Endpoint: serveHTTP(t, s, false, nil)})
	}
	for name, c := range map[string]struct {
		connect func(*testing.T, *mcp.Server) *mcp.ClientSession
		want    string // "" for null: go-sdk shows SSE no headers
	}{
		"in-memory": {inMemory, "no-http-request"},
		"SSE":       {overSSE, ""},
		"HTTP":      {overHTTP, "http"},
	} {
		t.Run(name, func(t *testing.T) {
			server, sink := installWithSink(t, Config{}, addLookup)
			call(t, c.connect(t, server), "lookup", map[string]any{"name": "a"})
			got := ofType(sink.all(), "tool_call_start")[0].TransportObserved
			if (got == nil) != (c.want == "") || (got != nil && *got != c.want) {
				t.Errorf("transport_observed = %v, want %q", got, c.want)
			}
		})
	}
}

// SPEC §3.4 rung 5: a request with no session id is its own session, shared
// with nothing else (not even the snapshot), and numbered by the clock.
func TestPerRequestSessions(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "s"}, &mcp.ServerOptions{GetSessionID: func() string { return "" }})
	addLookup(server)
	sink := installOn(t, server, Config{})
	call(t, connectOver(t, &mcp.StreamableClientTransport{Endpoint: serveHTTP(t, server, false, &mcp.StreamableHTTPOptions{Stateless: true})}), "lookup", map[string]any{"name": "a"})
	events := sink.all()
	snap, start, end := ofType(events, "surface_snapshot")[0], ofType(events, "tool_call_start")[0], ofType(events, "tool_call_end")[0]
	if snap.SessionID == start.SessionID || start.SessionID != end.SessionID {
		t.Errorf("snapshot %s, start %s, end %s", snap.SessionID, start.SessionID, end.SessionID)
	}
	if start.SequenceNumber < 1_000_000_000_000 || end.SequenceNumber <= start.SequenceNumber {
		t.Errorf("per-request numbers %d, %d are not clock-based", start.SequenceNumber, end.SequenceNumber)
	}
}

func TestUnusableIdentityIsDropped(t *testing.T) {
	for _, id := range []string{"user\x00", "\xff\xfe42", "  "} {
		if w := wireIdentity(id, "", false); w != nil {
			t.Errorf("%q gave a principal", id)
		}
	}
	long := strings.Repeat("é", 200)
	if w := wireIdentity(long, long, false); w == nil || utf8.RuneCountInString(w.ID) != identityCap || w.DisplayName != nil {
		t.Errorf("long identity: %+v", w)
	}
}

func TestAnnotationContextKeepsLargeIntegers(t *testing.T) {
	server, sink := installWithSink(t, Config{ReportMode: "on"}, addLookup)
	cs := connect(t, server)
	raw := json.RawMessage(`{"user_goal":"1: x","what_happened":"bad","tool_name":"lookup","context":{"row_id":9007199254740993}}`)
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "spec-vector-generator_annotate", Arguments: raw}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(ofType(sink.all(), "annotation")[0].Payload)
	if !strings.Contains(string(payload), `"row_id":9007199254740993`) {
		t.Errorf("annotation: %s", payload)
	}
}

type notFound struct{}

func (notFound) Error() string { return "not found" }

func TestErrorTypeLooksThroughFmtWrappers(t *testing.T) {
	for err, want := range map[error]string{
		fmt.Errorf("lookup: %w", notFound{}):                        "baton.notFound",
		fmt.Errorf("a: %w", fmt.Errorf("b: %w", &PublicError{"x"})): "*baton.PublicError",
		errors.New("plain"):                                         "*errors.errorString",
		fmt.Errorf("no wrap: %v", notFound{}):                       "*errors.errorString",
	} {
		if got := errorType(err); got != want {
			t.Errorf("%v: %s, want %s", err, got, want)
		}
	}
}
