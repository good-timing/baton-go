package baton

import (
	"context"
	"maps"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// around sends a request's start and its end, by the given builders. As in
// baton-sdk, a round that resumes a paused request is not a new start, and a
// paused round is not an end (TestAPausedCallIsOneCall, TestAPausedRequestIsOne).
func (b *installed) around(ctx context.Context, req mcp.Request, kind string,
	start func(cc *callContext, resuming bool),
	serve func(cc callContext) (mcp.Result, error),
	end func(cc callContext, res mcp.Result, err error, elapsed int64),
) (mcp.Result, error) {
	var (
		cc    callContext
		ready bool
	)
	resuming := resumesAPause(req.GetParams())
	guard(kind+"_start", func() {
		cc = b.newCallContext(ctx, req)
		start(&cc, resuming)
		ready = true
	})

	began := time.Now()
	res, err := serve(cc)
	elapsed := time.Since(began).Milliseconds()

	if ready && !paused(res) {
		guard(kind+"_end", func() { end(cc, res, err, elapsed) })
	}
	return res, err
}

var lifecycleKinds = map[string]string{
	"tools/list":     "tool_list",
	"prompts/list":   "prompt_list",
	"resources/list": "resource_list",
	"prompts/get":    "prompt_get",
	"resources/read": "resource_read",
}

// lifecycle records a listing, prompt get or resource read (SPEC §11.4.4,
// §11.4.5).
func (b *installed) lifecycle(ctx context.Context, req mcp.Request, kind string, serve func(cc callContext) (mcp.Result, error)) (mcp.Result, error) {
	start, end := b.lifecycleEvents(req, kind)
	return b.around(ctx, req, kind, start, serve, end)
}

func (b *installed) lifecycleEvents(req mcp.Request, kind string) (
	start func(cc *callContext, resuming bool),
	end func(cc callContext, res mcp.Result, err error, elapsed int64),
) {
	var subject map[string]any
	return func(cc *callContext, resuming bool) {
			var params map[string]any
			subject, params = b.lifecycleSubject(req)
			if resuming {
				return
			}
			event := maps.Clone(subject)
			if params != nil {
				event["params"] = params
			}
			b.emit(*cc, kind+"_start", event)
		},
		func(cc callContext, res mcp.Result, err error, elapsed int64) {
			outcome := subject
			outcome["duration_ms"] = elapsed
			if err != nil {
				outcome["error_type"], outcome["error_body"] = errorType(err), b.errorBody(err.Error())
				b.emit(cc, kind+"_error", outcome)
				return
			}
			if n, ok := listCount(res); ok {
				outcome["count"] = n
			}
			b.emit(cc, kind+"_end", outcome)
		}
}

// lifecycleSubject's params are what SPEC §11.4.4 records: a prompt's
// arguments alone, and a read's params without _meta, uri included.
func (b *installed) lifecycleSubject(req mcp.Request) (subject, params map[string]any) {
	switch r := req.(type) {
	case *mcp.GetPromptRequest:
		if r.Params != nil {
			return map[string]any{"name": r.Params.Name}, b.scrubParams(jsonValue(r.Params.Arguments))
		}
	case *mcp.ReadResourceRequest:
		if r.Params != nil {
			read, _ := jsonValue(r.Params).(map[string]any)
			delete(read, "_meta")
			return map[string]any{"uri": r.Params.URI}, b.scrubParams(read)
		}
	}
	return map[string]any{}, nil
}

// listCount is the length of the one page a listing answered. Resource
// templates are a separate method and are not counted (SPEC §11.4.4).
func listCount(res mcp.Result) (int, bool) {
	switch r := res.(type) {
	case *mcp.ListToolsResult:
		return len(r.Tools), true
	case *mcp.ListPromptsResult:
		return len(r.Prompts), true
	case *mcp.ListResourcesResult:
		return len(r.Resources), true
	}
	return 0, false
}
