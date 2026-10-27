# mcpx

MCP (Model Context Protocol) tool provider for `ax`.

```text
ax --tools "... mcpx ..."  -->  mcpx describe / mcpx run <name>  -->  an MCP server
```

`ax`'s tools are external Unix-convention commands (`<command> describe` /
`<command> run <name>`), not MCP. `mcpx` is the bridge: it speaks MCP as a
client to whatever server(s) you point it at, and exposes their tools to
`ax` in `ax`'s own convention.

**Standalone**: `mcpx` has no dependency on `acpi`, ACP, or Buzz. Set
`MCPX_SERVERS_FILE` and add `mcpx` to `AX_TOOLS` — that's the whole contract.
(`acpi` happens to write that file itself when an ACP client hands it MCP
servers via `session/new`, since that's the shape ax's `AX_TOOLS` needs
either way, but nothing about `mcpx` requires `acpi` to be involved.)

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- mcpx
```

## Configure

`MCPX_SERVERS_FILE` points at a JSON array of stdio MCP servers:

```json
[
  {
    "name": "buzz-cli",
    "command": "/usr/local/bin/buzz-cli",
    "args": [],
    "env": [{"name": "BUZZ_API_TOKEN", "value": "..."}]
  }
]
```

This is the same shape as the wire format of ACP's `McpServer` (stdio
variant) — verified against `acpi`'s actual output using the real
`coder/acp-go-sdk`, not just by matching two hand-written fixtures. `type`
may be omitted (stdio is the only transport v1 supports; any other value is
rejected with a clear error).

```sh
export AX_TOOLS="wax bqx pgx fsx bashx skillx mcpx"
export MCPX_SERVERS_FILE=/path/to/servers.json
ax
```

## Tool naming

Every tool is exposed to `ax` as `<server>__<tool>`, e.g. `buzz-cli__send_message`,
even when only one server is configured. That keeps the tool namespace's
shape stable: adding a second server later doesn't rename anything that
already worked.

## Known limitations (v1)

- **No persistent connection.** `ax` execs a fresh `mcpx describe` at
  startup and a fresh `mcpx run <name>` per tool call — no daemon, no shared
  process. So every single invocation (including every `run`) spawns its
  target MCP server, does the MCP handshake, and tears it down again. This
  is the only design that keeps `mcpx` genuinely standalone (no separate
  process to own a persistent connection), but it means N tool calls in one
  turn respawn the underlying server N times. Fine for something like
  `buzz-cli`; worth revisiting (e.g. a local keep-alive daemon `mcpx` talks
  to instead) if that overhead becomes real for a heavier MCP server.
- **No pagination.** `tools/list`'s `NextCursor` isn't followed; only the
  first page is used. Fine for small, fixed tool catalogs.
- **Text content only.** `CallToolResult.Content` is flattened to its
  `TextContent` items; image/audio/resource content types aren't handled.
  Falls back to JSON-encoding `StructuredContent` when there's no text
  content at all.

## Test

```sh
go test ./cmd/mcpx
```

`mcpx_test.go` drives the real `mcpx` binary against a real (fixture) MCP
server (`testdata/fake-mcp-server`, built with the same
`modelcontextprotocol/go-sdk` `mcpx` itself uses as a client) — covers
`describe`'s tool-qualification and schema pass-through, `run`'s success and
`IsError` paths, and the unconfigured-`MCPX_SERVERS_FILE` no-op case.
