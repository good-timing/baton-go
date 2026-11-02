# baton-go

**Pre-1.0.** The public API is not yet stable.

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
	server := mcp.NewServer(&mcp.Implementation{Name: "your-vendor-mcp", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "lookup"}, lookup)

	handle, err := baton.Install(server, baton.Config{
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

Copy the DSN from **/account**. It packs four values: the collector to send to, your workspace, this server, and the key that binds them. The SDK unpacks them and sends to the collector itself. Tools added before and after `Install` are both captured.

**Installing changes what your server advertises.** One sentence is added below your server's own instructions, and a `user_goal` parameter is added to every tool's schema and stripped again before your handler sees it. With `ReportMode: "on"`, an annotation tool named after your server (slugged, so a server called "Acme Knowledge Base" gets `acme-knowledge-base_annotate`) is also served, and the added text tells the agent when to report a problem with it. [What gets injected](https://goodtiming.ai/docs.html#injected-tools).

**If you distribute your server, put the DSN in your source.** A stdio server runs on your user's machine, spawned by their MCP client, which passes it a fixed allowlist of environment variables (six names on macOS and Linux) plus whatever that user wrote in their own client config. Nothing from your `.env` is in either list. For a hosted server, set `BATON_DSN` and pass `baton.Config{}`. An explicit `DSN` wins over `BATON_DSN`.

**With no DSN, Baton installs nothing.** `Install` logs one warning and returns a handle whose `Close` does nothing. Your server starts as usual. A config value that is not valid, such as `ResultCapture: "none"`, is still an error.

**Call `Close` when your server stops.** Events are sent in the background, so a slow or broken collector never slows a tool call. `Close(ctx)` sends what is still buffered and returns when it is done. It stops trying at `ctx`'s deadline, or after 3 seconds if `ctx` has none. It never returns an error. Events recorded after `Close` are dropped.

## Stateless HTTP

go-sdk's stateless mode is supported. Build the server once, call `Install` once, and hand that server to the handler:

```go
handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
	&mcp.StreamableHTTPOptions{Stateless: true})
```

**Do not call `Install` per request.** Each `Install` starts its own sender, which runs until its `Close`, so a server built and installed per request leaks one per request.

When the client sends no `Mcp-Session-Id`, each request is its own session. Signals that need several calls in one session, such as a retry loop, do not fire across requests. To see one person's requests together, return a principal from `ResolvePrincipal` ([Who is calling](#who-is-calling)).

## What leaves your server

Only what crosses the MCP call. The SDK never reads your logs, your database or anything else in your process.

| Sent | What |
|---|---|
| Always | The tool's name, its arguments (`params`, scrubbed), `user_goal`, how long the call took, and on failure the `error_type`. The request's `_meta`, as `runtime_meta` (scrubbed, with any latitude and longitude rounded). What the client declares about itself, such as its name and version, and on HTTP its `User-Agent` and `X-Anthropic-Client` headers. Once per change: your server's name, version, capabilities and instructions, and each tool's name, description and schemas. Who the caller is and their company, only if you set `ResolvePrincipal`. For a prompt or a resource: its name and arguments (scrubbed), or its URI, how long the request took, and on failure the error's type and message. How many tools, prompts or resources a listing returned. Never a prompt's messages or a resource's contents. |
| Only with `ResultCapture: "full"` (the default) | What the tool returned (`result`), including any error text in it, and the message of an `error` your handler returned. |
| With `ReportMode: "on"` | The reports the agent files, in its own words. |
| Never | Anything that did not cross the MCP call. |

## PII scrubbing

**On by default.** `baton.DefaultScrubber` walks the arguments, results, error text, `_meta` and agent reports, and redacts email / `Bearer …` / `sk-*` / `AKIA*` / JWT / Luhn-validated card / NA-format phone, plus a list of sensitive field names. It is the same ruleset as the Python SDK, checked against the same shared test cases. Opt out with `Scrubber: baton.NoScrub`, or pass your own `func(any) any`. Your server's instructions and your tools' names, descriptions and schemas are not scrubbed: they are sent as you wrote them.

**It is pattern matching, not a guarantee.** `{"name": "Jane Doe", "address": "12 Elm St"}` passes through untouched, and a card number is redacted when its digits are contiguous but **not** when they are spaced or hyphenated. Decide what your server puts in tool params and results on that basis. Do not tell your users "PII is scrubbed" on the strength of it. [What it does and does not catch](https://goodtiming.ai/docs.html#pii).

## Who is calling

**Nothing is captured about the person behind a call unless you say how to find them.** Pass `ResolvePrincipal`: it runs on every tool call, receives the request's verified token, and returns a `*baton.Principal` or `nil`.

```go
baton.Install(server, baton.Config{
	DSN: "https://baton_pk_...@baton.goodtiming.ai/ten_.../your-vendor",
	ResolvePrincipal: func(ctx context.Context, token *auth.TokenInfo) (*baton.Principal, error) {
		if token == nil || token.UserID == "" {
			return nil, nil
		}
		return &baton.Principal{ID: token.UserID}, nil
	},
})
```

`auth` is `github.com/modelcontextprotocol/go-sdk/auth`. A token exists only on streamable HTTP behind go-sdk's `auth.RequireBearerToken`. On stdio `token` is nil, so name the user from whatever you authenticated them with. If the hook returns an error or panics, that call is sent without a principal. Set `DisplayName` to choose what your dashboard shows for a person.

The id is sent as your hook returns it, so the hook decides what is safe to send. An id longer than 128 characters is cut to 128, and a blank one is not sent. A display name longer than 128 characters is not sent. To send a pseudonym instead, hash the id inside your hook and set `Hashed: true`.

If your users belong to your customers' companies, return the company too, so you can filter by it:

```go
&baton.Principal{ID: "u_123", Organization: &baton.Organization{ID: "org_acme", DisplayName: "Acme"}}
```

## Not capturing responses at all

`ResultCapture: "off"` and nothing **derived from what your tool returns** leaves your process: not the result, and not the error text, whether your handler returns it in the result or returns it as an `error`. Requests are unaffected: `params` are captured either way. Each affected event says so in its own field (`result_capture: "off"`).

**What is still sent:** the tool name, the duration, whether the call failed, and the `error_type`, which for a returned `error` is its Go type name, such as `*acme.NotFoundError`. So failure detection and timing keep working. Two messages are kept too: the message of a call to a tool that does not exist (when it is tagged `unknown_tool`, below), and a message you return as a `*baton.PublicError`, which marks it safe to send. The message of a failed listing, prompt or resource request is kept as well, and `Install` warns once about this when the mode is off. It can include text your handler returned, so return a message that names the resource, not its contents.

## Failures your handler never saw

A call to a tool that does not exist never reaches a handler. Baton still records it, as `tool_call_error` with `failure_kind: "unknown_tool"`. To know a tool is missing, Baton lists your tools on each call; if that listing fails, the event has no `failure_kind`.

For a tool made with `mcp.AddTool`, go-sdk checks the arguments against the input schema before your handler runs, and the output against the output schema after. Baton names those failures `invalid_argument` and `output_schema_mismatch`, read from go-sdk's own messages. Two failures are not named: arguments that pass the schema but not the conversion to your Go type, and a call your own middleware turns away before the tool runs (`refused` in the other SDKs), which Baton cannot see.

## Turning capture off entirely

`BATON_DISABLED=1` in the environment of the process running the server, and `Install` adds nothing at all: no changed tools, no annotation tool, no added instructions, no collector connection. Nothing in the config is checked either, so the switch cannot stop your server.

The switch belongs to whoever RUNS the server: your user for a server you distribute, you for one you host. **If you pass this on to your users, say where the variable goes.** For a server started by an MCP client it belongs in that client's config file, **not in their shell**, which never reaches the process. [The long version](https://goodtiming.ai/docs.html#off-switch).

## What is not here yet

None of it blocks the quickstart above.

- No library API: only a go-sdk `*mcp.Server` is wrapped.
- Only tools. Resources, prompts and `tools/list` requests emit no events.
- No local mode, and no file or stderr output: events go to the collector the DSN names.

## Wire compatibility

Events from this package must be JSON-identical in shape to the Python SDK's, so a collector never branches on which SDK produced one. The tests check them against [baton-spec](https://github.com/good-timing/baton-spec), a submodule here: each event is compared with the shared vectors, and the scrubber with the shared scrub cases.

## More

| | |
|---|---|
| [goodtiming.ai/docs.html](https://goodtiming.ai/docs.html) | The docs: configuration, what is captured, the Console |
| [`docs/SPEC.md`](https://github.com/good-timing/baton/blob/main/docs/SPEC.md) | The wire protocol, in the `baton` repo. Applies here too |
| [`docs/CHARTER.md`](https://github.com/good-timing/baton/blob/main/docs/CHARTER.md) | Why the SDK is thin. Read before architectural changes |

Apache-2.0.
