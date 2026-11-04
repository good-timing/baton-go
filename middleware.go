package baton

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
				guard("setup answer", func() { b.keepSetupAnswer(res) })
				guard("instructions", func() {
					if instructions := instructionsOf(res); instructions != nil {
						if l, ok := b.vendorTools(ctx, next, req); ok {
							b.addInstructions(instructions, l, b.reportsReachable())
						}
					}
				})
			}
			return res, err
		case "tools/call":
			if r, ok := req.(*mcp.CallToolRequest); ok && r.Params != nil {
				return b.toolCall(ctx, next, method, r)
			}
		}
		if kind, ok := lifecycleKinds[method]; ok {
			serve := func(callContext) (mcp.Result, error) { return next(ctx, method, req) }
			if method == "tools/list" {
				serve = func(cc callContext) (mcp.Result, error) { return b.listTools(ctx, next, req, cc.settings) }
			}
			return b.lifecycle(ctx, req, kind, serve)
		}
		return next(ctx, method, req)
	}
}

type passageKey struct{}

// passage is one tools/call's trip through the vendor's middleware.
type passage struct {
	reached atomic.Bool
	// run is what Baton does with a call the vendor's middleware lets
	// through, with the ctx that middleware passed on: serve calls the tool.
	run func(ctx context.Context, serve func(context.Context) (mcp.Result, error)) (mcp.Result, error)
}

// innerMiddleware is the innermost middleware when Config.Middleware is set.
// It runs a tools/call's passage once the vendor's middleware lets it
// through, and keeps the handler below that middleware for Baton's listings.
func (b *installed) innerMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	b.belowVendor = next
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		p, ok := ctx.Value(passageKey{}).(*passage)
		if !ok {
			// The vendor's middleware passed on a new ctx; the request is the
			// same one (TestAMiddlewareThatDropsTheCtxDoesNotMakeCallsRefused).
			if v, found := b.passages.Load(req); found {
				p, ok = v.(*passage), true
			}
		}
		if !ok {
			return next(ctx, method, req)
		}
		if p.reached.Swap(true) {
			// A retry, or a second call the vendor's middleware made with this
			// ctx: recorded once (TestAMiddlewareThatRetriesRecordsTheCallOnce).
			return next(ctx, method, req)
		}
		return p.run(ctx, func(c context.Context) (mcp.Result, error) { return next(c, method, req) })
	}
}

// startPassage gives r a passage that innerMiddleware can find by ctx or by
// request; end forgets it.
func (b *installed) startPassage(ctx context.Context, r *mcp.CallToolRequest, p *passage) (context.Context, func()) {
	b.passages.Store(mcp.Request(r), p)
	return context.WithValue(ctx, passageKey{}, p), func() { b.passages.Delete(mcp.Request(r)) }
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
// the report tool and reports reach someone, then the subagent sentence
// unless a tool declares its own user_goal, where the sentence would send the
// turn number into the vendor's argument.
func (b *installed) ourInstructions(l listing, reports bool) string {
	pointer := ""
	if reports && b.servesAnnotate(l) {
		pointer = b.reportPointer
	}
	if b.anyDeclaresUserGoal(l) {
		return pointer
	}
	return strings.TrimSpace(pointer + instructionsSubagentClause)
}

// addInstructions puts Baton's paragraph after the vendor's own text in this
// response (SPEC §5.1.2).
func (b *installed) addInstructions(instructions *string, l listing, reports bool) {
	ours := b.ourInstructions(l, reports)
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
// checks auth sees the same caller. Config.Middleware is skipped: it would be
// charged for a request the caller never sent. ok is false when the listing
// failed, which may be the vendor's auth refusing the caller; nothing is
// concluded from it.
func (b *installed) vendorTools(ctx context.Context, next mcp.MethodHandler, req mcp.Request) (l listing, ok bool) {
	if b.placesVendorMiddleware() && b.belowVendor != nil {
		next = b.belowVendor
	}
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

// reportsReachable is whether some caller can get the report tool: with no
// settings document held, the Config's ReportMode decides (SPEC §2.6). The
// instructions go out once per connection, so they are the same for every
// caller.
func (b *installed) reportsReachable() bool {
	doc := b.settings.document()
	return doc == nil || doc.reportsOnForAnyone(b.cfg.ReportMode)
}

func (b *installed) listTools(ctx context.Context, next mcp.MethodHandler, req mcp.Request, cs callSettings) (mcp.Result, error) {
	res, err := next(ctx, "tools/list", req)
	if r, ok := res.(*mcp.ListToolsResult); ok && err == nil {
		var listed *mcp.ListToolsResult
		guard("tools/list", func() { listed = b.augmentToolList(ctx, next, req, r, cs) })
		if listed != nil {
			return listed, nil
		}
	}
	return res, err
}

// listedKey is a tool as listed under one goal setting.
type listedKey struct {
	tool *mcp.Tool
	goal IntentParamMode
}

// augmentToolList returns a copy of the page with user_goal in each schema,
// and the annotate tool on the last page, under the caller's goal and reports
// settings (SPEC §2.6). With reports off, the annotate tool is left out of
// the list but still answers.
func (b *installed) augmentToolList(ctx context.Context, next mcp.MethodHandler, req mcp.Request, r *mcp.ListToolsResult, cs callSettings) *mcp.ListToolsResult {
	goal := cmp.Or(cs.goal, b.cfg.IntentParamMode)
	out := *r
	out.Tools = make([]*mcp.Tool, 0, len(r.Tools)+1)
	for _, t := range r.Tools {
		out.Tools = append(out.Tools, b.listed.getOrPut(listedKey{t, goal}, func() *mcp.Tool { return b.withUserGoal(t, goal) }))
	}
	if r.NextCursor != "" || b.annotateName == "" || cmp.Or(cs.reports, b.cfg.ReportMode) == ReportOff {
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
func (b *installed) withUserGoal(t *mcp.Tool, goal IntentParamMode) *mcp.Tool {
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
	props[userGoalParam] = map[string]any{"type": "string", "description": userGoalDescription(goal)}
	if goal == IntentParamRequired {
		required, _ := schema["required"].([]any)
		schema["required"] = append(required, userGoalParam)
	}
	return &copied
}

type callContext struct {
	session     callSession
	callID      string
	who         who
	runtimeMeta map[string]any
	client      *ClientObserved
	settings    callSettings
}

func (b *installed) newCallContext(ctx context.Context, r mcp.Request) callContext {
	var header http.Header
	var token *auth.TokenInfo
	if extra := r.GetExtra(); extra != nil {
		header, token = extra.Header, extra.TokenInfo
	}
	session, _ := r.GetSession().(*mcp.ServerSession)
	var meta mcp.Meta
	if params := r.GetParams(); params != nil && !reflect.ValueOf(params).IsNil() {
		meta = params.GetMeta()
	}
	cc := callContext{
		session: b.sessions.resolve(session, header),
	}
	if b.cfg.ResolvePrincipal != nil {
		cc.who = b.resolveCaller(ctx, func() Caller { return callerOf(r, header, token, meta) })
	}
	cc.client = b.observeClient(meta, session, header)
	if doc := b.settings.document(); doc != nil {
		cc.settings = b.settingsFor(doc, cc)
	}
	if len(meta) > 0 {
		meta, _ := jsonValue(map[string]any(meta)).(map[string]any)
		// Rounded before the vendor's scrubber, so NoScrub keeps the rounding.
		cc.runtimeMeta, _ = b.scrubOrNil(roundMetaCoordinates(meta), "runtime_meta").(map[string]any)
	}
	return cc
}

// settingsFor leaves the Config's values for a call whose matching fails.
// No event is lost, so it is not counted as a drop, as guard would.
func (b *installed) settingsFor(doc *settingsDocument, cc callContext) (s callSettings) {
	defer func() {
		if r := recover(); r != nil {
			logRecovered("baton: applying capture settings failed; this call uses the Config's values", r)
			s = callSettings{}
		}
	}()
	return doc.settingsFor(cc.matchView(), b.vendorID)
}

func (cc callContext) matchView() matchView {
	v := matchView{labels: cc.who.labels}
	if cc.client != nil && cc.client.Info != nil {
		v.client = cc.client.Info.Name
	}
	if id := cmp.Or(cc.who.principal, cc.who.org); id != nil {
		v.shareKey = id.ID
	}
	return v
}

// callerOf copies what the hook may read, so a hook still running after its
// timeout does not share it with the request (TestTheResolverGetsCopies,
// TestTheResolversTokenIsACopy). The token's claims keep their types, so a
// nested claim is shared.
func callerOf(r mcp.Request, header http.Header, token *auth.TokenInfo, meta mcp.Meta) Caller {
	c := Caller{Header: header.Clone(), Arguments: map[string]any{}}
	if token != nil {
		copied := *token
		copied.Scopes = slices.Clone(token.Scopes)
		copied.Extra = maps.Clone(token.Extra)
		c.Token = &copied
	}
	if len(meta) > 0 {
		c.Meta, _ = jsonValue(map[string]any(meta)).(map[string]any)
	}
	if call, ok := r.(*mcp.CallToolRequest); ok && call.Params != nil {
		c.ToolName = call.Params.Name
		if args, _ := jsonValue(call.Params.Arguments).(map[string]any); args != nil {
			c.Arguments = args
		}
	}
	return c
}

func (b *installed) toolCall(ctx context.Context, next mcp.MethodHandler, method string, r *mcp.CallToolRequest) (mcp.Result, error) {
	var (
		l      listing
		listed bool
	)
	guard("tools", func() { l, listed = b.vendorTools(ctx, next, r) })

	if listed && r.Params.Name == b.annotateName && b.servesAnnotate(l) {
		answer := func(ctx context.Context) (res *mcp.CallToolResult) {
			guard("annotate", func() { res = b.answerAnnotate(ctx, r) })
			return res
		}
		if !b.placesVendorMiddleware() {
			if res := answer(ctx); res != nil {
				return res, nil
			}
		} else {
			// Through the vendor's middleware, which may check auth on it too.
			passCtx, forget := b.startPassage(ctx, r, &passage{run: func(ctx context.Context, serve func(context.Context) (mcp.Result, error)) (mcp.Result, error) {
				if res := answer(ctx); res != nil {
					return res, nil
				}
				return serve(ctx)
			}})
			defer forget()
			return emptyIfNil(next(passCtx, method, r))
		}
	}

	tool := l.find(r.Params.Name)
	// With no listing, a tool is not assumed missing.
	known := tool != nil || !listed
	var (
		params any
		goals  map[string]string
	)
	guard("user_goal", func() { params, goals = b.stripGoalParams(r, tool) })

	sendStart := func(cc *callContext, resuming bool) {
		cc.callID = uuid7()
		if resuming {
			return
		}
		if listed {
			b.maybeSnapshot(l)
		}
		start := toolCallStart{
			ToolName:     r.Params.Name,
			Params:       b.scrubParams(params),
			CallIntent:   b.scrubOptionalOrNil(goals[userGoalParam], userGoalParam),
			CallExpected: b.scrubOptionalOrNil(goals[expectedResultParam], expectedResultParam),
			CallWorkflow: b.scrubOptionalOrNil(goals[overallTaskParam], overallTaskParam),
		}
		if start.CallIntent != nil || start.CallExpected != nil || start.CallWorkflow != nil {
			start.IntentSource = ptr(intentSourceParam)
		}
		b.emit(*cc, "tool_call_start", start)
	}
	sendEnd := func(kind *string) func(callContext, mcp.Result, error, int64) {
		return func(cc callContext, res mcp.Result, err error, elapsed int64) {
			b.emitOutcome(cc, r.Params.Name, known, tool != nil && tool.OutputSchema != nil, kind, res, err, elapsed)
		}
	}

	var (
		res mcp.Result
		err error
	)
	if !b.placesVendorMiddleware() {
		res, err = b.around(ctx, r, "tool_call", sendStart, func(callContext) (mcp.Result, error) { return next(ctx, method, r) }, sendEnd(nil))
	} else {
		// A call the vendor's middleware lets through is recorded inside it,
		// with the ctx it passed on, so ResolvePrincipal sees what its auth
		// set (TestResolvePrincipalSeesWhatTheVendorsMiddlewareSet). Here,
		// only a call it stopped.
		pass := &passage{run: func(ctx context.Context, serve func(context.Context) (mcp.Result, error)) (mcp.Result, error) {
			return b.around(ctx, r, "tool_call", sendStart, func(callContext) (mcp.Result, error) { return serve(ctx) }, sendEnd(nil))
		}}
		passCtx, forget := b.startPassage(ctx, r, pass)
		defer forget()
		resuming := resumesAPause(r.GetParams())
		began := time.Now()
		res, err = next(passCtx, method, r)
		elapsed := time.Since(began).Milliseconds()
		if !pass.reached.Load() && !paused(res) {
			guard("tool_call_refused", func() {
				cc := b.newCallContext(ctx, r)
				sendStart(&cc, resuming)
				sendEnd(ptr("refused"))(cc, res, err, elapsed)
			})
		}
	}
	return emptyIfNil(res, err)
}

// emptyIfNil sends an empty result for a nil one: go-sdk v1.7+ panics on a nil
// result under MCP 2026-07-28 (TestAHandlerReturningNothingStillEnds).
func emptyIfNil(res mcp.Result, err error) (mcp.Result, error) {
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
// float. It returns the arguments the vendor receives, and the non-blank
// string value of each name it removed.
func (b *installed) stripGoalParams(r *mcp.CallToolRequest, tool *mcp.Tool) (any, map[string]string) {
	var args map[string]json.RawMessage
	if len(r.Params.Arguments) == 0 || json.Unmarshal(r.Params.Arguments, &args) != nil || args == nil {
		return jsonValue(r.Params.Arguments), nil
	}
	names := unknownToolGoalParams
	if tool != nil {
		names = knownToolGoalParams
	}
	before := len(args)
	var goals map[string]string
	for _, name := range names {
		raw, present := args[name]
		if !present || (tool != nil && b.declares(tool, name)) {
			continue
		}
		delete(args, name)
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			if goals == nil {
				goals = map[string]string{}
			}
			goals[name] = s
		}
	}
	if len(args) != before {
		if raw, err := json.Marshal(args); err == nil {
			r.Params.Arguments = raw
		}
	}
	return jsonValue(r.Params.Arguments), goals
}

// emitOutcome sends the call's end or error. A failure is classified, kind
// replaces the classification when set, and then it is withheld in one place
// under ResultCaptureMode "off" (SPEC §11.4.3).
func (b *installed) emitOutcome(cc callContext, toolName string, known, checksOutput bool, kind *string, res mcp.Result, err error, elapsed int64) {
	result, _ := res.(*mcp.CallToolResult)
	failure := toolCallError{ToolName: toolName, DurationMS: elapsed}
	var raised error
	switch {
	case err != nil:
		raised = err
		if !known {
			failure.FailureKind = ptr("unknown_tool")
		} else if checksOutput && goSDKRejectedOutput(err) {
			failure.FailureKind = ptr("output_schema_mismatch")
		}
	case result == nil || !result.IsError:
		end := toolCallEnd{ToolName: toolName, DurationMS: elapsed}
		if b.withholding(cc) {
			end.ResultCapture = ptr("off")
		} else if result != nil {
			end.Result = b.scrubValue(jsonValue(result))
		}
		b.emit(cc, "tool_call_end", end)
		return
	case result.GetError() != nil:
		// go-sdk turns the handler's returned error into this result and keeps
		// the error: a raise, in SPEC §11.4.3's terms.
		raised = result.GetError()
		if goSDKRejectedArguments(raised) {
			failure.FailureKind = ptr("invalid_argument")
		}
	default:
		failure.ErrorType = "tool_error"
		failure.ErrorBody = b.errorBody(contentText(result.Content))
		failure.Result = b.scrubValue(jsonValue(result))
	}
	if raised != nil {
		failure.ErrorType = errorType(raised)
		failure.ErrorBody = b.errorBody(raised.Error())
	}
	if kind != nil {
		failure.FailureKind = kind
	}
	// unknown_tool's message describes the request. go-sdk's argument message
	// does too, but a tool can return the same words, so it is withheld.
	if b.withholding(cc) && (failure.FailureKind == nil || *failure.FailureKind != "unknown_tool") {
		var public *PublicError
		if errors.As(raised, &public) {
			failure.ErrorBody = b.errorBody(public.Message)
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

// withholding is the call's results setting, from the Console's settings
// when they set one (SPEC §2.6).
func (b *installed) withholding(cc callContext) bool {
	return cmp.Or(cc.settings.results, b.cfg.ResultCaptureMode) == ResultCaptureOff
}

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

const errorBodyCap = 2000

// errorBody is s scrubbed, then cut to errorBodyCap code points, as baton-sdk does.
func (b *installed) errorBody(s string) *string {
	if out := b.scrubText(s); out != nil {
		return ptr(cutRunes(*out, errorBodyCap))
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
		SequenceNumber:    b.sequence(cc.session),
		CapturedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		ConsentToken:      b.cfg.ConsentToken,
		SDKVersion:        sdkVersion,
		AgentRuntime:      "unknown",
		Principal:         cc.who.principal,
		Organization:      cc.who.org,
		Labels:            cc.who.labels,
		Capture:           cc.settings.capture,
		CallID:            nonEmpty(cc.callID),
		TransportObserved: cc.session.transport,
		SessionIDOrigin:   &cc.session.origin,
		RuntimeMeta:       cc.runtimeMeta,
		ClientObserved:    cc.client,
		EventType:         eventType,
		Payload:           payload,
	})
}

func (b *installed) sequence(sess callSession) int64 {
	if sess.id == b.sessions.fallback {
		return b.sessions.fallbackSeq.Add(1)
	}
	return b.seq.next(sess)
}

// lastSurface is the tool list and capabilities the last snapshot was checked
// against, so an unchanged surface skips the hash.
type lastSurface struct {
	mu    sync.Mutex
	seen  bool
	tools []*mcp.Tool
	caps  *capabilities
}

func (l *lastSurface) unchanged(tools listing, caps *capabilities) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen && l.caps == caps && slices.Equal(l.tools, tools) {
		return true
	}
	l.seen = true
	l.tools = slices.Clone(tools)
	l.caps = caps
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
