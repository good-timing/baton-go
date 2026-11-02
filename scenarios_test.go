package baton

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Helpers for the runners of baton-spec's collector/*.json scenarios.

type collectorSettings struct {
	RequestTimeoutMS int  `json:"request_timeout_ms"`
	ShutdownBudgetMS int  `json:"shutdown_budget_ms"`
	BackoffBaseMS    int  `json:"backoff_base_ms"`
	FlushIntervalMS  *int `json:"flush_interval_ms"`
}

// apply gives s these settings; one that is absent keeps the sink's default.
func (c collectorSettings) apply(s *httpSink) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	s.client.Timeout = ms(c.RequestTimeoutMS)
	s.budget = ms(c.ShutdownBudgetMS)
	s.backoff = ms(c.BackoffBaseMS)
	if c.FlushIntervalMS != nil {
		s.flushEvery = ms(*c.FlushIntervalMS)
	}
}

// readScenarios reads collector/name into into, and skips the test where
// python3, which the collector needs, is missing.
func readScenarios(t *testing.T, name string, into any) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is missing; the fake collector needs it")
	}
	raw, err := os.ReadFile(collectorDir + "/" + name)
	if err != nil {
		t.Fatalf("is the baton-spec submodule checked out? %v", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

type echoIn struct {
	Text string `json:"text"`
}

func echo(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoIn, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, in, nil
}

// installEcho adds the echo tool to server and installs Baton on it as vendor
// "acme", with an HTTP sink sending to the collector at url. set, when not
// nil, changes the sink's settings.
func installEcho(t *testing.T, server *mcp.Server, url string, set func(*httpSink)) (*Handle, *httpSink) {
	t.Helper()
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, echo)
	var sent *httpSink
	handle := installAs(t, server, Config{}, "acme", func() sink {
		sent = stopsWithTheTest(t, newHTTPSink(url, testKey))
		if set != nil {
			set(sent)
		}
		return sent
	})
	return handle, sent
}

func deref(s *string) string {
	if s == nil {
		return "<absent>"
	}
	return *s
}
