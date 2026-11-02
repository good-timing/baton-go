package baton

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A paused call (go-sdk v1.7+) is built by name, so this file compiles on the floor.

// pausingTool asks for the client's input until its answers arrive, or once
// with no questions when empty is set.
func pausingTool(t *testing.T, empty bool, rounds *int, leaked *bool) func(*mcp.Server) {
	if !structField(&mcp.CallToolResult{}, "InputRequests").IsValid() {
		t.Skip("this go-sdk has no paused calls")
	}
	return func(s *mcp.Server) {
		s.AddTool(&mcp.Tool{Name: "deploy", InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				*rounds++
				var args map[string]any
				_ = json.Unmarshal(req.Params.Arguments, &args)
				*leaked = *leaked || args["user_goal"] != nil
				if resumesAPause(req.Params) || (empty && *rounds > 1) {
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "deployed"}}}, nil
				}
				res := &mcp.CallToolResult{}
				requests := structField(res, "InputRequests")
				requests.Set(reflect.MakeMap(requests.Type()))
				if !empty {
					requests.SetMapIndex(reflect.ValueOf("confirm"), reflect.ValueOf(&mcp.ElicitParams{Message: "Deploy?"}))
				}
				return res, nil
			})
	}
}

func callDeploy(t *testing.T, server *mcp.Server) (*mcp.CallToolResult, error) {
	t.Helper()
	cs := connectWith(t, server, &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}, nil
		},
	})
	return cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "deploy", Arguments: map[string]any{"user_goal": "1: ship it"}})
}

func TestAPausedCallIsOneCall(t *testing.T) {
	rounds, leaked := 0, false
	server, sink := installWithSink(t, Config{}, pausingTool(t, false, &rounds, &leaked))
	if res, err := callDeploy(t, server); err != nil || len(res.Content) != 1 {
		t.Fatalf("result %v, err %v", res, err)
	}
	starts, ends := ofType(sink.all(), "tool_call_start"), ofType(sink.all(), "tool_call_end")
	if rounds != 2 || len(starts) != 1 || len(ends) != 1 {
		t.Fatalf("%d rounds, %d starts, %d ends; want 2, 1, 1", rounds, len(starts), len(ends))
	}
	if leaked {
		t.Error("user_goal reached the tool on a round")
	}
	if ends[0].SessionID != starts[0].SessionID {
		t.Errorf("start in session %q, end in %q", starts[0].SessionID, ends[0].SessionID)
	}
}

// A pause with no questions reaches a 2026-07-28 client as one, and its retry
// carries no answers, so each round is a whole call. go-sdk answers an older
// client "the server is busy".
func TestAnEmptyPauseIsAnEnd(t *testing.T) {
	rounds, leaked := 0, false
	server, sink := installWithSink(t, Config{}, pausingTool(t, true, &rounds, &leaked))
	_, _ = callDeploy(t, server)
	starts := ofType(sink.all(), "tool_call_start")
	ends := append(ofType(sink.all(), "tool_call_end"), ofType(sink.all(), "tool_call_error")...)
	if len(starts) == 0 || len(starts) != len(ends) {
		t.Errorf("%d rounds, %d starts, %d ends; want one end per start", rounds, len(starts), len(ends))
	}
}

// go-sdk's fallback for older clients runs inside Baton, so a pause it cannot
// complete reaches Baton as the call's error.
func TestAPauseTheSDKCannotCompleteIsAnError(t *testing.T) {
	rounds, leaked := 0, false
	server, sink := installWithSink(t, Config{}, pausingTool(t, false, &rounds, &leaked))
	cs := connectWith(t, server, nil)
	if newProtocol(cs) {
		t.Skip("a 2026-07-28 client that cannot answer stops, and the server never hears back")
	}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "deploy", Arguments: map[string]any{}}); err == nil {
		t.Fatal("a client that cannot elicit answered the pause")
	}
	starts, failures := ofType(sink.all(), "tool_call_start"), ofType(sink.all(), "tool_call_error")
	if len(starts) != 1 || len(failures) != 1 || len(ofType(sink.all(), "tool_call_end")) != 0 {
		t.Errorf("%d starts, %d errors; want 1, 1", len(starts), len(failures))
	}
}
