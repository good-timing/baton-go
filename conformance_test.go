package baton

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The scenarios mirror baton-spec/scripts/generate.py: same vendor id,
// consent token, tools, arguments and call order. Change both together.

type lookupIn struct {
	Name string `json:"name"`
}

type lookupOut struct {
	Found bool   `json:"found"`
	Name  string `json:"name"`
}

func lookup(_ context.Context, _ *mcp.CallToolRequest, in lookupIn) (*mcp.CallToolResult, lookupOut, error) {
	return nil, lookupOut{Found: true, Name: in.Name}, nil
}

var testImpl = &mcp.Implementation{Name: "spec-vector-generator"}

func newVectorServer() *mcp.Server {
	return mcp.NewServer(testImpl, nil)
}

func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	return connectWith(t, server, nil)
}

func connectWith(t *testing.T, server *mcp.Server, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientSide, serverSide := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverSide, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, opts)
	cs, err := client.Connect(ctx, clientSide, clientOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func mainScenario(t *testing.T) []Event {
	server := newVectorServer()
	mcp.AddTool(server, &mcp.Tool{Name: "lookup"}, lookup)
	mcp.AddTool(server, &mcp.Tool{Name: "boom"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return nil, nil, errors.New("simulated failure")
	})
	mcp.AddTool(server, &mcp.Tool{Name: "soft_fail"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "simulated returned failure"}}}, nil, nil
	})
	mem := &memorySink{}
	handle := installAs(t, server, Config{
		VendorDisplayName: "Spec Vector Generator",
		ConsentToken:      "ct_spec_vectors",
		ReportMode:        "on",
	}, "spec-vectors", func() sink { return mem })
	cs := connect(t, server)
	call(t, cs, "lookup", map[string]any{"name": "alice"})
	call(t, cs, "boom", map[string]any{})
	call(t, cs, "soft_fail", map[string]any{})
	call(t, cs, handle.b.annotateName, map[string]any{
		"user_goal":     "look something up",
		"what_happened": "asked for a match; the tool returned an error with no reason",
		"tool_name":     "soft_fail",
	})
	return mem.all()
}

func identifiedScenario(t *testing.T) []Event {
	server := newVectorServer()
	mcp.AddTool(server, &mcp.Tool{Name: "lookup"}, lookup)
	mem := &memorySink{}
	installAs(t, server, Config{
		ConsentToken: "ct_spec_vectors",
		ResolvePrincipal: func(context.Context, Caller) (*Principal, error) {
			return &Principal{ID: "user-123", DisplayName: "Jane", Organization: &Organization{ID: "org-456", DisplayName: "Acme"}}, nil
		},
	}, "spec-vectors", func() sink { return mem })
	cs := connect(t, server)
	call(t, cs, "lookup", map[string]any{"name": "alice", "user_goal": "1: find alice; success is a match"})
	return mem.all()
}

func toolEvent(eventType, tool string) func(map[string]any) bool {
	return func(e map[string]any) bool {
		p, _ := e["payload"].(map[string]any)
		return e["event_type"] == eventType && p["tool_name"] == tool
	}
}

type vectorCase struct {
	name     string
	scenario string
	pick     func(map[string]any) bool
}

var vectorCases = []vectorCase{
	{"surface_snapshot", "main", func(e map[string]any) bool { return e["event_type"] == "surface_snapshot" }},
	{"tool_call_start", "main", toolEvent("tool_call_start", "lookup")},
	{"tool_call_end", "main", toolEvent("tool_call_end", "lookup")},
	{"tool_call_error", "main", toolEvent("tool_call_error", "boom")},
	{"tool_call_error.returned", "main", toolEvent("tool_call_error", "soft_fail")},
	{"annotation.report", "main", func(e map[string]any) bool { return e["event_type"] == "annotation" }},
	{"tool_call_start.identified", "identified", toolEvent("tool_call_start", "lookup")},
}

// annotation.json is a note, which current producers no longer send.
var notEmittedHere = []string{"annotation"}

// clientOptions asks for the protocol in BATON_TEST_MCP_PROTOCOL, so CI can run
// the newest go-sdk against clients of both protocols. The field is exported
// only from go-sdk v1.8, so it is set by name.
func clientOptions(t *testing.T) *mcp.ClientSessionOptions {
	t.Helper()
	version := os.Getenv("BATON_TEST_MCP_PROTOCOL")
	if version == "" {
		return nil
	}
	opts := &mcp.ClientSessionOptions{}
	f := structField(opts, "ProtocolVersion")
	if !f.CanSet() {
		t.Fatalf("this go-sdk cannot ask for protocol %s", version)
	}
	f.SetString(version)
	return opts
}

// goSDKv17 is go-sdk v1.7 or later, told by a field it added: a test binary
// carries no module versions.
func goSDKv17() bool {
	return structField(&mcp.CallToolResult{}, "InputRequests").IsValid()
}

// newProtocol is MCP 2026-07-28 or later, which has no handshake and no
// Mcp-Session-Id (SEP-2567), and puts the client's info in each request's _meta.
func newProtocol(cs *mcp.ClientSession) bool {
	return cs.InitializeResult().ProtocolVersion >= "2026-07-28"
}

// vendorMeta is runtime_meta without the keys a 2026-07-28 client writes on
// every request, for tests about the vendor's own keys. They are forwarded
// with the rest (TestProtocolMetaIsForwarded).
func vendorMeta(meta map[string]any) any {
	out := map[string]any{}
	for k, v := range meta {
		if !strings.HasPrefix(k, "io.modelcontextprotocol/") {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SPEC §11.4.6.
var exemptEnvelope = []string{"event_id", "session_id", "captured_at", "call_id", "sdk_version", "agent_runtime", "client_observed", "transport_observed"}

func exemptPayload(name string) []string {
	switch name {
	case "tool_call_end", "tool_call_error.returned":
		return []string{"duration_ms", "result"}
	case "tool_call_error":
		return []string{"duration_ms", "error_type"}
	case "surface_snapshot":
		return []string{"surface_hash", "server_info", "capabilities", "tools"}
	}
	return []string{"duration_ms"}
}

func asJSON(t *testing.T, events []Event) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// dropNulls makes absent and null equal (SPEC §11.4).
func dropNulls(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, item := range x {
			if item != nil {
				out[k] = dropNulls(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = dropNulls(item)
		}
		return out
	}
	return v
}

func TestVectorsAllCovered(t *testing.T) {
	files, err := filepath.Glob("baton-spec/vectors/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no vectors found (is the baton-spec submodule checked out?): %v", err)
	}
	covered := slices.Clone(notEmittedHere)
	for _, c := range vectorCases {
		covered = append(covered, c.name)
	}
	var onDisk []string
	for _, f := range files {
		onDisk = append(onDisk, strings.TrimSuffix(filepath.Base(f), ".json"))
	}
	slices.Sort(covered)
	slices.Sort(onDisk)
	if !slices.Equal(covered, onDisk) {
		t.Fatalf("vector files %v, cases %v", onDisk, covered)
	}
}

func TestEventsMatchVectors(t *testing.T) {
	scenarios := map[string][]map[string]any{
		"main":       asJSON(t, mainScenario(t)),
		"identified": asJSON(t, identifiedScenario(t)),
	}
	for _, c := range vectorCases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("baton-spec", "vectors", c.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			for _, e := range scenarios[c.scenario] {
				if c.pick(e) {
					if got != nil {
						t.Fatalf("more than one event matches")
					}
					got = e
				}
			}
			if got == nil {
				t.Fatalf("no event matches")
			}
			if got["sdk_version"] != sdkVersion {
				t.Errorf("sdk_version = %v, want %s", got["sdk_version"], sdkVersion)
			}
			if meta, ok := got["runtime_meta"].(map[string]any); ok {
				got["runtime_meta"] = vendorMeta(meta)
			}
			for _, side := range []map[string]any{want, got} {
				for _, k := range exemptEnvelope {
					delete(side, k)
				}
				if p, ok := side["payload"].(map[string]any); ok {
					for _, k := range exemptPayload(c.name) {
						delete(p, k)
					}
				}
			}
			if !reflect.DeepEqual(dropNulls(want), dropNulls(got)) {
				w, _ := json.MarshalIndent(dropNulls(want), "", "  ")
				g, _ := json.MarshalIndent(dropNulls(got), "", "  ")
				t.Errorf("event differs from vector\nwant %s\ngot  %s", w, g)
			}
		})
	}
}

func TestCallIDPairsStartWithOutcome(t *testing.T) {
	events := mainScenario(t)
	ids := map[string][]string{}
	for _, e := range events {
		if e.CallID != nil {
			ids[*e.CallID] = append(ids[*e.CallID], e.EventType)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("want 3 calls, got %v", ids)
	}
	for id, types := range ids {
		if len(types) != 2 || types[0] != "tool_call_start" {
			t.Errorf("call %s has events %v", id, types)
		}
	}
}
