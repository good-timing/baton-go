package baton

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The annotate tool is served by the middleware, not registered on the
// server: its name is Baton's, and a stateless server has no session to
// register it against.
type annotateTool struct {
	tool    *mcp.Tool
	refusal string
}

func newAnnotateTool(name, displayName string) annotateTool {
	text := map[string]any{"type": "string"}
	return annotateTool{
		tool: &mcp.Tool{
			Name:        name,
			Description: annotateDescription(displayName),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					userGoalParam:           text,
					"what_happened":         text,
					"tool_name":             text,
					"suggested_improvement": text,
					"context":               map[string]any{"type": "object"},
				},
				"required": []string{userGoalParam, "tool_name"},
			},
		},
		refusal: refusalText(name),
	}
}

type annotateArgs struct {
	UserGoal             string          `json:"user_goal"`
	WhatHappened         string          `json:"what_happened"`
	ToolName             *string         `json:"tool_name"`
	SuggestedImprovement string          `json:"suggested_improvement"`
	Context              json.RawMessage `json:"context"`
}

func (b *installed) answerAnnotate(ctx context.Context, r *mcp.CallToolRequest) *mcp.CallToolResult {
	var args annotateArgs
	if len(r.Params.Arguments) > 0 {
		if err := json.Unmarshal(r.Params.Arguments, &args); err != nil {
			return toolAnswer(map[string]any{"ok": false, "error": "arguments must be a JSON object: " + err.Error()}, true)
		}
	}
	// SPEC §11.4: an annotation is a report only when its account is filled.
	if strings.TrimSpace(args.WhatHappened) == "" {
		return toolAnswer(map[string]any{"ok": false, "error": b.annotate.refusal}, false)
	}
	// The agent's report is answered even when its event is lost
	// (TestAScrubberThatPanicsOnAnAnnotationStillAnswersIt).
	guard("annotation", func() { b.emitAnnotation(ctx, r, args) })
	return toolAnswer(map[string]any{"ok": true}, false)
}

func (b *installed) emitAnnotation(ctx context.Context, r *mcp.CallToolRequest, args annotateArgs) {
	// tool_name is sent as given, so "" (no tool exists) and absent differ
	// (SPEC §5.1.1).
	payload := annotation{
		Intent:               b.scrubOptional(args.UserGoal),
		WhatHappened:         b.scrubText(args.WhatHappened),
		SuggestedImprovement: b.scrubOptional(args.SuggestedImprovement),
		ToolName:             args.ToolName,
	}
	if args.ToolName != nil && *args.ToolName != "" {
		payload.ToolName = b.scrubText(*args.ToolName)
	}
	if extra, ok := jsonValue(args.Context).(map[string]any); ok && len(extra) > 0 {
		payload.Context = asObject(b.scrubValue(extra))
	}
	b.emit(b.newCallContext(ctx, r), "annotation", payload)
}

func toolAnswer(body map[string]any, isError bool) *mcp.CallToolResult {
	raw, _ := json.Marshal(body)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(raw)}},
		StructuredContent: body,
		IsError:           isError,
	}
}
