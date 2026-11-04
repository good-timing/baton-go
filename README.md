# baton-go

**Stable from v1.0.0.** This module follows [semantic versioning](https://semver.org). A breaking change means a v2, with a `/v2` module path. The public API is every exported identifier in this package, except these, which are **experimental** and may change in a minor release: `Config.Settings`, `Config.SettingsRefresh`, `SettingsMode` and its values, `Principal.Labels`, `Organization.Labels`, `Event.Labels`, `Event.Capture` and `CaptureObserved`.

baton-go v1.0 covers the MCP path only: a go-sdk `*mcp.Server`. The library API (SPEC §5.6) is not in 1.0; it will come later as an addition. The events Baton sends only grow: nothing is removed or changes meaning, except what SPEC marks experimental. See [SPEC §13, "Stability from 1.0"](https://github.com/good-timing/baton/blob/main/docs/SPEC.md#stability-from-10).

MCP standardizes how agents discover and call your tools. It does not capture *why* a call happened or whether it helped the user. Baton instruments those interactions on the vendor side by wrapping your MCP server. It captures intent, the tool calls, expected outcomes and observed outcomes, plus friction signals, and sends each one to Baton.

This is the Go SDK, for servers built on the official [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk). It takes the same DSN and sends events in the same format as the [Python SDK](https://pypi.org/project/baton-sdk/) and the [TypeScript SDK](https://www.npmjs.com/package/@goodtiming/baton-sdk). **The docs are at [goodtiming.ai/docs.html](https://goodtiming.ai/docs.html)**. Their examples are Python and TypeScript; what they say about events, the DSN and the Console applies here too.

## Install

```sh
go get github.com/good-timing/baton-go
```

Go 1.25+, and `github.com/modelcontextprotocol/go-sdk` v1.6.1 or later in v1. If your server is on an older go-sdk, `go get` moves it up to v1.6.1; a newer one stays where it is. Tested on v1.6.1, v1.7.0 and v1.8.0, and on v1.8.0 with clients of both MCP protocols (2025-11-25 and 2026-07-28).

## Quickstart

```go
package main

import (
	"context"
	"log"

	baton "github.com/good-timing/baton-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type lookupIn struct {
	Name string `json:"name"`
}

func lookup(ctx context.Context, req *mcp.CallToolRequest, in lookupIn) (*mcp.CallToolResult, any, error) {
	return nil, map[string]string{"found": in.Name}, nil
}

func main() {
	impl := &mcp.Implementation{Name: "your-vendor-mcp", Version: "1.0.0"}
	server := mcp.NewServer(impl, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "lookup"}, lookup)

	handle, err := baton.Install(server, impl, nil, baton.Config{
		DSN: "https://baton_pk_...@baton.goodtiming.ai/ten_.../your-vendor",
	})
	if err != nil {
		log.Fatal(err) // a bad DSN or config; nothing was installed
	}
	defer handle.Close(context.Background())

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Print(err)
	}
}
```

**Pass `Install` the same `impl` and options you gave `mcp.NewServer`** (the options may be nil in both places). Baton reads your server's name, version and instructions from them. It never calls your server to find them out. Keep `impl` and the options in variables, as above, so `mcp.NewServer` and `Install` get the same ones. A nil `impl` or an empty `impl.Name` is an error.

Copy the DSN from **/account**. It packs four values: the collector to send to, your workspace, this server, and the key that binds them. The SDK unpacks them and sends to the collector itself. Tools added before and after `Install` are both captured.

**Installing changes what your server advertises.** One sentence is added below your server's own instructions, and a `user_goal` parameter is added to every tool's schema and stripped again before your handler sees it. With `ReportMode: baton.ReportOn`, an annotation tool named after your server (slugged, so a server called "Acme Knowledge Base" gets `acme-knowledge-base_annotate`) is also served, and the added text tells the agent when to report a problem with it. [What gets injected](https://goodtiming.ai/docs.html#injected-tools).

**If you distribute your server, put the DSN in your source.** A stdio server runs on your user's machine, spawned by their MCP client, which passes it a fixed allowlist of environment variables (six names on macOS and Linux) plus whatever that user wrote in their own client config. Nothing from your `.env` is in either list. For a hosted server, set `BATON_DSN` and pass `baton.Config{}`. An explicit `DSN` wins over `BATON_DSN`.

**With no DSN, Baton installs nothing,** unless you set `Sink` or `VendorID` (see [Events to a file or stderr](#events-to-a-file-or-stderr)). `Install` logs one warning and returns a handle whose `Close` does nothing. Your server starts as usual. A config value that is not valid, such as `ResultCaptureMode: "none"`, is still an error.

**Call `Close` when your server stops.** Events are sent in the background, so a slow or broken collector never slows a tool call. They are sent in batches, so an event can reach the console up to 5 seconds after it was recorded. `Close(ctx)` sends what is still buffered and returns when it is done. It stops sending shortly before `ctx`'s deadline, or before 3 seconds if `ctx` has none, so it can count what it did not deliver and return at about that time. It never returns an error. Events recorded after `Close` are dropped.

## Events to a file or stderr

Without a collector, send events to a `Sink` instead of a DSN. Each event is one line of JSON.

```go
handle, err := baton.Install(server, impl, opts, baton.Config{
	Sink:     baton.FileSink("events.jsonl"),
	VendorID: "acme",
	TenantID: "ten_0123abcd", // optional; unset, it is VendorID
})
```

- `baton.StdoutSink()` writes to **stderr**, because stdout carries the stdio transport. It is the sink when you set `VendorID` and no `Sink`.
- `baton.FileSink(path)` appends to `path`. It creates the file on the first event, not before.
- `baton.MultiSink(a, b)` writes every event to each sink.
- Your own type works too: anything with `Write(ctx, []baton.Event) error` and `Close(ctx) error`.

`VendorID` is required with a `Sink`. A `DSN` in `Config` together with `Sink`, `VendorID` or `TenantID` is an error, because both name where the events go. A `BATON_DSN` in the environment is ignored, with one warning.

Baton calls a sink only from its own goroutine, never from a tool call, so a slow disk does not slow your server. It holds up to 1,000 events in front of the sink, and drops the oldest when that is full. A `Write` that returns an error or panics loses that batch. `Close` writes what is held within its deadline, then calls the sink's `Close` once. Baton counts every event it loses, and logs a warning, but it sends no loss report through a sink: there is no collector to read one.

## Stateless HTTP

go-sdk's stateless mode is supported. Build the server once, call `Install` once, and hand that server to the handler:

```go
handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
	&mcp.StreamableHTTPOptions{Stateless: true})
```

**Do not call `Install` per request.** Each `Install` starts its own sender, which runs until its `Close`, so a server built and installed per request leaks one per request. Use `New` and `Wrap` instead (next section).

Your server's capabilities are sent once Baton sees a client's first `initialize` or `server/discover`. A stateless process may serve a tool call before that. Its first `surface_snapshot` then has no capabilities, and a second one with them follows.

When the client sends no `Mcp-Session-Id`, each request is its own session. Signals that need several calls in one session, such as a retry loop, do not fire across requests. To see one person's requests together, return a principal from `ResolvePrincipal` ([Who is calling](#who-is-calling)).

## Servers built per request

If your handler builds a new `*mcp.Server` for each request or session, make one `Baton` at startup and wrap each server:

```go
impl := &mcp.Implementation{Name: "your-vendor-mcp", Version: "1.0.0"}
opts := &mcp.ServerOptions{Instructions: "..."} // or nil

b, err := baton.New(impl, opts, baton.Config{DSN: os.Getenv("BATON_DSN")})
if err != nil {
	log.Fatal(err) // a config that is not valid
}
defer b.Close(context.Background())

handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
	s := mcp.NewServer(impl, opts)
	// your AddTool lines
	b.Wrap(s)
	return s
}, &mcp.StreamableHTTPOptions{Stateless: true})
```

- Build every server with the `impl` and `opts` you gave `New`. Servers with a different name need their own `Baton`.
- Every wrapped server shares one sender and one sink, which `b.Close` closes.
- `Wrap` never fails a request. It returns an error only for a nil server. A server already wrapped is skipped, so go-sdk asking for the same server twice is safe.
- Pass your own middleware in `Config.Middleware`, as with `Install`. `Wrap` adds it to each server once, even when Baton records nothing. That happens with a config error from `New`, with no DSN, `Sink`, `VendorID` or `TenantID`, with `BATON_DISABLED=1`, or after `Close`. Do not add it again.
- `Install(server, impl, opts, cfg)` is the one-server form; its handle's `Close` closes its sender.

## What leaves your server

Only what crosses the MCP call. The SDK never reads your logs, your database or anything else in your process.

| Sent | What |
|---|---|
| Always | The tool's name, its arguments (`params`, scrubbed), `user_goal`, how long the call took, and on failure the `error_type`. The request's `_meta`, as `runtime_meta` (scrubbed, with any latitude and longitude rounded). What the client declares about itself, such as its name and version, and on HTTP its `User-Agent` and `X-Anthropic-Client` headers. Once per change: your server's name, version, capabilities and instructions, and each tool's name, description and schemas. Who the caller is and their company, only if you set `ResolvePrincipal`. For a prompt or a resource: its name and arguments (scrubbed), or its URI, how long the request took, and on failure the error's type and message. How many tools, prompts or resources a listing returned. Never a prompt's messages or a resource's contents. |
| Only with `ResultCaptureMode: baton.ResultCaptureFull` (the default) | What the tool returned (`result`), including any error text in it, and the message of an `error` your handler returned. |
| With `ReportMode: baton.ReportOn` | The reports the agent files, in its own words. |
| Never | Anything that did not cross the MCP call. |

## PII scrubbing

**On by default.** `baton.DefaultScrubber` walks the arguments, results, error text, `_meta` and agent reports, and redacts email / `Bearer …` / `sk-*` / `AKIA*` / JWT / Luhn-validated card / NA-format phone, plus a list of sensitive field names. It is the same ruleset as the Python SDK, checked against the same shared test cases. Opt out with `Scrubber: baton.NoScrub`, or pass your own `func(any) any`. Your server's instructions and your tools' names, descriptions and schemas are not scrubbed: they are sent as you wrote them.

**It is pattern matching, not a guarantee.** `{"name": "Jane Doe", "address": "12 Elm St"}` passes through untouched, and a card number is redacted when its digits are contiguous but **not** when they are spaced or hyphenated. Decide what your server puts in tool params and results on that basis. Do not tell your users "PII is scrubbed" on the strength of it. [What it does and does not catch](https://goodtiming.ai/docs.html#pii).

## Who is calling

**Nothing is captured about the person behind a call unless you say how to find them.** Pass `ResolvePrincipal`: it runs on every tool call, listing, prompt get and resource read, receives a `baton.Caller` (the verified token, the HTTP headers, `_meta`, the tool's name and its arguments), and returns a `*baton.Principal` or `nil`.

```go
baton.Install(server, impl, opts, baton.Config{
	DSN: "https://baton_pk_...@baton.goodtiming.ai/ten_.../your-vendor",
	ResolvePrincipal: func(ctx context.Context, c baton.Caller) (*baton.Principal, error) {
		if c.Token == nil || c.Token.UserID == "" {
			return nil, nil
		}
		return &baton.Principal{ID: c.Token.UserID}, nil
	},
})
```

A token exists only on streamable HTTP behind go-sdk's `auth.RequireBearerToken`. On stdio and SSE `c.Token` and `c.Header` are nil, so name the user from whatever you authenticated them with. `ctx` ends with the request, after 5 seconds, or once Baton has the answer. If the hook returns an error, panics or takes longer than 5 seconds, that call is sent without a principal, and the call goes on. Use `ctx`: a hook that ignores it keeps running, and while 64 timed-out calls are still running in the process Baton stops calling it. For OAuth, `baton.PrincipalFromOAuthSub` and `baton.PrincipalFromOAuthEmail` read the `sub` or `email` claim from `c.Token.Extra`; pass one as `ResolvePrincipal`, or call one inside your own hook. Set `DisplayName` to choose what your dashboard shows for a person.

The id is sent as your hook returns it, so the hook decides what is safe to send. An id longer than 128 characters is cut to 128, and a blank one is not sent. A display name longer than 128 characters is not sent. To send a pseudonym instead, hash the id inside your hook and set `Hashed: true`.

If your users belong to your customers' companies, return the company too. Each event then carries it:

```go
&baton.Principal{ID: "u_123", Organization: &baton.Organization{ID: "org_acme", DisplayName: "Acme"}}
```

## Not capturing responses at all

`ResultCaptureMode: baton.ResultCaptureOff` and nothing **derived from what your tool returns** leaves your process: not the result, and not the error text, whether your handler returns it in the result or returns it as an `error`. Requests are unaffected: `params` are captured either way. Each affected event says so in its own field (`result_capture: "off"`).

**What is still sent:** the tool name, the duration, whether the call failed, and the `error_type`, which for a returned `error` is its Go type name, such as `*acme.NotFoundError`. So failure detection and timing keep working. Two messages are kept too: the message of a call to a tool that does not exist (when it is tagged `unknown_tool`, below), and a message you return as a `*baton.PublicError`, which marks it safe to send. The message of a failed listing, prompt or resource request is kept as well, and `Install` warns once about this when the mode is off. It can include text your handler returned, so return a message that names the resource, not its contents.

## Settings from the Console

*Experimental: may change in a minor release.*

`Settings: baton.SettingsConsole` reads the capture settings saved on the Console, so you can change them without a deploy, and they can differ by label, client or a share of your callers:

```go
baton.Install(server, impl, opts, baton.Config{
	DSN:      "https://baton_pk_...@baton.goodtiming.ai/ten_.../your-vendor",
	Settings: baton.SettingsConsole,
	ResolvePrincipal: func(ctx context.Context, c baton.Caller) (*baton.Principal, error) {
		return &baton.Principal{ID: c.Header.Get("X-User"), Labels: []string{"beta"}}, nil
	},
})
```

- It needs a DSN, and reads with the DSN's key. With no DSN, or with `Sink`, `VendorID` or `TenantID`, it is an error.
- Baton reads the settings once in the background when it starts, then every `SettingsRefresh`: 60 seconds when zero, and at least 15. A call never waits for a read. Until the first read succeeds, and when nothing is saved, the values in your `Config` apply. A failed or unreadable answer keeps the settings Baton holds.
- `results` applies from the next call. `goal` and `reports` apply from the next `tools/list`. With `reports` off for a caller, the report tool is left out of their tool list, but it still accepts a report.
- `ReportMode` is `baton.ReportOn` unless you set it, so the Console can turn reports off for some callers. With `ReportMode: baton.ReportOff` there is no report tool to turn on.
- The server instructions go out once per connection, so they point to the report tool while some caller can get it.
- One `New` reads once for every server it wraps. `Close` stops the reads.
- Once settings are held, each event says which settings it ran under in `capture`: the settings' version and the matched audience.
- A `tools/list` is matched before your `Config.Middleware` runs, and a tool call after it. So a label your `ResolvePrincipal` reads from what your auth put in `ctx` applies to calls, not to the tool list. To match the tool list too, read the caller from `c.Token` or `c.Header`.

**Labels** are your names for the groups a caller is in, such as `beta` or one unguessable key per customer, for the Console's settings to match. Return them in `Principal.Labels` and `Organization.Labels`. A `Principal` with labels and no `ID` sends the labels alone. They ride each event in `labels`, at most 20 of up to 64 characters each. They are sent as you return them, not scrubbed, so never put a raw id in one.

## Failures your handler never saw

A call to a tool that does not exist never reaches a handler. Baton still records it, as `tool_call_error` with `failure_kind: "unknown_tool"`. To know a tool is missing, Baton lists your tools on each call; if that listing fails, the event has no `failure_kind`.

For a tool made with `mcp.AddTool`, go-sdk checks the arguments against the input schema before your handler runs, and the output against the output schema after. Baton names those failures `invalid_argument` and `output_schema_mismatch`, read from go-sdk's own messages. Arguments that pass the schema but not the conversion to your Go type are not named.

**A call your own middleware stops with an error** before the tool runs, such as an auth or credits check, is named `refused` when you give that middleware to Baton instead of adding it yourself:

```go
handle, err := baton.Install(server, impl, opts, baton.Config{Middleware: []mcp.Middleware{auth}})
```

Baton adds it inside its own middleware, so Baton sees every call, and a call that never got past your middleware is `refused`. Under `ResultCaptureMode: baton.ResultCaptureOff` its message is withheld, unless it is a `*baton.PublicError`. A call your middleware lets through is recorded with the `ctx` it passes on, so `ResolvePrincipal` sees what your auth put there. Call `next` before your middleware returns, not in a goroutine it leaves running, and pass on that `ctx` (or one made from it) or the same request. A call your middleware sends on twice, as a retry does, is recorded once, from the first try. If your middleware changes both the `ctx` and the request, Baton loses track of the call: it is recorded without what your auth set, and a failure of your tool is sent as `refused`. Your middleware also checks calls to Baton's annotation tool. It does not see the tool listings Baton makes for itself. When `Install` adds nothing (`BATON_DISABLED`, no DSN, or an error), it still adds your middleware, so your auth always runs; do not add it again.

**Middleware you add yourself is not named.** Added with `AddReceivingMiddleware` before `Install`, a call it stops looks like your tool's own error. Added after `Install`, it runs before Baton, so a call it stops is not recorded at all.

## Turning capture off entirely

`BATON_DISABLED=1` in the environment of the process running the server, and `Install` adds nothing at all: no changed tools, no annotation tool, no added instructions, no collector connection. Nothing in the config is checked either, so the switch cannot stop your server.

The switch belongs to whoever RUNS the server: your user for a server you distribute, you for one you host. **If you pass this on to your users, say where the variable goes.** For a server started by an MCP client it belongs in that client's config file, **not in their shell**, which never reaches the process. [The long version](https://goodtiming.ai/docs.html#off-switch).

## What is not here yet

None of it blocks the quickstart above.

- No library API (SPEC §5.6): only a go-sdk `*mcp.Server` is wrapped.
- The Console's setup has no recipe yet for a server built per request.
  Follow [Servers built per request](#servers-built-per-request).

## Wire compatibility

Events from this package must be JSON-identical in shape to the Python SDK's, so a collector never branches on which SDK produced one. The tests check them against [baton-spec](https://github.com/good-timing/baton-spec), a submodule here: each event is compared with the shared vectors, and the scrubber with the shared scrub cases.

## More

| | |
|---|---|
| [goodtiming.ai/docs.html](https://goodtiming.ai/docs.html) | The docs: configuration, what is captured, the Console |
| [`docs/SPEC.md`](https://github.com/good-timing/baton/blob/main/docs/SPEC.md) | The wire protocol, in the `baton` repo. Applies here too |
| [`docs/CHARTER.md`](https://github.com/good-timing/baton/blob/main/docs/CHARTER.md) | Why the SDK is thin. Read before architectural changes |

Apache-2.0.
