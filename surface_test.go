package baton

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// methodsSeen records every method that reaches server's middleware from now on.
func methodsSeen(server *mcp.Server) func() []string {
	var (
		mu   sync.Mutex
		seen []string
	)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			mu.Lock()
			seen = append(seen, method)
			mu.Unlock()
			return next(ctx, method, req)
		}
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

func TestBatonNeverCallsTheVendorsServer(t *testing.T) {
	t.Setenv("BATON_DSN", "")
	installed := newVectorServer()
	addLookup(installed)
	seen := methodsSeen(installed)
	handle, err := Install(installed, testImpl, nil, Config{VendorID: "acme", Sink: &recordingSink{}})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close(context.Background())
	if got := seen(); len(got) != 0 {
		t.Errorf("Install sent the server %v", got)
	}

	b, _ := newTestBaton(t, Config{})
	wrappedServer := newVectorServer()
	addLookup(wrappedServer)
	seen = methodsSeen(wrappedServer)
	if err := b.Wrap(wrappedServer); err != nil {
		t.Fatal(err)
	}
	if got := seen(); len(got) != 0 {
		t.Errorf("Wrap sent the server %v", got)
	}
}

func TestAServerNeedsANamedImpl(t *testing.T) {
	t.Setenv("BATON_DSN", "")
	cfg := Config{VendorID: "acme", Sink: &recordingSink{}, Middleware: []mcp.Middleware{stopsAll}}
	for name, impl := range map[string]*mcp.Implementation{"nil": nil, "no name": {Version: "1"}} {
		t.Run(name, func(t *testing.T) {
			b, err := New(impl, nil, cfg)
			if err == nil || !strings.Contains(err.Error(), "*mcp.Implementation") {
				t.Errorf("New returned %v, want the impl error", err)
			}
			wrappedServer := newVectorServer()
			addLookup(wrappedServer)
			if err := b.Wrap(wrappedServer); err != nil {
				t.Fatal(err)
			}
			if _, err := connect(t, wrappedServer).CallTool(context.Background(), &mcp.CallToolParams{Name: "lookup", Arguments: map[string]any{"name": "a"}}); err == nil {
				t.Error("New's inert Baton did not add the vendor's middleware")
			}

			installed := newVectorServer()
			addLookup(installed)
			if _, err := Install(installed, impl, nil, cfg); err == nil || !strings.Contains(err.Error(), "*mcp.Implementation") {
				t.Errorf("Install returned %v, want the impl error", err)
			}
			if _, err := connect(t, installed).CallTool(context.Background(), &mcp.CallToolParams{Name: "lookup", Arguments: map[string]any{"name": "a"}}); err == nil {
				t.Error("Install did not add the vendor's middleware")
			}
		})
	}
	t.Run("BATON_DISABLED", func(t *testing.T) {
		t.Setenv("BATON_DISABLED", "1")
		if _, err := New(nil, nil, cfg); err != nil {
			t.Errorf("New checked the impl: %v", err)
		}
		if _, err := Install(newVectorServer(), nil, nil, cfg); err != nil {
			t.Errorf("Install checked the impl: %v", err)
		}
	})
}

// A 2026-07-28 client sends no initialize, and go-sdk adds serverInfo to an
// answer only after every middleware, so both come from what the vendor passed.
func TestServerInfoAndInstructionsComeFromImplAndOpts(t *testing.T) {
	impl := &mcp.Implementation{Name: "weather", Title: "Weather", Version: "1.2.0", WebsiteURL: "https://example.com",
		Icons: []mcp.Icon{{Source: "https://example.com/i.png", MIMEType: "image/png", Sizes: []string{"48x48"}}}}
	opts := &mcp.ServerOptions{Instructions: "Vendor text."}
	server := mcp.NewServer(impl, opts)
	addLookup(server)
	sink := installBuiltWith(t, server, impl, opts, Config{})
	cs := connect(t, server)
	call(t, cs, "lookup", map[string]any{"name": "a"})
	snaps := ofType(sink.all(), "surface_snapshot")
	if len(snaps) != 1 {
		t.Fatalf("%d snapshots, want 1", len(snaps))
	}
	snap := snaps[0].Payload.(surfaceSnapshot)
	if snap.Instructions == nil || *snap.Instructions != "Vendor text." {
		t.Errorf("instructions = %v, want the vendor's own", snap.Instructions)
	}
	if snap.Capabilities == nil {
		t.Error("no capabilities, though the client's setup call was seen")
	}
	// server_info must be byte for byte what the server itself sends, or the
	// same server gets a new surface_hash.
	plain := mcp.NewServer(impl, opts)
	addLookup(plain)
	resp := newRawSession(t, plain).request("initialize", initializeParams("c"))
	var answer mcp.InitializeResult
	if err := json.Unmarshal(resp.Result, &answer); err != nil {
		t.Fatal(err)
	}
	if want := jsonValue(answer.ServerInfo); !reflect.DeepEqual(snap.ServerInfo, want) {
		t.Errorf("server_info = %v, want %v as the server sends it", snap.ServerInfo, want)
	}
	if want := jsonValue(answer.Capabilities); !reflect.DeepEqual(snap.Capabilities, want) {
		t.Errorf("capabilities = %v, want %v as the server sends them", snap.Capabilities, want)
	}
}

// In stateless HTTP a tools/call can reach a server that saw no setup call.
func TestCapabilitiesFollowInASecondSnapshot(t *testing.T) {
	b, sink := newTestBaton(t, Config{})
	server := wrapped(t, b, addLookup)
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(ts.Close)
	req, err := http.NewRequest(http.MethodPost, ts.URL,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"lookup","arguments":{"name":"a"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/call with no setup call: %s", resp.Status)
	}
	call(t, connectOver(t, &mcp.StreamableClientTransport{Endpoint: ts.URL}), "lookup", map[string]any{"name": "a"})
	snaps := ofType(sink.all(), "surface_snapshot")
	if len(snaps) != 2 {
		t.Fatalf("%d snapshots, want 2", len(snaps))
	}
	first, second := snaps[0].Payload.(surfaceSnapshot), snaps[1].Payload.(surfaceSnapshot)
	if first.Capabilities != nil || second.Capabilities == nil {
		t.Errorf("capabilities %v then %v, want none then the server's", first.Capabilities, second.Capabilities)
	}
	if first.ServerInfo == nil || !reflect.DeepEqual(first.ServerInfo, second.ServerInfo) {
		t.Errorf("server_info %v then %v, want the impl in both", first.ServerInfo, second.ServerInfo)
	}
}

func TestANameThatDiffersFromTheServersIsWarnedOnce(t *testing.T) {
	logs := captureLogs(t)
	server := mcp.NewServer(&mcp.Implementation{Name: "real"}, nil)
	addLookup(server)
	installBuiltWith(t, server, &mcp.Implementation{Name: "given"}, nil, Config{})
	for range 2 {
		newRawSession(t, server).request("initialize", initializeParams("c"))
	}
	if n := strings.Count(logs.String(), "different name"); n != 1 {
		t.Errorf("%d warnings, want 1:\n%s", n, logs)
	}
}

// heldSink holds the first event written until release is closed.
type heldSink struct {
	memorySink
	writing chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *heldSink) write(ev Event) {
	s.once.Do(func() {
		close(s.writing)
		<-s.release
	})
	s.memorySink.write(ev)
}

func TestASnapshotWithoutCapabilitiesIsNeverSentAfterOneWithThem(t *testing.T) {
	b, _ := newTestBaton(t, Config{})
	held := &heldSink{writing: make(chan struct{}), release: make(chan struct{})}
	b.sink = held
	s, err := b.newServer()
	if err != nil {
		t.Fatal(err)
	}
	go s.maybeSnapshot(listing{{Name: "lookup", InputSchema: map[string]any{"type": "object"}}})
	<-held.writing
	stored := make(chan struct{})
	go func() {
		b.keepSetupAnswer(&mcp.InitializeResult{Capabilities: &mcp.ServerCapabilities{Logging: &mcp.LoggingCapabilities{}}})
		close(stored)
	}()
	select {
	case <-stored:
		t.Error("capabilities arrived while a snapshot without them was still being sent")
	case <-time.After(100 * time.Millisecond):
	}
	close(held.release)
	<-stored
}
