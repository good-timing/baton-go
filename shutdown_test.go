package baton

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Every scenario of baton-spec's collector/shutdown.json, through Install's
// path; what each checks is the file's "about".

type shutdownStep struct {
	Call       int `json:"call"`
	Flush      *struct{}
	Close      *struct{}
	CloseTwice *struct{} `json:"close_twice"`
	Check      *struct {
		ToolEvents int `json:"tool_events"`
	}
}

type shutdownScenario struct {
	Name    string         `json:"name"`
	Mode    string         `json:"mode"`
	DelayMS int            `json:"delay_ms"`
	Steps   []shutdownStep `json:"steps"`
	Expect  struct {
		Dropped            map[dropReason]int `json:"dropped"`
		WarningsAfterClose *int               `json:"warnings_after_close"`
	} `json:"expect"`
}

type shutdownSpec struct {
	Settings struct {
		RequestTimeoutMS int `json:"request_timeout_ms"`
		ShutdownBudgetMS int `json:"shutdown_budget_ms"`
		BackoffBaseMS    int `json:"backoff_base_ms"`
	} `json:"settings"`
	Limits struct {
		CallMS        int `json:"call_ms"`
		CloseMarginMS int `json:"close_margin_ms"`
	} `json:"limits"`
	Scenarios []shutdownScenario `json:"scenarios"`
}

var shutdownNotRun = map[string]string{
	"flush_waits_for_what_is_queued": "Handle has no Flush; Close is the only way to send what is queued",
}

type echoIn struct {
	Text string `json:"text"`
}

func echo(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoIn, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, in, nil
}

// lockedLog can be read while the sink's goroutine logs.
type lockedLog struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// installEcho adds the echo tool to server and installs Baton on it with
// default settings, sending to the collector at url.
func installEcho(t *testing.T, server *mcp.Server, url string, set func(*httpSink)) (*mcp.Server, *Handle, *httpSink) {
	t.Helper()
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, echo)
	b, err := configure(Config{})
	if err != nil {
		t.Fatal(err)
	}
	b.vendorID, b.tenantID = "acme", "acme"
	var sent *httpSink
	handle, err := b.install(server, func() sink {
		sent = newHTTPSink(url, testKey)
		set(sent)
		return sent
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, handle, sent
}

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

func TestShutdownScenarios(t *testing.T) {
	var spec shutdownSpec
	readScenarios(t, "shutdown.json", &spec)
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	closeLimit := ms(spec.Settings.ShutdownBudgetMS + spec.Limits.CloseMarginMS)
	for _, sc := range spec.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			if why, ok := shutdownNotRun[sc.Name]; ok {
				t.Skip(why)
			}
			captureLogs(t)
			logs := &lockedLog{}
			slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
			url := startCollector(t, collectorScenario{Mode: sc.Mode, DelayMS: sc.DelayMS})
			server, handle, sent := installEcho(t, mcp.NewServer(&mcp.Implementation{Name: "echo-server"}, nil), url, func(s *httpSink) {
				s.client.Timeout = ms(spec.Settings.RequestTimeoutMS)
				s.budget = ms(spec.Settings.ShutdownBudgetMS)
				s.backoff = ms(spec.Settings.BackoffBaseMS)
			})
			cs := connect(t, server)
			closeWithin := func() {
				started := time.Now()
				handle.Close(context.Background())
				if took := time.Since(started); took > closeLimit {
					t.Errorf("close took %v", took)
				}
			}
			warningsBefore := -1
			for _, step := range sc.Steps {
				for n := range step.Call {
					started := time.Now()
					res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "hi"}})
					if took := time.Since(started); took > ms(spec.Limits.CallMS) {
						t.Errorf("call %d took %v", n, took)
					}
					if err != nil || res.IsError || len(res.Content) == 0 || res.Content[0].(*mcp.TextContent).Text != "hi" {
						t.Errorf("call %d: %v %+v", n, err, res)
					}
				}
				switch {
				case step.Close != nil:
					closeWithin()
				case step.CloseTwice != nil:
					var first sync.WaitGroup
					first.Go(closeWithin)
					time.Sleep(10 * time.Millisecond)
					closeWithin()
					defer first.Wait()
				case step.Check != nil:
					got := 0
					for _, e := range receivedBy(t, url, sc.Mode, "").Events {
						if e.EventType == "tool_call_start" || e.EventType == "tool_call_end" {
							got++
						}
					}
					if got != step.Check.ToolEvents {
						t.Errorf("the collector has %d tool events, want %d", got, step.Check.ToolEvents)
					}
				}
				if (step.Close != nil || step.CloseTwice != nil) && warningsBefore < 0 {
					warningsBefore = strings.Count(logs.String(), "level=WARN")
				}
			}
			<-sent.done

			counted := processDrops.totals()
			for reason, n := range counted {
				if want, named := sc.Expect.Dropped[reason]; !named || n < want {
					t.Errorf("counted %v, want at least %v and nothing else", counted, sc.Expect.Dropped)
					break
				}
			}
			for reason, want := range sc.Expect.Dropped {
				if counted[reason] < want {
					t.Errorf("counted %v, want at least %v", counted, sc.Expect.Dropped)
				}
			}
			if strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("logs: %s", logs)
			}
			if want := sc.Expect.WarningsAfterClose; want != nil {
				if got := strings.Count(logs.String(), "level=WARN") - warningsBefore; got != *want {
					t.Errorf("%d warnings after close, want %d: %s", got, *want, logs)
				}
			}
		})
	}
}
