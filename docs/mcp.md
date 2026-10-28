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

Both source kinds are **connections** ([connections.md](connections.md)): the
catalog indexes `<connection>.<tool>` and `bot.toml` declares integrations, not
tool names. `tools` and `[[mcp]]` are the legacy spellings and keep their ids.

### Local providers

`tools` in `bot.toml` and `AX_TOOLS` from the environment keep the
`describe`/`run` contract in [tools.md](tools.md). Each executable becomes a
server named after its basename (or after the `[[connection]]` id):

```text
AX_TOOLS="fsx bashx"  ->  fsx.read, fsx.write, fsx.edit, bashx.bash
```

A provider that fails to describe is shown as `unavailable`; the rest of the
catalog still loads.

### MCP servers

```toml
[[mcp]]
name = "github"
url = "https://api.githubcopilot.com/mcp/"
headers = { Authorization = "Bearer ${GITHUB_PERSONAL_ACCESS_TOKEN}" }

[[mcp]]
name = "sentry"
url = "https://mcp.sentry.dev/mcp"          # OAuth: ax mcp login sentry

[[mcp]]
name = "filesystem"
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/work"]
timeout = 30
```

- `command` starts a stdio server; `url` uses Streamable HTTP. Exactly one.
- `pass_env` passes named environment variables through. `env` sets literal
  values; `${VAR}` (and `${VAR:-default}`) expands in `env`, `headers` and
  `url`. `token_env` sets `Authorization: Bearer <value>`. Secrets stay out
  of `bot.toml`.
- `timeout` is the per-request limit in seconds (default 30, max 600).
- Names are lowercase letters, digits, `_` and `-`; they namespace tool ids
  (`github.create_issue`) and must be unique across sources.

A `[[mcp]]` table that only fails at runtime — an unset environment variable,
a missing binary — is shown as `unavailable` in the menu with the reason.
It never stops the rest of the catalog from loading.

The client speaks protocol `2026-07-28` (`server/discover`, stateless requests,
`tools/list`, `tools/call`) and falls back to the `initialize` handshake for
older servers. Resources, prompts, elicitation, sampling and subscriptions are
not implemented; a server that requires them returns a clear error.

### Authentication

A remote server with no `token_env` and no `Authorization` header uses ax's
own OAuth login. ax is a public client: it discovers the protected resource
metadata (`WWW-Authenticate: resource_metadata` or
`/.well-known/oauth-protected-resource`), registers itself dynamically when
the authorization server allows it, and runs the authorization-code flow with
PKCE and a loopback redirect on `127.0.0.1`.

```bash
ax mcp login sentry        # opens a browser, stores the session
ax mcp logout sentry
```

- Tokens live in `BOT_DATA/state/ax/mcp/auth/<server>.json`, mode `0600`, and
  are refreshed automatically; a 401 triggers one forced refresh and retry.
- Requests go out anonymously first. Only a 401 produces the hint
  `run ax mcp login <server>`, so a public endpoint works without a login.
- When the server hands ax a pre-registered client instead of dynamic
  registration, keep it in `bot.toml` without the secret:

  ```toml
  [[mcp]]
  name = "zoom"
  url = "https://mcp.zoom.us/mcp/zoom/streamable"
  client_id = "${ZOOM_CLIENT_ID}"
  client_secret_env = "ZOOM_CLIENT_SECRET"
  scopes = ["meeting:read"]
  ```

- `auth = "none"` disables OAuth for a remote server; `auth = "oauth"` is the
  default and can be spelled out.

The catalog shows a server that needs a login as
`login required: run ax mcp login <server>`, and the model sees the same line
in the menu, so it can tell the user what to run instead of retrying.

### Importing `mcp.json`

`ax mcp import` reads the shapes Cursor, Claude Desktop and VS Code write and
appends `[[mcp]]` tables to `bot.toml`:

```bash
ax mcp import ./mcp.json                     # {"mcpServers": {...}}
ax mcp import ./mcp.json --print             # show the TOML, write nothing
ax mcp import https://example.com/mcp.json
ax mcp import ./my-cursor-plugin             # .cursor-plugin/plugin.json
```

- Accepts an `mcpServers` object, an array of named servers, a `"path"` to
  another document, and VS Code's `servers` key.
- `env`, `headers` and `url` keep their `${VAR}` references; `auth`
  (`CLIENT_ID`, `CLIENT_SECRET`) becomes `client_id` and
  `client_secret_env`, never a literal secret.
- Existing names are kept unless `--force`. Skipped servers are listed with
  the reason; Cursor-hosted `api.cursor.com/rest-mcp/...` endpoints are not
  reachable outside Cursor and are skipped.

An imported plugin may declare `variables.required`; the importer prints those
names so you know which environment variables to set.

### Registry

`ax mcp add` knows the common servers, so a connection is one command:

```bash
ax mcp add                                 # list the registry
ax mcp add sentry                          # https://mcp.sentry.dev/mcp, OAuth
ax mcp add github                          # PAT header, needs GITHUB_PERSONAL_ACCESS_TOKEN
ax mcp add filesystem --arg /work          # stdio server with an argument
ax mcp add https://example.com/mcp --name example
```

The registry is the curated list in `cmd/ax/mcps.json`: GitHub, GitLab,
Sentry, Linear, Notion, Atlassian, Stripe, Vercel, Figma, Supabase, Slack,
Neon, PostHog, Todoist, HubSpot, Attio, Calendly, Gamma, Framer, Docusign,
Zoom, Greenhouse, Gusto, Square, Readwise, Mem, Hunter, Klaviyo, Typeform,
Trello, Wrike, Statsig, Shopify, Coda, Intercom, Cloudflare docs, plus the
reference stdio servers (Playwright, filesystem, memory, sequential thinking,
fetch, time, git, SQLite, Postgres). `ax mcp add` prints what is still
missing, usually an environment variable or `ax mcp login <server>`.

### CLI

| Command | Purpose |
| --- | --- |
| `ax mcp list [--json]` | Configured servers, auth kind and login state. |
| `ax mcp add [name\|url]` | Append a server to `bot.toml`. |
| `ax mcp import <path\|url\|dir>` | Import `mcp.json`, `plugin.json` or a Cursor plugin. |
| `ax mcp login <server>` | OAuth login (`--manual` pastes a callback URL instead of using a loopback port). |
| `ax mcp logout <server>` | Delete the stored token. |
| `ax mcp doctor [--server NAME]` | Connect to every server, list tool counts and failures (always bypasses the cache). |
| `ax mcp serve` | Expose `search`/`execute` over MCP stdio. |

### Cache and lifetime

Tool lists are cached at `BOT_DATA/state/ax/mcp/<server>.json`, mode `0600`,
and OAuth sessions at `BOT_DATA/state/ax/mcp/auth/<server>.json`, also `0600`;
keyed by a fingerprint of the server configuration (names only, never secret
values). The TTL is the server's `ttlMs` or 10 minutes. `search` with a warm
cache does not spawn anything. `execute` starts the owning server and keeps
the connection for the rest of the run. A server that cannot be reached and
has a cache is served stale; without a cache it is reported as unavailable.

`ax catalog --refresh` bypasses the cache, and `ax mcp doctor` always talks to
the server. The same information is available to scripts with
`ax catalog --json` and `ax mcp list --json`.

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
