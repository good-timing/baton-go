package baton

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Every scenario of baton-spec's collector/intent.json, through Install's
// path; what each checks is the file's "about".

type intentScenario struct {
	Name      string          `json:"name"`
	Arguments map[string]any  `json:"arguments"`
	Expect    json.RawMessage `json:"expect"`
}

// intentMembers are the tool_call_start payload members intent.json pins.
type intentMembers struct {
	Params       map[string]any `json:"params"`
	CallIntent   *string        `json:"call_intent"`
	CallExpected *string        `json:"call_expected"`
	CallWorkflow *string        `json:"call_workflow"`
	IntentSource *string        `json:"intent_source"`
}

func TestIntentScenarios(t *testing.T) {
	var spec struct{ Scenarios []intentScenario }
	readScenarios(t, "intent.json", &spec)
	for _, sc := range spec.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			captureLogs(t)
			url := startCollector(t, collectorScenario{Mode: "ok"})
			server, handle, _ := installEcho(t, mcp.NewServer(&mcp.Implementation{Name: "echo-server"}, nil), url, func(*httpSink) {})
			cs := connect(t, server)

			tools, err := cs.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(tools.Tools, func(tool *mcp.Tool) bool { return tool.Name == "echo" })
			if i < 0 {
				t.Fatalf("no echo in %v", tools.Tools)
			}
			var schema struct {
				Properties map[string]struct{ Type string } `json:"properties"`
				Required   []string                         `json:"required"`
			}
			raw, _ := json.Marshal(tools.Tools[i].InputSchema)
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			if schema.Properties["user_goal"].Type != "string" || !slices.Contains(schema.Required, "user_goal") {
				t.Errorf("echo's input schema: %s", raw)
			}

			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo", Arguments: sc.Arguments})
			if err != nil || res.IsError || len(res.Content) == 0 || res.Content[0].(*mcp.TextContent).Text != sc.Arguments["text"] {
				t.Errorf("call: %v %+v", err, res)
			}
			handle.Close(context.Background())

			var starts []intentMembers
			for _, e := range receivedBy(t, url, "ok", "").Events {
				if e.EventType == "tool_call_start" {
					var p intentMembers
					if err := json.Unmarshal(e.Payload, &p); err != nil {
						t.Fatal(err)
					}
					starts = append(starts, p)
				}
			}
			var want intentMembers
			if err := json.Unmarshal(sc.Expect, &want); err != nil {
				t.Fatal(err)
			}
			if len(starts) != 1 || !reflect.DeepEqual(starts[0], want) {
				got, _ := json.Marshal(starts)
				t.Errorf("tool_call_start payloads %s, want %s", got, sc.Expect)
			}
		})
	}
}
