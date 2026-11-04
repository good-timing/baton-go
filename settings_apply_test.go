package baton

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// holding is a Baton for vendor "acme" that already holds the settings
// document body, as if read from the Console, with a memory sink.
func holding(t *testing.T, cfg Config, body string) (*Baton, *memorySink) {
	t.Helper()
	b, sink := newTestBaton(t, cfg)
	b.settings = &settingsReader{}
	if body != "" {
		b.settings.doc.Store(parsed(t, []byte(body)))
	}
	return b, sink
}

const betaFullOthersOff = `{"version": 7, "audiences": [
	{"id": "beta", "match": {"label": ["beta"]}, "capture": {"results": "full"}},
	{"id": "everyone", "match": {}, "capture": {"results": "off"}}]}`

func labelled(labels ...string) ResolvePrincipal {
	return func(context.Context, Caller) (*Principal, error) {
		return &Principal{ID: "u_1", Labels: labels}, nil
	}
}

func TestEventsCarryTheCallersAudienceAndItsResultsSetting(t *testing.T) {
	for _, tc := range []struct {
		labels   []string
		audience string
		withheld bool
	}{{[]string{"beta"}, "beta", false}, {[]string{"gamma"}, "everyone", true}} {
		b, sink := holding(t, Config{ResolvePrincipal: labelled(tc.labels...)}, betaFullOthersOff)
		call(t, connect(t, wrapped(t, b, addLookup)), "lookup", map[string]any{"name": "a"})
		for _, ev := range append(ofType(sink.all(), "tool_call_start"), ofType(sink.all(), "tool_call_end")...) {
			if ev.Capture == nil || *ev.Capture != (CaptureObserved{SettingsVersion: 7, Audience: tc.audience}) {
				t.Errorf("%v: %s capture %+v, want audience %s", tc.labels, ev.EventType, ev.Capture, tc.audience)
			}
			if !slices.Equal(ev.Labels, tc.labels) {
				t.Errorf("%v: %s labels %q", tc.labels, ev.EventType, ev.Labels)
			}
		}
		end := ofType(sink.all(), "tool_call_end")[0].Payload.(toolCallEnd)
		if got := end.ResultCapture != nil; got != tc.withheld {
			t.Errorf("%v: result withheld %v, want %v", tc.labels, got, tc.withheld)
		}
	}
}

func TestAClientRuleMatchesTheDeclaredClientName(t *testing.T) {
	b, sink := holding(t, Config{}, `{"version": 2, "audiences": [
		{"id": "ours", "match": {"client": ["test-client"]}, "capture": {}},
		{"id": "everyone", "match": {}, "capture": {}}]}`)
	call(t, connect(t, wrapped(t, b, addLookup)), "lookup", map[string]any{"name": "a"})
	if c := ofType(sink.all(), "tool_call_start")[0].Capture; c == nil || c.Audience != "ours" {
		t.Errorf("capture %+v", c)
	}
}

func TestCodeSettingsSendLabelsAndNoCapture(t *testing.T) {
	b, sink := holding(t, Config{ResolvePrincipal: labelled("beta")}, "")
	call(t, connect(t, wrapped(t, b, addLookup)), "lookup", map[string]any{"name": "a"})
	start := ofType(sink.all(), "tool_call_start")[0]
	if start.Capture != nil || !slices.Equal(start.Labels, []string{"beta"}) {
		t.Errorf("capture %+v, labels %q", start.Capture, start.Labels)
	}
}

func listedTools(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		out[tool.Name] = tool
	}
	return out
}

func goalRequired(t *testing.T, tool *mcp.Tool) bool {
	t.Helper()
	schema, _ := jsonValue(tool.InputSchema).(map[string]any)
	required, _ := schema["required"].([]any)
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props[userGoalParam]; !ok {
		t.Fatalf("%s lists no user_goal", tool.Name)
	}
	return slices.Contains(required, any(userGoalParam))
}

func TestEachToolListHasTheCallersGoalAndReports(t *testing.T) {
	doc := `{"version": 1, "audiences": [
		{"id": "quiet", "match": {"label": ["quiet"]}, "capture": {"goal": "optional", "reports": "off"}},
		{"id": "everyone", "match": {}, "capture": {}}]}`
	for _, tc := range []struct {
		label    string
		required bool
		reports  bool
	}{{"quiet", false, false}, {"loud", true, true}} {
		b, _ := holding(t, Config{ReportMode: ReportOn, ResolvePrincipal: labelled(tc.label)}, doc)
		cs := connect(t, wrapped(t, b, addLookup))
		tools := listedTools(t, cs)
		if got := goalRequired(t, tools["lookup"]); got != tc.required {
			t.Errorf("%s: user_goal required %v, want %v", tc.label, got, tc.required)
		}
		if _, got := tools[b.annotateName]; got != tc.reports {
			t.Errorf("%s: report tool listed %v, want %v", tc.label, got, tc.reports)
		}
		// Left out of the list, the report tool still answers a stale one.
		res := call(t, cs, b.annotateName, map[string]any{"what_happened": "x", "tool_name": "none"})
		if res.IsError {
			t.Errorf("%s: the report tool refused a report: %+v", tc.label, res.Content)
		}
	}
}

func TestTheSnapshotCarriesTheCodesValues(t *testing.T) {
	b, sink := holding(t, Config{ReportMode: ReportOn}, `{"version": 1, "audiences": [
		{"id": "everyone", "match": {}, "capture": {"goal": "optional", "reports": "off"}}]}`)
	call(t, connect(t, wrapped(t, b, addLookup)), "lookup", map[string]any{"name": "a"})
	snap := ofType(sink.all(), "surface_snapshot")[0]
	aug := snap.Payload.(surfaceSnapshot).SeamAugmentations
	if aug.IntentParam.Mode != "required" || !slices.Equal(aug.InjectedTools, []string{b.annotateName}) || !aug.InstructionsSuffix || snap.Capture != nil {
		t.Errorf("snapshot %+v, capture %+v", aug, snap.Capture)
	}
}

func TestTheSnapshotsInstructionsSuffixIsTheCodes(t *testing.T) {
	// With a tool's own user_goal, Baton's text is the report pointer alone,
	// which a document with reports off for everyone leaves out of the
	// instructions but not out of the snapshot.
	b, sink := holding(t, Config{ReportMode: ReportOn}, `{"version": 1, "audiences": [
		{"id": "everyone", "match": {}, "capture": {"reports": "off"}}]}`)
	server := wrapped(t, b, func(s *mcp.Server) {
		addLookup(s)
		s.AddTool(&mcp.Tool{Name: "own", InputSchema: json.RawMessage(`{"type":"object","properties":{"user_goal":{"type":"string"}}}`)},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
	})
	call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	if !ofType(sink.all(), "surface_snapshot")[0].Payload.(surfaceSnapshot).SeamAugmentations.InstructionsSuffix {
		t.Error("the snapshot followed the document, not the code")
	}
}

func TestTheInstructionsPointToReportsWhileSomeCallerCanGetThem(t *testing.T) {
	for doc, want := range map[string]bool{
		"": true,
		`{"version": 1, "audiences": [{"id": "everyone", "match": {}, "capture": {"reports": "off"}}]}`: false,
		`{"version": 1, "audiences": [{"id": "b", "match": {"label": ["b"]}, "capture": {"reports": "on"}},
			{"id": "everyone", "match": {}, "capture": {"reports": "off"}}]}`: true,
	} {
		b, _ := holding(t, Config{ReportMode: ReportOn}, doc)
		got := strings.Contains(connect(t, wrapped(t, b, addLookup)).InitializeResult().Instructions, b.annotateName)
		if got != want {
			t.Errorf("%.50s: pointer %v, want %v", doc, got, want)
		}
	}
}

// console is a fake collector that serves the settings document and takes
// events.
type console struct {
	mu       sync.Mutex
	status   int
	body     string
	etag     string
	delay    time.Duration
	requests []*http.Request
}

func (c *console) serve(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != settingsPath {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results": []}`))
			return
		}
		c.mu.Lock()
		c.requests = append(c.requests, r.Clone(context.Background()))
		status, body, etag, delay := c.status, c.body, c.etag, c.delay
		c.mu.Unlock()
		time.Sleep(delay)
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return "http://" + testKey + "@" + strings.TrimPrefix(ts.URL, "http://") + "/" + testWorkspace + "/srv"
}

func (c *console) reads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func TestASettingsReadSendsTheKeyTenantServerAndETag(t *testing.T) {
	c := &console{status: http.StatusOK, body: betaFullOthersOff, etag: `"v7"`}
	dsn := c.serve(t)
	d, err := parseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	r := newSettingsReader(d.origin, d.key, d.tenantID, d.vendorID, time.Minute)
	r.readOnce(context.Background())
	c.mu.Lock()
	c.status, c.body = http.StatusNotModified, ""
	c.mu.Unlock()
	r.readOnce(context.Background())
	if r.document() == nil || r.document().version != 7 {
		t.Fatal("a 200 and then a 304 did not leave the document held")
	}
	first, second := c.requests[0], c.requests[1]
	if first.Method != http.MethodGet || first.URL.Query().Get("server") != "srv" ||
		first.Header.Get("Authorization") != "Bearer "+testKey || first.Header.Get(settingsTenantHeader) != testWorkspace ||
		first.Header.Get("If-None-Match") != "" {
		t.Errorf("first read: %s %s %v", first.Method, first.URL, first.Header)
	}
	if second.Header.Get("If-None-Match") != `"v7"` {
		t.Errorf("second read sent If-None-Match %q", second.Header.Get("If-None-Match"))
	}
}

func TestAnAnswerOtherThan200Or204KeepsTheDocument(t *testing.T) {
	logs := captureLogs(t)
	c := &console{status: http.StatusOK, body: betaFullOthersOff}
	d, _ := parseDSN(c.serve(t))
	r := newSettingsReader(d.origin, d.key, d.tenantID, d.vendorID, time.Minute)
	r.readOnce(context.Background())
	for _, answer := range []struct {
		status int
		body   string
	}{{http.StatusInternalServerError, ""}, {http.StatusOK, "not json"}, {http.StatusOK, "{}"}, {http.StatusFound, ""}} {
		c.mu.Lock()
		c.status, c.body = answer.status, answer.body
		c.mu.Unlock()
		r.readOnce(context.Background())
		if r.document() == nil {
			t.Fatalf("%d %q dropped the document", answer.status, answer.body)
		}
	}
	if n := strings.Count(logs.String(), "cannot read"); n != 1 {
		t.Errorf("%d warnings about an unreadable document, want 1", n)
	}
	c.mu.Lock()
	c.status = http.StatusNoContent
	c.mu.Unlock()
	r.readOnce(context.Background())
	if r.document() != nil {
		t.Error("a 204 kept the document")
	}
}

func TestOneBatonReadsOnceForEveryServerAndStopsAtClose(t *testing.T) {
	c := &console{status: http.StatusOK, body: betaFullOthersOff}
	b, err := New(testImpl, nil, Config{DSN: c.serve(t), Settings: SettingsConsole})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		wrapped(t, b, addLookup)
	}
	waitFor(t, "the first read", func() bool { return b.settings.document() != nil })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b.Close(ctx)
	select {
	case <-b.settings.done:
	default:
		t.Error("the settings reader still runs after Close")
	}
	if n := c.reads(); n != 1 {
		t.Errorf("%d reads, want 1", n)
	}
}

func TestASlowReadHoldsUpNeitherInstallNorACall(t *testing.T) {
	c := &console{status: http.StatusOK, body: betaFullOthersOff, delay: 2 * time.Second}
	server := newVectorServer()
	addLookup(server)
	began := time.Now()
	h, err := Install(server, testImpl, nil, Config{DSN: c.serve(t), Settings: SettingsConsole})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())
	call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	if took := time.Since(began); took > time.Second {
		t.Errorf("Install and a call took %v while the settings read was slow", took)
	}
	if h.b.settings.document() != nil {
		t.Error("the document arrived before the slow read ended")
	}
}

func TestConsoleSettingsTurnReportsOnUnlessTheVendorSetsThem(t *testing.T) {
	t.Setenv("BATON_DSN", "")
	for name, tc := range map[string]struct {
		cfg  Config
		want ReportMode
	}{
		"console with a DSN":         {Config{DSN: testDSN, Settings: SettingsConsole}, ReportOn},
		"console, ReportOff set":     {Config{DSN: testDSN, Settings: SettingsConsole, ReportMode: ReportOff}, ReportOff},
		"local":                      {Config{DSN: testDSN}, ReportOff},
		"console with no DSN at all": {Config{Settings: SettingsConsole}, ReportOff},
	} {
		b, err := configure(testImpl, nil, tc.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if b.cfg.ReportMode != tc.want {
			t.Errorf("%s: ReportMode %q, want %q", name, b.cfg.ReportMode, tc.want)
		}
	}
	t.Setenv("BATON_DSN", testDSN)
	if b, _ := configure(testImpl, nil, Config{Settings: SettingsConsole}); b.cfg.ReportMode != ReportOn {
		t.Error("console with BATON_DSN did not turn reports on")
	}
}

func TestBadSettingsAreRefused(t *testing.T) {
	t.Setenv("BATON_DSN", "")
	for _, tc := range []struct {
		cfg  Config
		want string
	}{
		{Config{DSN: testDSN, Settings: "cloud"}, `Config.Settings is "cloud"; use one of ["local" "console"]`},
		{Config{VendorID: "srv", Settings: SettingsConsole}, "needs a DSN"},
		{Config{Settings: SettingsConsole}, "needs a DSN"},
		{Config{DSN: testDSN, SettingsRefresh: 5 * time.Second}, "at least 15s"},
		{Config{DSN: testDSN, SettingsRefresh: -time.Minute}, "at least 15s"},
	} {
		server := newVectorServer()
		if _, err := Install(server, testImpl, nil, tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: got %v, want %q", tc.cfg, err, tc.want)
		}
	}
}

func TestLocalSettingsAndAFailedInstallReadNothing(t *testing.T) {
	c := &console{status: http.StatusOK, body: betaFullOthersOff}
	dsn := c.serve(t)
	h, err := Install(newVectorServer(), testImpl, nil, Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(newVectorServer(), testImpl, nil, Config{DSN: dsn, Settings: SettingsConsole, ResultCaptureMode: "none"}); err == nil {
		t.Fatal("a bad config installed")
	}
	h.Close(context.Background())
	time.Sleep(50 * time.Millisecond)
	if n := c.reads(); n != 0 {
		t.Errorf("%d settings reads, want 0", n)
	}
}

func TestTheSettingsReaderNeverPanicsOut(t *testing.T) {
	var reads atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	r := newSettingsReader(ts.URL, testKey, testWorkspace, "srv", time.Minute)
	r.client = nil // a nil client panics in Do
	r.readOnce(context.Background())
	if r.document() != nil {
		t.Error("a read that panicked stored a document")
	}
}
