# Changelog

## 0.0.1

The first release: `baton.Install` on a go-sdk `*mcp.Server`.

- **Tool calls are captured** as `tool_call_start`, then `tool_call_end` or
  `tool_call_error`, with the same envelope as `baton-sdk` and `baton-ts`. A
  call to a tool that does not exist is a `tool_call_error` with
  `failure_kind: "unknown_tool"`. Each change to the tool list sends a
  `surface_snapshot`.
- **`user_goal`** is added to every tool's schema and stripped before your
  handler sees it, and one sentence is added below your server's
  instructions. With `ReportMode: "on"`, Baton also serves an annotation tool.
- **The DSN** comes from `Config.DSN` or `BATON_DSN`. With neither, `Install`
  logs one warning and installs nothing.
- **Events are sent in the background**, so a slow or broken collector never
  slows a tool call. `Handle.Close(ctx)` sends what is left, for at most
  `ctx`'s deadline or 3 seconds, and never returns an error.
- **The scrubber is on by default**, with `baton-sdk`'s ruleset, checked
  against baton-spec's 800 scrub cases. `NoScrub` turns it off.
- **`ResultCapture: "off"`** withholds results and error text.
  `ResolvePrincipal` names the caller from the request's token.
- **`BATON_DISABLED=1`** installs nothing and checks nothing.
- stdio, streamable HTTP (stateful and stateless) and SSE are supported.

Not here yet:

- `client_observed` is not sent, so the Console shows "app not reported" for
  sessions from this SDK.
- No library API, no events for resources, prompts or `tools/list`, and no
  local mode.
