# MCP-first catalog

`ax` is an MCP host with progressive discovery. It does not dump every tool
into the model context. It indexes all configured sources — MCP servers and
local `describe`/`run` providers — and exposes exactly two tools:

| Tool | Purpose |
| --- | --- |
| `search` | The index: browse servers, rank tools, open one schema. Never empty. |
| `execute` | Run one tool by id with validated JSON arguments. |

The model calls `search` to find the right tool, then `execute` to run it.
`core_tools` are also listed in the system prompt so common file and shell
work can call `execute` directly.

## Search

Input:

```json
{"query": "create an issue", "server": "github", "tool": "github.create_issue", "limit": 8}
```

All fields are optional.

- `{}` returns the catalog: every server, its description, tool count, sample
  tool ids and how to use the two tools.
- `{"query": "..."}` ranks tools. Each hit includes the description, tags,
  top-level arguments and a ready-to-run `execute(...)` call.
- `{"server": "github"}` lists that server's tools (with a display cap; refine with `query`).
- `{"tool": "github.create_issue"}` returns the full JSON schema, annotations
  and an example call.
- `{"query": "send", "server": "email"}` ranks inside one server.
- `{"limit": N}` caps ranked hits (1–20, default 8).

The response is Markdown, capped at 24 KiB. A query with no strong match
returns the closest names **and** the catalog menu; it never returns a bare
"no results".

## Execute

Input:

```json
{"tool": "github.create_issue", "arguments": {"repo": "3-lines-studio/ax", "title": "..."}}
```

`arguments` must be a JSON object; a JSON-encoded string is accepted for
models that double-encode tool arguments. `ax` validates it against the
tool's input schema before calling the provider. Errors are returned to the
model as text and name the tool, so the model can recover.

`execute` is sequential: two calls never run at the same time. The catalog
also tracks `readOnlyHint`, `destructiveHint`, `idempotentHint` and
`openWorldHint` from MCP, and `read_only`, `destructive`, `open_world` from
local descriptors.

## Sources

### Local providers

`tools` in `bot.toml` and `AX_TOOLS` from the environment keep the
`describe`/`run` contract in [tools.md](tools.md). Each executable becomes a
server named after its basename:

```text
AX_TOOLS="fsx bashx"  ->  fsx.read, fsx.write, fsx.edit, bashx.bash
```

A provider that fails to describe is shown as `unavailable`; the rest of the
catalog still loads.

### MCP servers

```toml
[[mcp]]
name = "github"
command = "npx"
args = ["-y", "@modelcontextprotocol/server-github"]
pass_env = ["GITHUB_TOKEN"]
timeout = 30

[[mcp]]
name = "linear"
url = "https://mcp.linear.app/mcp"
token_env = "LINEAR_TOKEN"
```

- `command` starts a stdio server; `url` uses Streamable HTTP. Exactly one.
- `pass_env` passes named environment variables through. `env` sets literal
  values; `${VAR}` expands in `env` and `headers`. `token_env` sets
  `Authorization: Bearer <value>`. Secrets stay out of `bot.toml`.
- `timeout` is the per-request limit in seconds (default 30, max 600).
- Names are lowercase letters, digits, `_` and `-`; they namespace tool ids
  (`github.create_issue`) and must be unique across sources.

The client speaks protocol `2026-07-28` (`server/discover`, stateless requests,
`tools/list`, `tools/call`) and falls back to the `initialize` handshake for
older servers. It does not implement OAuth, elicitation, sampling, resources,
prompts or subscriptions; a server that requires them returns a clear error.

### Cache and lifetime

Tool lists are cached at `BOT_DATA/state/ax/mcp/<server>.json`, mode `0600`,
keyed by a fingerprint of the server configuration (names only, never secret
values). The TTL is the server's `ttlMs` or 10 minutes. `search` with a warm
cache does not spawn anything. `execute` starts the owning server and keeps
the connection for the rest of the run. A server that cannot be reached and
has a cache is served stale; without a cache it is reported as unavailable.

`ax catalog --refresh` bypasses the cache. The same information is available
to scripts with `ax catalog --json`.

## `ax mcp serve`

`ax mcp serve` exposes `search` and `execute` over MCP stdio, using the same
catalog. Any MCP host — Claude, Cursor, Kody — can use every configured AX
integration through two stable tools. `--http` is not implemented yet.

## Durability

`execute` is the only catalog tool with side effects, which makes the session
log precise: `tool_intent` records the resolved `target` (for example
`fsx.write`) and the arguments, and a crash before the result is recorded is
reported to the model as an interrupted call **with that target**. `search` is
read-only and is not part of the recovery decision.

## Direct mode

`ax -tool-mode direct` exposes every local provider tool with its own schema,
the pre-catalog behavior; `[[mcp]]` servers are ignored. It exists for one
release so bots and scripts can migrate; `catalog` is the default.
