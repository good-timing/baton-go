# Changelog

## Unreleased

- **Names with "İ" (U+0130) now match `baton-sdk`.** Go lowercases İ to "i",
  but Python lowercases it to "i" plus a dot mark, which matches no field
  name. Three places follow Python now:
  - The scrubber no longer redacts a key such as `APİ_KEY` as `api_key`, so
    its value is sent unless a pattern rule catches it.
  - A `_meta` key such as `LATİTUDE` is no longer rounded as a coordinate.
  - A server called "İAPI" gets the annotation tool `i-api_annotate`, not
    `iapi_annotate`.

## 0.1.0

- **Every event the SDK does not deliver is counted, by reason:**
  `buffer_full`, `refused`, `encode_failed`, `after_close` and
  `unsent_at_close`, the names `baton-sdk` and `baton-ts` use. The counts are
  internal for now. After a loss, the first event delivered logs one warning
  with what was lost, at most once a minute. A run that loses nothing logs
  nothing new.
- **Events name the client (`client_observed`, SPEC §11.4).** Each tool call
  and agent report now carries the client's name and version. They come from
  the request's `_meta`, or else from the connection's handshake. On HTTP it
  also carries the `User-Agent` and `X-Anthropic-Client` headers, and no other
  header. Every value is scrubbed, then cut to 128 characters (256 for a
  header). The Console shows the client's name instead of "app not reported".
  `surface_snapshot` does not carry it.
- **A `Scrubber` that panics no longer loses the whole call.** On `_meta` or
  `user_goal`, only that value is left out. On the arguments, `params` is
  sent as `{}`, as in `baton-sdk`. Before, any of these lost the call's
  `tool_call_start` and `tool_call_end`. The same holds for what the client
  declares. Each is logged once. A panic on a result or error text still drops
  that one event, as in `baton-sdk`, and it is counted.
- **A `Scrubber` that panics on an agent's report no longer fails the
  annotation tool.** The agent is answered `{"ok": true}`, and the event is
  dropped and counted. Before, the agent got an "unknown tool" error.

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
