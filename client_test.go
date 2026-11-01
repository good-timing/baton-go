package baton

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func clientOf(t *testing.T, e Event) map[string]any {
	t.Helper()
	got, _ := asJSON(t, []Event{e})[0]["client_observed"].(map[string]any)
	return got
}

func TestTheHandshakeNamesTheClient(t *testing.T) {
	server, sink := installWithSink(t, Config{ReportMode: "on"}, addLookup)
	cs := connect(t, server) // declares test-client, version 1
	call(t, cs, "lookup", map[string]any{"name": "alice"})
	call(t, cs, slugAnnotateName("spec-vector-generator"), map[string]any{"user_goal": "g", "tool_name": "lookup", "what_happened": "w"})
	want := map[string]any{"info": map[string]any{"name": "test-client", "version": "1"}}
	for _, e := range sink.all() {
		got := clientOf(t, e)
		if e.EventType == "surface_snapshot" {
			if got != nil {
				t.Errorf("surface_snapshot carries client_observed %v", got)
			}
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: client_observed %v, want %v", e.EventType, got, want)
		}
	}
	if len(ofType(sink.all(), "annotation")) != 1 {
		t.Fatal("no annotation")
	}
}

func TestADeclarationOnTheRequestWinsAndIsScrubbedAndCut(t *testing.T) {
	server, sink := installWithSink(t, Config{}, addLookup)
	cs := connect(t, server)
	long := strings.Repeat("v", 200)
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "lookup",
		Arguments: map[string]any{"name": "alice"},
		Meta:      mcp.Meta{clientInfoMetaKey: map[string]any{"name": "bob@example.com", "version": long}},
	})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := clientOf(t, ofType(sink.all(), "tool_call_start")[0])["info"].(map[string]any)
	if info["name"] != "[REDACTED:email]" || utf8.RuneCountInString(info["version"].(string)) != clientInfoCap {
		t.Errorf("info %v", info)
	}
}

func TestAnUnusableRequestDeclarationFallsBackToTheHandshake(t *testing.T) {
	server, sink := installWithSink(t, Config{}, addLookup)
	cs := connect(t, server)
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "lookup",
		Arguments: map[string]any{"name": "alice"},
		Meta:      mcp.Meta{clientInfoMetaKey: map[string]any{"name": ""}},
	})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := clientOf(t, ofType(sink.all(), "tool_call_start")[0])["info"].(map[string]any)
	if info["name"] != "test-client" {
		t.Errorf("info %v", info)
	}
}

type addHeaders struct {
	base http.RoundTripper
	add  http.Header
}

func connectWithHeaders(t *testing.T, url string, headers http.Header) *mcp.ClientSession {
	t.Helper()
	client := &http.Client{Transport: addHeaders{http.DefaultTransport, headers}}
	return connectOver(t, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: client})
}

func (a addHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, vs := range a.add {
		r.Header[k] = vs
	}
	return a.base.RoundTrip(r)
}

func TestOnlyRegisteredHeadersAreCopied(t *testing.T) {
	for _, stateless := range []bool{false, true} {
		server, sink := installWithSink(t, Config{}, addLookup)
		url := serveHTTP(t, server, false, &mcp.StreamableHTTPOptions{Stateless: stateless})
		headers := http.Header{}
		headers.Set("User-Agent", "agent/1 "+strings.Repeat("x", 300))
		headers.Add("X-Anthropic-Client", "one")
		headers.Add("X-Anthropic-Client", "two")
		headers.Set("Authorization", "Bearer not-copied")
		cs := connectWithHeaders(t, url, headers)
		call(t, cs, "lookup", map[string]any{"name": "alice"})
		observed := clientOf(t, ofType(sink.all(), "tool_call_start")[0])
		if _, hasInfo := observed["info"]; hasInfo == stateless {
			t.Errorf("stateless=%v: info %v (a stateless request has no handshake)", stateless, observed["info"])
		}
		got, _ := observed["headers"].(map[string]any)
		ua, _ := got["user-agent"].(string)
		if len(got) != 2 || got["x-anthropic-client"] != "one, two" ||
			!strings.HasPrefix(ua, "agent/1 ") || utf8.RuneCountInString(ua) != headerValueCap {
			t.Errorf("stateless=%v: headers %v", stateless, got)
		}
	}
}

func TestAScrubberThatReturnsNoStringDropsTheValue(t *testing.T) {
	scrub := func(v any) any {
		if s, ok := v.(string); ok && s == "test-client" {
			return 42
		}
		return v
	}
	server, sink := installWithSink(t, Config{Scrubber: scrub}, addLookup)
	call(t, connect(t, server), "lookup", map[string]any{"name": "alice"})
	got := clientOf(t, ofType(sink.all(), "tool_call_start")[0])
	if !reflect.DeepEqual(got, map[string]any{"info": map[string]any{"version": "1"}}) {
		t.Errorf("client_observed %v", got)
	}
}

func TestAScrubberThatPanicsOnClientTextLosesOnlyThatValue(t *testing.T) {
	scrub := func(v any) any {
		if s, ok := v.(string); ok && (s == "c" || s == "agent/1") {
			panic("this scrubber expected a map")
		}
		return v
	}
	server, sink := installWithSink(t, Config{Scrubber: scrub}, addLookup)
	url := serveHTTP(t, server, false, nil)
	call(t, connectWithHeaders(t, url, http.Header{"User-Agent": {"agent/1"}, "X-Anthropic-Client": {"cli"}}), "lookup", map[string]any{"name": "alice"})
	if len(ofType(sink.all(), "tool_call_start")) != 1 || len(ofType(sink.all(), "tool_call_end")) != 1 {
		t.Fatalf("the call's events were lost: %v", sink.all())
	}
	got := clientOf(t, ofType(sink.all(), "tool_call_start")[0])
	if want := map[string]any{"info": map[string]any{"version": "1"}, "headers": map[string]any{"x-anthropic-client": "cli"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("client_observed %v, want %v", got, want)
	}
}

func TestCutRunes(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"héllo", 2, "hé"},
		{"abc", 5, "abc"},
		{"a\xe9bc", 2, "a\xe9"},
		{"", 3, ""},
	} {
		if got := cutRunes(c.in, c.n); got != c.want {
			t.Errorf("cutRunes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func callWithMeta(t *testing.T, cs *mcp.ClientSession, meta mcp.Meta) {
	t.Helper()
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "lookup", Arguments: map[string]any{"name": "alice"}, Meta: meta})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNameAndVersionComeFromOneSource(t *testing.T) {
	server, sink := installWithSink(t, Config{}, addLookup)
	callWithMeta(t, connect(t, server), mcp.Meta{clientInfoMetaKey: map[string]any{"name": "bob"}})
	got := clientOf(t, ofType(sink.all(), "tool_call_start")[0])
	if want := map[string]any{"info": map[string]any{"name": "bob"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("client_observed %v, want %v (no version from the handshake)", got, want)
	}
}

func TestAValueTheScrubberEmptiesIsLeftOut(t *testing.T) {
	scrub := func(v any) any {
		if s, ok := v.(string); ok && (s == "c" || s == "agent/1") {
			return ""
		}
		return v
	}
	server, sink := installWithSink(t, Config{Scrubber: scrub}, addLookup)
	url := serveHTTP(t, server, false, nil)
	call(t, connectWithHeaders(t, url, http.Header{"User-Agent": {"agent/1"}, "X-Anthropic-Client": {"cli"}}), "lookup", map[string]any{"name": "alice"})
	got := clientOf(t, ofType(sink.all(), "tool_call_start")[0])
	if want := map[string]any{"info": map[string]any{"version": "1"}, "headers": map[string]any{"x-anthropic-client": "cli"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("client_observed %v, want %v", got, want)
	}
}

func TestAScrubberThatPanicsBeforeTheEventCostsOnlyThatValue(t *testing.T) {
	logs := captureLogs(t)
	scrub := func(v any) any {
		if m, ok := v.(map[string]any); ok && m["trace"] == "t1" {
			panic("this scrubber cannot handle that")
		}
		if v == "find alice" {
			panic("this scrubber cannot handle that")
		}
		return v
	}
	server, sink := installWithSink(t, Config{Scrubber: scrub}, addLookup)
	cs := connect(t, server)
	for range 2 {
		callWithMeta(t, cs, mcp.Meta{"trace": "t1"})
	}
	call(t, cs, "lookup", map[string]any{"name": "alice", "user_goal": "find alice"})
	starts, ends := ofType(sink.all(), "tool_call_start"), ofType(sink.all(), "tool_call_end")
	if len(starts) != 3 || len(ends) != 3 {
		t.Fatalf("%d starts, %d ends", len(starts), len(ends))
	}
	for _, e := range asJSON(t, starts) {
		p := e["payload"].(map[string]any)
		if e["runtime_meta"] != nil || p["call_intent"] != nil || p["params"] == nil || e["client_observed"] == nil {
			t.Errorf("start %v", e)
		}
	}
	if n := strings.Count(logs.String(), "the Scrubber panicked"); n != 2 {
		t.Errorf("%d panic logs, want one per field:\n%s", n, logs)
	}
}

func panicsOn(match func(any) bool) func(any) any {
	return func(v any) any {
		if match(v) {
			panic("this scrubber cannot handle that")
		}
		return v
	}
}

func encodeFailedDrops() int {
	processDrops.mu.Lock()
	defer processDrops.mu.Unlock()
	return processDrops.total[dropEncodeFailed]
}

func TestAScrubberThatPanicsOnTheArgumentsSendsNoneOfThem(t *testing.T) {
	logs := captureLogs(t)
	scrub := panicsOn(func(v any) bool { m, ok := v.(map[string]any); return ok && m["name"] == "alice" })
	server, sink := installWithSink(t, Config{Scrubber: scrub}, addLookup)
	cs := connect(t, server)
	for range 2 {
		call(t, cs, "lookup", map[string]any{"name": "alice"})
	}
	starts, ends := ofType(sink.all(), "tool_call_start"), ofType(sink.all(), "tool_call_end")
	if len(starts) != 2 || len(ends) != 2 {
		t.Fatalf("%d starts, %d ends", len(starts), len(ends))
	}
	for _, e := range asJSON(t, starts) {
		if p := e["payload"].(map[string]any)["params"]; !reflect.DeepEqual(p, map[string]any{}) {
			t.Errorf("params %v, want {}", p)
		}
	}
	if n := encodeFailedDrops(); n != 0 {
		t.Errorf("%d drops counted, want 0: nothing was lost", n)
	}
	if n := strings.Count(logs.String(), "the Scrubber panicked"); n != 1 {
		t.Errorf("%d panic logs, want 1", n)
	}
}

func TestAScrubberThatPanicsOnAFailureDropsOnlyThatEventAndCountsIt(t *testing.T) {
	onText := func(v any) bool { s, ok := v.(string); return ok && strings.HasPrefix(s, "simulated") }
	onResult := func(v any) bool { m, ok := v.(map[string]any); return ok && m["isError"] == true }
	// A raised error has no result, so only the returned failure is lost there.
	for name, c := range map[string]struct {
		match func(any) bool
		lost  int
	}{"error text": {onText, 2}, "returned result": {onResult, 1}} {
		t.Run(name, func(t *testing.T) { scrubberPanicsOnAFailure(t, panicsOn(c.match), c.lost) })
	}
}

func scrubberPanicsOnAFailure(t *testing.T, scrub func(any) any, lost int) {
	captureLogs(t)
	server := newVectorServer()
	mcp.AddTool(server, &mcp.Tool{Name: "boom"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return nil, nil, errors.New("simulated failure")
	})
	mcp.AddTool(server, &mcp.Tool{Name: "soft_fail"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "simulated returned failure"}}}, nil, nil
	})
	_, sink := installAs(t, server, Config{Scrubber: scrub}, "acme")
	cs := connect(t, server)
	call(t, cs, "boom", nil)
	call(t, cs, "soft_fail", nil)
	if starts, errs := ofType(sink.all(), "tool_call_start"), ofType(sink.all(), "tool_call_error"); len(starts) != 2 || len(errs) != 2-lost {
		t.Fatalf("%d starts, %d errors: %v", len(starts), len(errs), asJSON(t, errs))
	}
	if n := encodeFailedDrops(); n != lost {
		t.Errorf("%d drops counted, want %d", n, lost)
	}
}

func TestAScrubberThatPanicsOnAResultDropsOnlyTheEndAndCountsIt(t *testing.T) {
	captureLogs(t)
	scrub := panicsOn(func(v any) bool { _, ok := v.([]any); return ok })
	server, sink := installWithSink(t, Config{Scrubber: scrub}, addLookup)
	call(t, connect(t, server), "lookup", map[string]any{"name": "alice"})
	if starts, ends := ofType(sink.all(), "tool_call_start"), ofType(sink.all(), "tool_call_end"); len(starts) != 1 || len(ends) != 0 {
		t.Fatalf("%d starts, %d ends", len(starts), len(ends))
	}
	if n := encodeFailedDrops(); n != 1 {
		t.Errorf("%d drops counted, want 1", n)
	}
}

func TestAScrubberThatPanicsOnAnAnnotationStillAnswersIt(t *testing.T) {
	captureLogs(t)
	scrub := panicsOn(func(v any) bool { return v == "boom" })
	server, sink := installWithSink(t, Config{Scrubber: scrub, ReportMode: "on"}, addLookup)
	res := call(t, connect(t, server), slugAnnotateName("spec-vector-generator"),
		map[string]any{"user_goal": "g", "tool_name": "lookup", "what_happened": "boom"})
	if res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, `"ok":true`) {
		t.Errorf("answer %v", res.Content)
	}
	if got := ofType(sink.all(), "annotation"); len(got) != 0 {
		t.Errorf("annotation sent: %v", asJSON(t, got))
	}
	if n := encodeFailedDrops(); n != 1 {
		t.Errorf("%d drops counted, want 1", n)
	}
}
