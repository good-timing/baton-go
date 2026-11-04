package baton

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addPromptsAndResources(s *mcp.Server) {
	addLookup(s)
	s.AddPrompt(&mcp.Prompt{Name: "fleet", Arguments: []*mcp.PromptArgument{{Name: "fleet"}}},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "secret prompt text"}}}}, nil
		})
	s.AddPrompt(&mcp.Prompt{Name: "broken"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return nil, errors.New("no such fleet")
	})
	s.AddResource(&mcp.Resource{URI: "docs://intro", Name: "intro"}, func(_ context.Context, r *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.Params.URI, Text: "secret doc text"}}}, nil
	})
}

func eventTypes(events []Event) []string {
	var out []string
	for _, e := range events {
		if e.EventType != "surface_snapshot" {
			out = append(out, e.EventType)
		}
	}
	return out
}

func TestPromptAndResourceRequestsAreRecorded(t *testing.T) {
	server, sink := installWithSink(t, Config{}, addPromptsAndResources)
	cs := connect(t, server)
	ctx := context.Background()
	if _, err := cs.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.ListPrompts(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "fleet", Arguments: map[string]string{"fleet": "f1", "note": "mail a@b.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.ListResources(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "docs://intro"}); err != nil {
		t.Fatal(err)
	}
	_, _ = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "docs://missing"})
	_, _ = cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "broken"})

	events := sink.all()
	want := []string{
		"tool_list_start", "tool_list_end", "prompt_list_start", "prompt_list_end",
		"prompt_get_start", "prompt_get_end", "resource_list_start", "resource_list_end",
		"resource_read_start", "resource_read_end", "resource_read_start", "resource_read_error",
		"prompt_get_start", "prompt_get_error",
	}
	if got := eventTypes(events); !slices.Equal(got, want) {
		t.Fatalf("events %v\nwant %v", got, want)
	}
	payload := func(eventType string) map[string]any {
		return asJSON(t, ofType(events, eventType))[0]["payload"].(map[string]any)
	}
	for eventType, want := range map[string]map[string]any{
		"tool_list_start":     {},
		"tool_list_end":       {"count": 1.0},
		"prompt_list_end":     {"count": 2.0},
		"prompt_get_start":    {"name": "fleet", "params": map[string]any{"fleet": "f1", "note": "mail [REDACTED:email]"}},
		"prompt_get_end":      {"name": "fleet"},
		"resource_list_end":   {"count": 1.0},
		"resource_read_start": {"uri": "docs://intro", "params": map[string]any{"uri": "docs://intro"}},
		"resource_read_end":   {"uri": "docs://intro"},
		"resource_read_error": {"uri": "docs://missing"},
		"prompt_get_error":    {"name": "broken"},
	} {
		got := payload(eventType)
		if _, timed := got["duration_ms"]; timed != !strings.HasSuffix(eventType, "_start") {
			t.Errorf("%s: duration_ms present = %v", eventType, timed)
		}
		delete(got, "duration_ms")
		if strings.HasSuffix(eventType, "_error") {
			for _, member := range []string{"error_type", "error_body"} {
				if text, _ := got[member].(string); text == "" {
					t.Errorf("%s: %s = %v, want text", eventType, member, got[member])
				}
				delete(got, member)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s payload %v, want %v", eventType, got, want)
		}
	}
	if raw, _ := json.Marshal(events); strings.Contains(string(raw), "secret") {
		t.Error("a prompt's messages or a resource's contents reached an event (SPEC §11.4.4)")
	}
	for _, e := range events {
		if e.EventType != "surface_snapshot" && (e.CallID != nil || e.SessionID != events[len(events)-1].SessionID) {
			t.Errorf("%s: call_id %v, session %s; want no call_id and one session", e.EventType, e.CallID, e.SessionID)
		}
	}
}

// SPEC §11.4.5: a listing Baton runs for itself, with no client request behind
// it, gets no events.
func TestBatonsOwnListingsAreNotRecorded(t *testing.T) {
	server, sink := installWithSink(t, Config{ReportMode: "on"}, addLookup)
	call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	if got := eventTypes(sink.all()); !slices.Equal(got, []string{"tool_call_start", "tool_call_end"}) {
		t.Errorf("events %v", got)
	}
}

// SPEC §11.4.4: under "off" these error legs keep their message and carry no
// result_capture, so the vendor is told once, when it configures the mode.
func TestUnderResultCaptureOffAFailedRequestKeepsItsMessage(t *testing.T) {
	for _, capture := range []string{"full", "off"} {
		logs := captureLogs(t)
		server, sink := installWithSink(t, Config{ResultCaptureMode: capture}, addPromptsAndResources)
		_, _ = connect(t, server).GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "broken"})
		got := asJSON(t, ofType(sink.all(), "prompt_get_error"))[0]["payload"].(map[string]any)
		if _, marked := got["result_capture"]; marked || got["error_body"] != "no such fleet" {
			t.Errorf("%s: payload %v", capture, got)
		}
		for _, leg := range []string{"tool_list_error", "prompt_list_error", "prompt_get_error", "resource_list_error", "resource_read_error"} {
			if warned := strings.Count(logs.String(), leg); warned != map[string]int{"full": 0, "off": 1}[capture] {
				t.Errorf("%s: %s warned %d times", capture, leg, warned)
			}
		}
	}
}

// SPEC §11.4.4: the resolver runs once per request and its answer rides the
// start and the terminal event.
func TestALifecycleRequestCarriesThePrincipal(t *testing.T) {
	calls := 0
	server, sink := installWithSink(t, Config{ResolvePrincipal: func(context.Context, Caller) (*Principal, error) {
		calls++
		return &Principal{ID: "u1"}, nil
	}}, addPromptsAndResources)
	if _, err := connect(t, server).GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "fleet"}); err != nil {
		t.Fatal(err)
	}
	events := asJSON(t, append(ofType(sink.all(), "prompt_get_start"), ofType(sink.all(), "prompt_get_end")...))
	for _, e := range events {
		if p, _ := e["principal"].(map[string]any); p["id"] != "u1" || e["client_observed"] == nil {
			t.Errorf("%s: principal %v, client_observed %v", e["event_type"], e["principal"], e["client_observed"])
		}
	}
	if calls != 1 || len(events) != 2 {
		t.Errorf("resolver ran %d times over %d events; want 1, 2", calls, len(events))
	}
}
