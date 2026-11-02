package baton

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Every scenario of baton-spec's collector/shutdown.json, through Install's
// path; what each checks is the file's "about".

type shutdownScenario struct {
	Name     string                       `json:"name"`
	Mode     string                       `json:"mode"`
	DelayMS  int                          `json:"delay_ms"`
	Settings json.RawMessage              `json:"settings"`
	Steps    []map[string]json.RawMessage `json:"steps"`
	Expect   struct {
		Dropped            map[dropReason]int `json:"dropped"`
		WarningsAfterClose *int               `json:"warnings_after_close"`
	} `json:"expect"`
}

type shutdownSpec struct {
	Settings collectorSettings `json:"settings"`
	Limits   struct {
		CallMS        int `json:"call_ms"`
		CloseMarginMS int `json:"close_margin_ms"`
	} `json:"limits"`
	Scenarios []shutdownScenario `json:"scenarios"`
}

var shutdownNotRun = map[string]string{
	"flush_waits_for_what_is_queued": "Handle has no Flush; Close is the only way to send what is queued",
}

// warnings counts slog records at Warn or above.
func warnings(logs *testLog) int {
	s := logs.String()
	return strings.Count(s, "level=WARN") + strings.Count(s, "level=ERROR")
}

func TestShutdownScenarios(t *testing.T) {
	var spec shutdownSpec
	readScenarios(t, "shutdown.json", &spec)
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	for _, sc := range spec.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			if why, ok := shutdownNotRun[sc.Name]; ok {
				t.Skip(why)
			}
			settings := spec.Settings
			if sc.Settings != nil {
				if err := json.Unmarshal(sc.Settings, &settings); err != nil {
					t.Fatal(err)
				}
			}
			closeLimit := ms(settings.ShutdownBudgetMS + spec.Limits.CloseMarginMS)
			logs := captureLogs(t)
			url := startCollector(t, collectorScenario{Mode: sc.Mode, DelayMS: sc.DelayMS})
			server := mcp.NewServer(&mcp.Implementation{Name: "echo-server"}, nil)
			handle, sent := installEcho(t, server, url, func(s *httpSink) {
				s.client.Timeout = ms(settings.RequestTimeoutMS)
				s.budget = ms(settings.ShutdownBudgetMS)
				s.backoff = ms(settings.BackoffBaseMS)
			})
			cs := connect(t, server)

			// Closes run off the test goroutine, so one that never returns
			// fails the scenario here instead of hanging it.
			returned := make(chan struct{}, 2)
			running := 0
			startCloses := func(n int) {
				gate := make(chan struct{})
				for range n {
					go func() {
						<-gate
						handle.Close(context.Background())
						returned <- struct{}{}
					}()
				}
				close(gate)
				running += n
			}
			awaitClose := func() {
				select {
				case <-returned:
					running--
				case <-time.After(closeLimit):
					t.Fatalf("close did not return within %v", closeLimit)
				}
			}
			warningsBefore := -1
			for _, step := range sc.Steps {
				if len(step) != 1 {
					t.Fatalf("a step names one action: %v", step)
				}
				for action, arg := range step {
					switch action {
					case "call":
						var calls int
						if err := json.Unmarshal(arg, &calls); err != nil {
							t.Fatal(err)
						}
						for n := range calls {
							started := time.Now()
							res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "hi"}})
							if took := time.Since(started); took > ms(spec.Limits.CallMS) {
								t.Errorf("call %d took %v", n, took)
							}
							if err != nil || res.IsError || len(res.Content) == 0 || res.Content[0].(*mcp.TextContent).Text != "hi" {
								t.Errorf("call %d: %v %+v", n, err, res)
							}
						}
					case "close":
						startCloses(1)
						awaitClose()
					case "close_twice":
						// The next step runs once either returns: a close that
						// returns early fails the check whichever started first.
						startCloses(2)
						awaitClose()
					case "check":
						var want struct {
							ToolEvents int `json:"tool_events"`
						}
						if err := json.Unmarshal(arg, &want); err != nil {
							t.Fatal(err)
						}
						got := 0
						for _, e := range receivedBy(t, url, sc.Mode, "").Events {
							if e.EventType == "tool_call_start" || e.EventType == "tool_call_end" {
								got++
							}
						}
						if got != want.ToolEvents {
							t.Errorf("the collector has %d tool events, want %d", got, want.ToolEvents)
						}
					default:
						t.Fatalf("unknown step %q", action)
					}
					if (action == "close" || action == "close_twice") && warningsBefore < 0 {
						warningsBefore = warnings(logs)
					}
				}
			}
			for running > 0 {
				awaitClose()
			}
			select { // an event still being sent is counted when its send ends
			case <-sent.done:
			case <-time.After(5 * time.Second):
				t.Fatal("the sink kept sending 5s after close")
			}

			counted := processDrops.totals()
			for _, reason := range slices.Concat(slices.Collect(maps.Keys(counted)), slices.Collect(maps.Keys(sc.Expect.Dropped))) {
				want, named := sc.Expect.Dropped[reason]
				if n := counted[reason]; n < want || (!named && n > 0) {
					t.Errorf("counted %v, want at least %v and nothing else", counted, sc.Expect.Dropped)
					break
				}
			}
			if strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("logs: %s", logs)
			}
			if want := sc.Expect.WarningsAfterClose; want != nil {
				if got := warnings(logs) - warningsBefore; got != *want {
					t.Errorf("%d warnings after close, want %d: %s", got, *want, logs)
				}
			}
		})
	}
}
