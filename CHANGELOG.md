# Changelog

## Unreleased

- **A panic on the HTTP sink's sending goroutine no longer crashes the
  process.** That sink stops sending, and logs one warning that gives the
  panic's type only. The events it held then are counted `write_failed`, or
  `refused` or `encode_failed` when it had already found one could not be
  sent, and every event recorded after it is counted `write_failed`. `Close`
  and `Flush` return at once. If the panic came in the middle of updating its
  queue, these counts may be off by the events that update touched.
- **A flood of events that cannot be sent no longer crashes the process.**
  The sending goroutine called itself once for each group of such events,
  so a long enough flood overflowed its stack. It now loops.
- **An event whose encoding stopped part way is dropped as `encode_failed`,**
  not sent. Under `GODEBUG=panicnil=1`, `encoding/json` returns a cut-short
  body with no error when a `MarshalJSON` panics with nil, and the collector
  then refused the whole batch.
- **A send that `Close` cancels no longer counts as a failure of the
  console.** It could log "the console is failing" after `Close` returned.
  And when `Close` cancelled a loss report being sent alone, that could pause
  sending, so events `Close` still had time to send were counted unsent.

## 0.1.6

- **`ResolvePrincipal` takes a `baton.Caller`,** as the hooks in `baton-sdk`
  and `baton-ts` take one context: the verified token, the HTTP headers,
  `_meta`, the tool's name and its arguments (without `user_goal`). **This
  breaks a hook written for 0.1.5 or earlier:** change `token *auth.TokenInfo` to
  `c baton.Caller` and read `c.Token`.
- **`PrincipalFromOAuthSub` and `PrincipalFromOAuthEmail`** read the `sub` or
  `email` claim from the token's `Extra`, as in `baton-ts`.
- **A `ResolvePrincipal` that takes longer than 5 seconds is cut off,** as in
  `baton-sdk` and `baton-ts`: the call goes on, its event has no principal,
  and the first time a warning is logged. A hook that ignores `ctx` keeps
  running, so while 64 timed-out calls are still running in the process, the
  hook is not called at all. Calls that finish in time never count.

- **With no DSN and `ReportMode: "on"`, a bad `AnnotationToolName` is an
  error,** as it is with a DSN, and as in `baton-sdk`. Before, it was let
  through and Baton installed nothing.

- **Close counts the batch it gave up on before it returns** (SPEC §11.2
  rule 8). 100 ms before the end of its budget (or half the budget, when
  that is shorter), close cancels the send and waits the rest of the budget
  for it to end, then counts what the collector has not answered. `Close`
  still returns within its budget. Before, close returned without waiting
  and the batch was counted only when its request ended, so a process that
  exited right after `Close` counted nothing for it. An answer that arrives
  after close counted changes no count. The close warning, now "closed with
  events not delivered", also gives `refused` and `encode_failed` counts.
- **Of several refused results for one event, the last code counts** (SPEC
  §2.5), as in `baton-sdk`. Before, the first one counted.
- **An event that close finds already measured as too big is counted
  `refused`, and one that could not be encoded `encode_failed`** (SPEC
  §2.5), not `unsent_at_close`. An event close finds not yet measured is
  still `unsent_at_close`.
- **A server named exactly `fastmcp` or `mcp-server`, in any case, counts as
  unnamed,** as in `baton-sdk`: the report tool is `<vendor>_annotate` and the
  text names the vendor id. Before, Go skipped any name whose slug was
  `mcp-server`, such as `MCP Server`, for the tool name only, and not
  `fastmcp`.
- **`AnnotationToolName` with `ReportMode` off logs a warning** that it has no
  effect, as in `baton-sdk` and `baton-ts`.

## 0.1.5

- **Events are sent in batches** (SPEC §2.5), to `POST /v0/events/batch`,
  one request at a time. A batch goes when 100 events are waiting, when it
  would pass 1 MiB, or 5 seconds after the oldest waiting event. `Close`
  sends at once. So an event can reach Baton up to 5 seconds after it was
  recorded, and a process that stops without `Close` can lose those last
  5 seconds of events. A collector without the batch endpoint (it answers
  404 or 405) still works: the SDK then sends one event per request to
  `/v0/events`, and loses nothing in the switch. A 429 with a
  `Retry-After` of 1 to 60 seconds pauses sending for that long. An event
  too big for a 1 MiB batch is never sent, and is counted as refused.

## 0.1.4

- **Prompt, resource and tool-list requests are recorded** (SPEC §11.4.4,
  §11.4.5), as in `baton-sdk` and `baton-ts`: `prompt_list`, `prompt_get`,
  `resource_list`, `resource_read` and `tool_list`, each a start and an end
  or error. A prompt's arguments and a read's params are scrubbed. A
  listing's end carries how many items it returned. No event carries a
  prompt's messages or a resource's contents. A listing Baton runs for
  itself sends none.
  - A prompt or read that pauses for the client's input (go-sdk v1.7+) is one
    request, as a tool call is. go-sdk cannot complete a paused read for a
    client of the older protocol and answers it with an error, which is the
    read's `resource_read_error`.
  - On stateless HTTP, each listing is its own session, as each call is.
  - **Under `ResultCapture: "off"` the message of a failed listing, prompt or
    resource request is still sent** (SPEC §11.4.4), and `Install` logs a
    warning about it once.

## 0.1.3

- **`tool_call_end.result` is the whole tool result,** as in `baton-ts`: its
  `content`, and its `structuredContent` when the tool has one. A typed tool
  made with `mcp.AddTool` always has one. Before, only `content` was sent.
  **If you pass your own `Scrubber`, check it:** the result now reaches it as
  an object with a `content` list inside, not as the list itself.
- **go-sdk's own checks of a typed tool are named** in `failure_kind`
  (SPEC §11.4.3): `invalid_argument` when its schema check rejects the
  arguments, and `output_schema_mismatch` when it rejects or cannot encode
  the tool's output. They happen below Baton, so Baton reads go-sdk's fixed
  messages; a test checks them on every go-sdk version CI runs. Under
  `ResultCapture: "off"` both texts are withheld, because a tool can return
  the same words. Not named: arguments that pass the schema but not the
  conversion to your Go type (go-sdk gives that error no fixed message), and
  `refused`, which Go cannot see.
- **`error_body` is cut at 2,000 code points** after scrubbing, as in
  `baton-sdk` and `baton-ts`.
- **On SSE, a call's session is go-sdk's own `sessionid`** from the client's
  endpoint URL, sent with `session_id_origin: "observed"` and
  `transport_observed: "http"` (SPEC §3.4 rung 4b). Before, it was an id
  Baton made per connection, `"minted"`, with no transport.

## 0.1.2

- **Works on go-sdk v1.7 and v1.8, as well as v1.6.1.** go.mod still
  requires v1.6.1, so installing this package does not move your go-sdk.
  CI tests v1.6.1 and v1.7.0, and v1.8.0 with a client of each protocol.
  - **A tool that returns no result no longer crashes your server on
    go-sdk v1.7+.** There go-sdk panics on a nil result when the client
    speaks MCP 2026-07-28, even without Baton. Baton now sends
    `{"content": []}` instead, as go-sdk's typed `AddTool` does. This is on
    every go-sdk version: on v1.6.1 the result was `null` before.
  - **Baton's text is added to the server instructions on `server/discover`
    too.** A 2026-07-28 client asks there, not on `initialize`, so before
    it never saw the sentence that tells a subagent to number its calls,
    nor the pointer to the annotation tool.
  - **A call that pauses for the client's input (go-sdk v1.7+) is one call,**
    as in `baton-sdk`: the paused round sends no `tool_call_end`, and the
    round that resumes it sends no new `tool_call_start`. With a 2026-07-28
    client the rounds are separate requests, so its start and its end carry
    different `call_id`s (SPEC §13, 0.8.0).
  - On the new protocol, and on any stateless server on go-sdk v1.7+, each
    HTTP request is its own session (SPEC §3.4). `runtime_meta` carries the
    `io.modelcontextprotocol/` keys a 2026-07-28 client writes (SPEC §5.2).

- **A client that still sends the retired `expected_result` or
  `overall_task` has them recorded as `call_expected` and `call_workflow`**
  (SPEC §13, 0.8.22). Before, Go removed them from the arguments but dropped
  the values, so the turn number in `overall_task` never reached the Console.

## 0.1.1

- **The HTTP sink sends a `client_report` event (SPEC §11.4.7) with this
  process's lost-event counts by reason.** It is sent only after a delivery,
  only when the counts changed, at most once a minute, and never at close. A
  healthy run sends none. It goes beside the event queue, so it never delays
  an event, and `Close` cancels one still in flight. A lost report is not
  counted. A collector that
  answers it with 400 or 422 gets no more reports from that sink.
- **Names with "İ" (U+0130) now match `baton-sdk`.** Go lowercases İ to "i",
  but Python lowercases it to "i" plus a dot mark, which matches no field
  name. Three places follow Python now:
  - The scrubber no longer redacts a key such as `APİ_KEY` as `api_key`, so
    its value is sent unless a pattern rule catches it.
  - A `_meta` key such as `LATİTUDE` is no longer rounded as a coordinate.
  - A server called "İAPI" gets the annotation tool `i-api_annotate`, not
    `iapi_annotate`.
- **`params` is always an object, as the schema says.** Before, a collector
  that checks the schema could refuse the event.
  - With no arguments it is `{}`, as in `baton-sdk`. So are arguments that
    are not an object, such as `[1, 2]`. Before, it was `null` or the array.
  - A custom `Scrubber` now sees that `{}`: it is called on calls with no
    arguments, and never sees arguments that are not an object.
  - When the `Scrubber` returns something that cannot be sent as an object,
    such as a string, it is `{}`, and this is logged once.
- **A custom `Scrubber`'s result is copied as plain JSON data while the call
  runs.**
  - `params`, `runtime_meta` (the request's `_meta`) and an annotation's
    `context` keep a result that is an object of another Go type, such as a
    `map[string]string`. Before, `runtime_meta` and `context` sent it as
    `null`.
  - A result that cannot be encoded, such as one holding a value whose
    `MarshalJSON` panics, costs what a `Scrubber` panic in that place costs:
    the value for `params`, `runtime_meta`, `user_goal` and
    `client_observed`; the event, counted as `encode_failed`, for a result
    and an annotation. Before, the panic happened in the background sender
    and stopped your server.
  - The sender never reads a map your code still holds.

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
