package baton

import (
	"context"
	"maps"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Every scenario of baton-spec's collector/sessions.json, through Install's
// path; what each checks is the file's "about".

type sessionScenario struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Clients   int    `json:"clients"`
	Calls     int    `json:"calls"`
	Expect    struct {
		Sessions  string `json:"sessions"`
		Origin    string `json:"origin"`
		Transport string `json:"transport"`
	} `json:"expect"`
}

func TestSessionScenarios(t *testing.T) {
	var spec struct{ Scenarios []sessionScenario }
	readScenarios(t, "sessions.json", &spec)
	for _, sc := range spec.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			captureLogs(t)
			url := startCollector(t, collectorScenario{Mode: "ok"})
			var opts *mcp.ServerOptions
			if sc.Transport == "stateless_http" {
				opts = &mcp.ServerOptions{GetSessionID: func() string { return "" }}
			}
			server, handle, _ := installEcho(t, mcp.NewServer(&mcp.Implementation{Name: "echo-server"}, opts), url, func(*httpSink) {})
			var clients []*mcp.ClientSession
			for range sc.Clients {
				switch sc.Transport {
				case "memory":
					clients = append(clients, connect(t, server))
				default:
					endpoint := serveHTTP(t, server, false, &mcp.StreamableHTTPOptions{Stateless: sc.Transport == "stateless_http"})
					clients = append(clients, connectOver(t, &mcp.StreamableClientTransport{Endpoint: endpoint}))
				}
			}
			for _, cs := range clients {
				for range sc.Calls {
					call(t, cs, "echo", map[string]any{"text": "hi"})
				}
			}
			handle.Close(context.Background())

			var events []collectedEvent
			for _, e := range receivedBy(t, url, "ok", "").Events {
				if e.EventType == "tool_call_start" || e.EventType == "tool_call_end" {
					events = append(events, e)
				}
			}
			if want := 2 * sc.Clients * sc.Calls; len(events) != want {
				t.Fatalf("%d tool events, want %d", len(events), want)
			}
			callSession := map[string]string{}
			for _, e := range events {
				if e.SessionIDOrigin == nil || *e.SessionIDOrigin != sc.Expect.Origin || e.TransportObserved == nil || *e.TransportObserved != sc.Expect.Transport {
					t.Errorf("%s: origin %v transport %v", e.EventType, deref(e.SessionIDOrigin), deref(e.TransportObserved))
				}
				if id, seen := callSession[e.CallID]; seen && id != e.SessionID {
					t.Errorf("call %s starts in %s and ends in %s", e.CallID, id, e.SessionID)
				}
				callSession[e.CallID] = e.SessionID
			}
			calls := map[string]int{}
			for _, id := range callSession {
				calls[id]++
			}
			switch sc.Expect.Sessions {
			case "one":
				for i, e := range events {
					if e.SessionID != events[0].SessionID {
						t.Errorf("event %d is in %s, not %s", i, e.SessionID, events[0].SessionID)
					}
					if i > 0 && e.SequenceNumber <= events[i-1].SequenceNumber {
						t.Errorf("event %d has sequence_number %d after %d", i, e.SequenceNumber, events[i-1].SequenceNumber)
					}
				}
			case "per_client":
				want := map[string]int{}
				for _, cs := range clients {
					want[cs.ID()] = sc.Calls
				}
				if len(want) != sc.Clients || !maps.Equal(calls, want) {
					t.Errorf("calls per session %v, want %v", calls, want)
				}
			case "per_call":
				if len(calls) != sc.Clients*sc.Calls {
					t.Errorf("calls per session %v, want one each", calls)
				}
			default:
				t.Fatalf("unknown sessions %q", sc.Expect.Sessions)
			}
		})
	}
}

func deref(s *string) string {
	if s == nil {
		return "<absent>"
	}
	return *s
}
