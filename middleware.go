package baton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
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
		case "initialize", "server/discover":
			res, err := next(ctx, method, req)
			if err == nil {
				guard("instructions", func() {
					if instructions := instructionsOf(res); instructions != nil {
						if l, ok := b.vendorTools(ctx, next, req); ok {
							b.addInstructions(instructions, l)
						}
					}
				})
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
			processDrops.add(dropEncodeFailed, 1)
			slog.Error("baton: internal failure; the request goes on without it", "step", what, "panic", r)
		}
	}()
	f()
}

// listing is the vendor's tools as one request sees them.
type listing []*mcp.Tool

func (l listing) find(name string) *mcp.Tool {
	if i := slices.IndexFunc(l, func(t *mcp.Tool) bool { return t.Name == name }); i >= 0 {
		return l[i]
	}
	return nil
}

// schema is t's input schema as plain JSON data, cached per tool. It is
// shared: read it, never edit it.
func (b *installed) schema(t *mcp.Tool) map[string]any {
	return b.schemas.getOrPut(t, func() map[string]any {
		s, _ := jsonValue(t.InputSchema).(map[string]any)
		return s
	})
}

func (b *installed) declares(t *mcp.Tool, param string) bool {
	props, _ := b.schema(t)["properties"].(map[string]any)
	_, ok := props[param]
	return ok
}

func (b *installed) anyDeclaresUserGoal(l listing) bool {
	return slices.ContainsFunc(l, func(t *mcp.Tool) bool { return b.declares(t, userGoalParam) })
}

// ourInstructions is Baton's paragraph: the report pointer when Baton serves
// the report tool, then the subagent sentence unless a tool declares its own
// user_goal, where the sentence would send the turn number into the vendor's
// argument.
func (b *installed) ourInstructions(l listing) string {
	pointer := ""
	if b.servesAnnotate(l) {
		pointer = b.reportPointer
	}
	if b.anyDeclaresUserGoal(l) {
		return pointer
	}
	return strings.TrimSpace(pointer + instructionsSubagentClause)
}

// addInstructions puts Baton's paragraph after the vendor's own text in this
// response (SPEC §5.1.2).
func (b *installed) addInstructions(instructions *string, l listing) {
	ours := b.ourInstructions(l)
	if ours == "" {
		return
	}
	*instructions = withVendorInstructions(*instructions, ours)
	if n := utf8.RuneCountInString(*instructions); n > claudeCodeTruncCap {
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
// checks auth sees the same caller. ok is false when the listing failed,
// which may be the vendor's auth refusing the caller; nothing is concluded from it.
func (b *installed) vendorTools(ctx context.Context, next mcp.MethodHandler, req mcp.Request) (l listing, ok bool) {
	ss, _ := req.GetSession().(*mcp.ServerSession)
	cursor := ""
	for range 1000 {
		res, err := next(ctx, "tools/list", &mcp.ListToolsRequest{Session: ss, Extra: req.GetExtra(), Params: &mcp.ListToolsParams{Cursor: cursor}})
		page, isList := res.(*mcp.ListToolsResult)
		if err != nil || !isList {
			return nil, false
		}
		l = append(l, page.Tools...)
		if page.NextCursor == "" {
			return l, true
		}
		cursor = page.NextCursor
	}
	return nil, false
}

// servesAnnotate is false when a vendor tool has the annotate tool's name,
// which wins over Baton's.
func (b *installed) servesAnnotate(l listing) bool {
	if b.annotateName == "" {
		return false
	}
	if l.find(b.annotateName) != nil {
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
	// A first page with no next page is the whole list.
	l, ok := listing(r.Tools), true
	if params, _ := req.GetParams().(*mcp.ListToolsParams); params != nil && params.Cursor != "" {
		l, ok = b.vendorTools(ctx, next, req)
	}
	if ok && b.servesAnnotate(l) {
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
	callID      string
	principal   *identityWire
	org         *identityWire
	runtimeMeta map[string]any
	client      *clientObserved
}

func (b *installed) newCallContext(ctx context.Context, r *mcp.CallToolRequest) callContext {
	var header http.Header
	var token *auth.TokenInfo
	if r.Extra != nil {
		header, token = r.Extra.Header, r.Extra.TokenInfo
	}
	cc := callContext{
		session: b.sessions.resolve(r.Session, header),
	}
	cc.principal, cc.org = resolveCaller(ctx, b.cfg.ResolvePrincipal, token)
	cc.client = b.observeClient(r.Params.Meta, r.Session, header)
	if len(r.Params.Meta) > 0 {
		meta, _ := jsonValue(map[string]any(r.Params.Meta)).(map[string]any)
		// Rounded before the vendor's scrubber, so NoScrub keeps the rounding.
		cc.runtimeMeta, _ = b.scrubOrNil(roundMetaCoordinates(meta), "runtime_meta").(map[string]any)
	}
	return cc
}

func (b *installed) toolCall(ctx context.Context, next mcp.MethodHandler, method string, r *mcp.CallToolRequest) (mcp.Result, error) {
	var (
		l      listing
		listed bool
	)
	guard("tools", func() { l, listed = b.vendorTools(ctx, next, r) })

	if listed && r.Params.Name == b.annotateName && b.servesAnnotate(l) {
		var res mcp.Result
		guard("annotate", func() { res = b.answerAnnotate(ctx, r) })
		if res != nil {
			return res, nil
		}
	}

	var (
		cc    callContext
		ready bool
	)
	tool := l.find(r.Params.Name)
	// With no listing, a tool is not assumed missing.
	known := tool != nil || !listed
	// As in baton-sdk, a round that resumes a paused call is not a new start,
	// and a paused round is not an end.
	resuming := resumesAPause(r.Params)
	guard("tool_call_start", func() {
		params, goal := b.stripGoalParams(r, tool)
		cc = b.newCallContext(ctx, r)
		cc.callID = uuid7()
		if !resuming {
			if listed {
				b.maybeSnapshot(l)
			}
			start := toolCallStart{ToolName: r.Params.Name, Params: b.scrubParams(params), CallIntent: b.scrubOptionalOrNil(goal, userGoalParam)}
			if start.CallIntent != nil {
				start.IntentSource = ptr(intentSourceParam)
			}
			b.emit(cc, "tool_call_start", start)
		}
		ready = true
	})

	began := time.Now()
	res, err := next(ctx, method, r)
	elapsed := time.Since(began).Milliseconds()

	if ready && !paused(res) {
		guard("tool_call_end", func() { b.emitOutcome(cc, r.Params.Name, known, res, err, elapsed) })
	}
	// go-sdk v1.7+ panics on a nil result under MCP 2026-07-28; its typed
	// AddTool sends an empty one (TestAHandlerReturningNothingStillEnds).
	if result, ok := res.(*mcp.CallToolResult); err == nil && (res == nil || ok && result == nil) {
		res = &mcp.CallToolResult{Content: []mcp.Content{}}
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
func (b *installed) stripGoalParams(r *mcp.CallToolRequest, tool *mcp.Tool) (any, string) {
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
		if !present || (tool != nil && b.declares(tool, name)) {
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
		failure.ErrorType = errorType(raised)
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

// errorType is the error's Go type, looking through fmt.Errorf's wrapper,
// which names no failure: "lookup: %w" around a notFound is notFound.
func errorType(err error) string {
	for {
		name := reflect.TypeOf(err).String()
		inner := errors.Unwrap(err)
		if !strings.HasPrefix(name, "*fmt.") || inner == nil {
			return name
		}
		err = inner
	}
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

// scrubOrNil is for a value read before its event is built: a Scrubber that
// panics costs only that value (TestAScrubberThatPanicsBeforeTheEventCostsOnlyThatValue).
// Inside the build, a panic drops the event, which guard counts: a null result
// on tool_call_error would read as a raise (SPEC §11.4.3).
func (b *installed) scrubOrNil(v any, field string) (out any) {
	defer func() {
		if r := recover(); r != nil {
			if _, logged := b.scrubPanicLogged.LoadOrStore(field, true); !logged {
				slog.Error("baton: the Scrubber panicked, or its result cannot be encoded; that value is not sent, and later panics on it are not logged", "field", field, "panic", r)
			}
			out = nil
		}
	}()
	return b.scrub(v)
}

// scrub copies a custom Scrubber's result as plain JSON data, so the sender never
// encodes a vendor type (TestAScrubbedValueThatCannotBeEncodedCostsWhatAScrubberPanicCosts).
func (b *installed) scrub(v any) any {
	out := b.cfg.Scrubber(v)
	if !b.customScrubber {
		return out
	}
	raw, err := json.Marshal(out)
	if err != nil {
		panic(fmt.Sprintf("the Scrubber's result cannot be encoded (%T)", err))
	}
	return jsonValue(json.RawMessage(raw))
}

func (b *installed) scrubOptionalOrNil(s, field string) *string {
	if s == "" {
		return nil
	}
	if out, ok := b.scrubOrNil(s, field).(string); ok {
		return &out
	}
	return nil
}

// The schema says params is an object, so a value that is not one is sent as
// {} (TestParamsIsAlwaysAnObject, TestAScrubberThatPanicsOnTheArgumentsSendsNoneOfThem).
func (b *installed) scrubParams(params any) map[string]any {
	if _, ok := params.(map[string]any); !ok {
		params = map[string]any{}
	}
	out := b.scrubOrNil(params, "params")
	if m, _ := out.(map[string]any); m != nil {
		return m
	}
	if out != nil {
		b.paramsShapeWarned.Do(func() {
			slog.Error("baton: the Scrubber returned params that cannot be sent as a JSON object; {} is sent instead", "type", reflect.TypeOf(out))
		})
	}
	return map[string]any{}
}

func (b *installed) scrubValue(v any) any {
	if v == nil {
		return nil
	}
	return b.scrub(v)
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
	b.sink.write(Event{
		EventID:           uuid7(),
		TenantID:          b.tenantID,
		VendorID:          b.vendorID,
		SessionID:         cc.session.id,
		SequenceNumber:    b.seq.next(cc.session),
		CapturedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		ConsentToken:      b.cfg.ConsentToken,
		SDKVersion:        sdkVersion,
		AgentRuntime:      "unknown",
		Principal:         cc.principal,
		Organization:      cc.org,
		CallID:            nonEmpty(cc.callID),
		TransportObserved: cc.session.transport,
		SessionIDOrigin:   &cc.session.origin,
		RuntimeMeta:       cc.runtimeMeta,
		ClientObserved:    cc.client,
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

func (l *lastSurface) unchanged(tools listing) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen && slices.Equal(l.tools, tools) {
		return true
	}
	l.seen = true
	l.tools = slices.Clone(tools)
	return false
}

func (b *installed) sortedToolMaps(l listing) []map[string]any {
	sorted := slices.SortedFunc(slices.Values(l), func(x, y *mcp.Tool) int { return strings.Compare(x.Name, y.Name) })
	out := make([]map[string]any, 0, len(sorted))
	for _, t := range sorted {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": b.schema(t),
		})
	}
	return out
}
