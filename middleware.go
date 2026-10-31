package baton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (b *installed) middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		switch method {
		case "initialize":
			res, err := next(ctx, method, req)
			if r, ok := res.(*mcp.InitializeResult); ok && err == nil {
				guard("instructions", func() { b.addInstructions(r, b.vendorTools(ctx, next, req)) })
			}
			return res, err
		case "tools/list":
			res, err := next(ctx, method, req)
			if r, ok := res.(*mcp.ListToolsResult); ok && err == nil {
				var listed *mcp.ListToolsResult
				guard("tools/list", func() { listed = b.augmentToolList(ctx, next, req, r) })
				if listed != nil {
					return listed, nil
				}
			}
			return res, err
		case "tools/call":
			if r, ok := req.(*mcp.CallToolRequest); ok && r.Params != nil {
				return b.toolCall(ctx, next, method, r)
			}
		}
		return next(ctx, method, req)
	}
}

// guard keeps a Baton failure out of the vendor's request (SPEC §11.2).
func guard(what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("baton: internal failure; the request goes on without it", "step", what, "panic", r)
		}
	}()
	f()
}

// vendorTool is a tool as listed, with its input schema as plain JSON data.
// The schema is shared through the cache: read it, never edit it.
type vendorTool struct {
	tool   *mcp.Tool
	schema map[string]any
}

func (t vendorTool) declares(param string) bool {
	props, _ := t.schema["properties"].(map[string]any)
	_, ok := props[param]
	return ok
}

func anyDeclaresUserGoal(tools []vendorTool) bool {
	return slices.ContainsFunc(tools, func(t vendorTool) bool { return t.declares(userGoalParam) })
}

func findTool(tools []vendorTool, name string) *vendorTool {
	if i := slices.IndexFunc(tools, func(t vendorTool) bool { return t.tool.Name == name }); i >= 0 {
		return &tools[i]
	}
	return nil
}

// listing is the vendor's tools as one request sees them. ok is false when
// the listing failed, and then nothing is concluded from it.
type listing struct {
	tools []vendorTool
	ok    bool
}

func (b *installed) ourInstructions(l listing) string {
	pointer := ""
	if b.servesAnnotate(l) {
		pointer = b.reportPointer
	}
	return ourInstructions(pointer, anyDeclaresUserGoal(l.tools))
}

// addInstructions puts Baton's paragraph after the vendor's own text in this
// response (SPEC §5.1.2).
func (b *installed) addInstructions(r *mcp.InitializeResult, l listing) {
	if !l.ok {
		return
	}
	ours := b.ourInstructions(l)
	if ours == "" {
		return
	}
	r.Instructions = withVendorInstructions(r.Instructions, ours)
	if n := utf8.RuneCountInString(r.Instructions); n > claudeCodeTruncCap {
		b.truncationWarned.Do(func() {
			slog.Warn("baton: the server instructions are longer than Claude Code keeps; the end of Baton's text is lost there",
				"chars", n, "cap", claudeCodeTruncCap)
		})
	}
}

// vendorTools lists every tool the vendor registered, all pages, without
// Baton's additions. Listed per call rather than kept: tools can be added at
// any time, and on a stateless server no earlier tools/list reached this
// process. The request's own Extra goes along, so vendor middleware that
// checks auth sees the same caller.
func (b *installed) vendorTools(ctx context.Context, next mcp.MethodHandler, req mcp.Request) listing {
	ss, _ := req.GetSession().(*mcp.ServerSession)
	var tools []vendorTool
	cursor := ""
	for range 1000 {
		res, err := next(ctx, "tools/list", &mcp.ListToolsRequest{Session: ss, Extra: req.GetExtra(), Params: &mcp.ListToolsParams{Cursor: cursor}})
		page, ok := res.(*mcp.ListToolsResult)
		if err != nil || !ok {
			return listing{}
		}
		for _, t := range page.Tools {
			tools = append(tools, b.withSchema(t))
		}
		if page.NextCursor == "" {
			return listing{tools: tools, ok: true}
		}
		cursor = page.NextCursor
	}
	return listing{}
}

func (b *installed) withSchema(t *mcp.Tool) vendorTool {
	schema := b.schemas.getOrPut(t, func() map[string]any {
		s, _ := jsonValue(t.InputSchema).(map[string]any)
		return s
	})
	return vendorTool{tool: t, schema: schema}
}

// servesAnnotate is false when the listing failed, which may be the
// vendor's auth refusing the caller, and when a vendor tool has the annotate
// tool's name, which wins over Baton's.
func (b *installed) servesAnnotate(l listing) bool {
	if b.annotateName == "" || !l.ok {
		return false
	}
	if findTool(l.tools, b.annotateName) != nil {
		b.clashWarned.Do(func() {
			slog.Warn("baton: a tool of yours has the annotation tool's name, so Baton's is not served; set Config.AnnotationToolName",
				"name", b.annotateName)
		})
		return false
	}
	return true
}

// augmentToolList returns a copy of the page with user_goal in each schema,
// and the annotate tool on the last page.
func (b *installed) augmentToolList(ctx context.Context, next mcp.MethodHandler, req mcp.Request, r *mcp.ListToolsResult) *mcp.ListToolsResult {
	out := *r
	out.Tools = make([]*mcp.Tool, 0, len(r.Tools)+1)
	for _, t := range r.Tools {
		out.Tools = append(out.Tools, b.listed.getOrPut(t, func() *mcp.Tool { return b.withUserGoal(t) }))
	}
	if r.NextCursor != "" || b.annotateName == "" {
		return &out
	}
	l := listing{ok: true}
	if params, _ := req.GetParams().(*mcp.ListToolsParams); params != nil && params.Cursor != "" {
		l = b.vendorTools(ctx, next, req)
	} else {
		// A first page with no next page is the whole list.
		for _, t := range r.Tools {
			l.tools = append(l.tools, b.withSchema(t))
		}
	}
	if b.servesAnnotate(l) {
		out.Tools = append(out.Tools, b.annotate.tool)
	}
	return &out
}

// withUserGoal is t as listed: a copy with user_goal in its schema. go-sdk
// hands out its registry's own *Tool values, so they are never edited.
func (b *installed) withUserGoal(t *mcp.Tool) *mcp.Tool {
	copied := *t
	schema, ok := jsonValue(t.InputSchema).(map[string]any)
	if !ok {
		return &copied
	}
	copied.InputSchema = schema
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		if _, present := schema["properties"]; present {
			return &copied
		}
		props = map[string]any{}
		schema["properties"] = props
	}
	if _, native := props[userGoalParam]; native {
		return &copied
	}
	props[userGoalParam] = map[string]any{"type": "string", "description": b.userGoalDescription}
	if len(b.requiredNames) > 0 {
		required, _ := schema["required"].([]any)
		for _, name := range b.requiredNames {
			required = append(required, name)
		}
		schema["required"] = required
	}
	return &copied
}

type callContext struct {
	session     callSession
	transport   *string
	callID      string
	principal   *identityWire
	org         *identityWire
	runtimeMeta map[string]any
}

func (b *installed) newCallContext(ctx context.Context, r *mcp.CallToolRequest) callContext {
	var header http.Header
	var token *auth.TokenInfo
	if r.Extra != nil {
		header, token = r.Extra.Header, r.Extra.TokenInfo
	}
	cc := callContext{
		session:   b.sessions.resolve(r.Session, header),
		transport: transportObserved(header),
	}
	cc.principal, cc.org = resolveCaller(ctx, b.cfg.ResolvePrincipal, token)
	if len(r.Params.Meta) > 0 {
		meta, _ := jsonValue(map[string]any(r.Params.Meta)).(map[string]any)
		// Rounded before the vendor's scrubber, so NoScrub keeps the rounding.
		if m, ok := b.scrubValue(roundMetaCoordinates(meta)).(map[string]any); ok {
			cc.runtimeMeta = m
		}
	}
	return cc
}

func (b *installed) toolCall(ctx context.Context, next mcp.MethodHandler, method string, r *mcp.CallToolRequest) (mcp.Result, error) {
	var l listing
	guard("tools", func() { l = b.vendorTools(ctx, next, r) })

	if r.Params.Name == b.annotateName && b.servesAnnotate(l) {
		var res mcp.Result
		guard("annotate", func() { res = b.answerAnnotate(ctx, r) })
		if res != nil {
			return res, nil
		}
	}

	var (
		cc      callContext
		started bool
	)
	tool := findTool(l.tools, r.Params.Name)
	// With no listing, a tool is not assumed missing.
	known := tool != nil || !l.ok
	guard("tool_call_start", func() {
		params, goal := b.stripGoalParams(r, tool)
		cc = b.newCallContext(ctx, r)
		cc.callID = uuid7()
		if l.ok {
			b.maybeSnapshot(l)
		}
		start := toolCallStart{ToolName: r.Params.Name, Params: b.scrubValue(params), CallIntent: b.scrubOptional(goal)}
		if start.CallIntent != nil {
			start.IntentSource = ptr(intentSourceParam)
		}
		b.emit(cc, "tool_call_start", start)
		started = true
	})

	began := time.Now()
	res, err := next(ctx, method, r)
	elapsed := time.Since(began).Milliseconds()

	if started {
		guard("tool_call_end", func() { b.emitOutcome(cc, r.Params.Name, known, res, err, elapsed) })
	}
	return res, err
}

var (
	unknownToolGoalParams = []string{userGoalParam}
	knownToolGoalParams   = append([]string{userGoalParam}, retiredParams...)
)

// stripGoalParams removes user_goal and the retired names from the call's
// arguments before go-sdk checks them against the tool's schema (SPEC
// §5.1.2), except a name the tool declares itself. Other values pass through
// as the client wrote them, so a large integer is not rounded through a
// float. It returns the arguments the vendor receives, and the goal.
func (b *installed) stripGoalParams(r *mcp.CallToolRequest, tool *vendorTool) (any, string) {
	var args map[string]json.RawMessage
	if len(r.Params.Arguments) == 0 || json.Unmarshal(r.Params.Arguments, &args) != nil || args == nil {
		return jsonValue(r.Params.Arguments), ""
	}
	names := unknownToolGoalParams
	if tool != nil {
		names = knownToolGoalParams
	}
	before := len(args)
	goal := ""
	for _, name := range names {
		raw, present := args[name]
		if !present || (tool != nil && tool.declares(name)) {
			continue
		}
		delete(args, name)
		var s string
		if name == userGoalParam && json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			goal = s
		}
	}
	if len(args) != before {
		if raw, err := json.Marshal(args); err == nil {
			r.Params.Arguments = raw
		}
	}
	return jsonValue(r.Params.Arguments), goal
}

// emitOutcome sends the call's end or error. A failure is classified first,
// then withheld in one place under ResultCapture "off" (SPEC §11.4.3).
func (b *installed) emitOutcome(cc callContext, toolName string, known bool, res mcp.Result, err error, elapsed int64) {
	result, _ := res.(*mcp.CallToolResult)
	failure := toolCallError{ToolName: toolName, DurationMS: elapsed}
	var raised error
	switch {
	case err != nil:
		raised = err
		if !known {
			failure.FailureKind = ptr("unknown_tool")
		}
	case result == nil || !result.IsError:
		end := toolCallEnd{ToolName: toolName, DurationMS: elapsed}
		if b.withholding() {
			end.ResultCapture = ptr("off")
		} else if result != nil {
			end.Result = b.scrubValue(jsonValue(result.Content))
		}
		b.emit(cc, "tool_call_end", end)
		return
	case result.GetError() != nil:
		// go-sdk turns the handler's returned error into this result and keeps
		// the error: a raise, in SPEC §11.4.3's terms.
		raised = result.GetError()
	default:
		failure.ErrorType = "tool_error"
		failure.ErrorBody = b.scrubText(contentText(result.Content))
		failure.Result = b.scrubValue(jsonValue(result))
	}
	if raised != nil {
		failure.ErrorType = fmt.Sprintf("%T", raised)
		failure.ErrorBody = b.scrubText(raised.Error())
	}
	// unknown_tool's message stays: it describes the request, not a result.
	if b.withholding() && failure.FailureKind == nil {
		var public *PublicError
		if errors.As(raised, &public) {
			failure.ErrorBody = b.scrubText(public.Message)
		} else {
			failure.ErrorBody, failure.Result, failure.ResultCapture = ptr(""), nil, ptr("off")
		}
	}
	b.emit(cc, "tool_call_error", failure)
}

func (b *installed) withholding() bool { return b.cfg.ResultCapture == "off" }

func contentText(content []mcp.Content) string {
	var parts []string
	for _, c := range content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (b *installed) scrubValue(v any) any {
	if v == nil {
		return nil
	}
	return b.cfg.Scrubber(v)
}

func (b *installed) scrubText(s string) *string {
	if out, ok := b.cfg.Scrubber(s).(string); ok {
		return &out
	}
	return nil
}

// scrubOptional is nil for "", so an unset text member goes out as null.
func (b *installed) scrubOptional(s string) *string {
	if s == "" {
		return nil
	}
	return b.scrubText(s)
}

func (b *installed) emit(cc callContext, eventType string, payload any) {
	b.cfg.sink.write(Event{
		EventID:           uuid7(),
		TenantID:          b.tenantID,
		VendorID:          b.cfg.VendorID,
		SessionID:         cc.session.id,
		SequenceNumber:    b.seq.next(cc.session),
		CapturedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		ConsentToken:      b.cfg.ConsentToken,
		SDKVersion:        sdkVersion,
		AgentRuntime:      "unknown",
		Principal:         cc.principal,
		Organization:      cc.org,
		CallID:            nonEmpty(cc.callID),
		TransportObserved: cc.transport,
		SessionIDOrigin:   cc.session.origin,
		RuntimeMeta:       cc.runtimeMeta,
		EventType:         eventType,
		Payload:           payload,
	})
}

// lastSurface is the tool list the last snapshot was checked against, so an
// unchanged list skips the hash.
type lastSurface struct {
	mu    sync.Mutex
	seen  bool
	tools []*mcp.Tool
}

func (l *lastSurface) unchanged(tools []vendorTool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen && slices.EqualFunc(l.tools, tools, func(a *mcp.Tool, t vendorTool) bool { return a == t.tool }) {
		return true
	}
	l.seen = true
	l.tools = l.tools[:0]
	for _, t := range tools {
		l.tools = append(l.tools, t.tool)
	}
	return false
}

func sortedToolMaps(tools []vendorTool) []map[string]any {
	sorted := slices.SortedFunc(slices.Values(tools), func(x, y vendorTool) int { return strings.Compare(x.tool.Name, y.tool.Name) })
	out := make([]map[string]any, 0, len(sorted))
	for _, t := range sorted {
		out = append(out, map[string]any{
			"name":        t.tool.Name,
			"description": t.tool.Description,
			"inputSchema": t.schema,
		})
	}
	return out
}
