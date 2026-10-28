# ax

A minimal headless LLM coding-agent harness for scripts and Unix-style
clients. One static binary, stdlib HTTP, no built-in tools.

`ax` is the engine of the [AX ecosystem](../../README.md). It runs a prompt,
streams JSONL events on stdout and exits. Capabilities come from a catalog:
MCP servers plus local describe/run providers registered through `AX_TOOLS`.
The model sees two tools, `search` and `execute`, instead of a tool list.

The default provider is DeepSeek: `https://api.deepseek.com` with
`deepseek-flash`. Any OpenAI-compatible endpoint works with `-base` (OpenAI,
OpenRouter, Ollama, vLLM).

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh
```

Or from the repository root:

```sh
make bin/ax
```

## Run

```sh
DEEPSEEK_API_KEY=... ax "explain main.go"
```

`ax` is a [Botdir](../../docs/botdir/BOTDIR.md) consumer. The bot root is the
current directory (or `-C DIR`); it reads `bot.toml` there:

```toml
model = "deepseek-flash"
base_url = "https://api.deepseek.com"
tools = ["fsx", "bashx", "skillx"]
core_tools = ["fsx.read", "fsx.write", "fsx.edit", "bashx.bash"]
thinking = true
reasoning_effort = "max"
max_tokens = 65536
compact_at = 0.5
context_window = 1000000

[[mcp]]
name = "github"
command = "npx"
args = ["-y", "@modelcontextprotocol/server-github"]
pass_env = ["GITHUB_TOKEN"]
```

Every key is optional. The API key comes from `DEEPSEEK_API_KEY`, then
`OPENAI_API_KEY`, then `secrets/api-key`, then `~/.config/ax/config`.
Precedence is **flags > `bot.toml` > `~/.config/ax/config` > defaults**;
`AX_TOOLS` comes from the environment and overrides `tools`. When the bot root
has `workspace/`, the model and tools run there.

`~/.config/ax/config` is a plain `key=value` file with the same keys as the
`bot.toml` fields above (`api_key`, `model`, `base`, `tools`, `thinking`,
`effort`, `max_tokens`, `image_detail`, `tool_mode`, `core_tools`, `compact_at`,
`context_window`).

## Context compaction

Long sessions compact themselves. When the projected prompt reaches
`compact_at` of the context window (defaults `0.5` and `1000000`, the same
numbers pixi used), AX summarizes the older messages with the model, appends one
`compaction` entry to the session log and puts the summary at the top of the
system prompt. The log stays append-only, so resuming works exactly as before.

```sh
ax -compact-at 0.7 "keep going"       # later threshold
ax -compact-at 0 "keep going"         # disable
ax -context-window 128000 "..."       # smaller window, earlier compaction
ax compact --session thread.jsonl     # the "Compact context" button
```

A compaction is visible in the event stream as
`{"type":"compaction","upto":N,"summary":"..."}`. A mid-run compaction also adds
a `Continue the task...` user turn, so the model keeps something to act on; a
compaction that cannot run (provider error) fails the run instead of silently
growing the prompt.

## Thinking and output budget

DeepSeek runs in thinking mode by default. `ax` mirrors that: with a DeepSeek
base URL or model it sends `"thinking":{"type":"enabled"}` and
`"reasoning_effort":"max"`. That is the highest thinking budget; choose `-effort
low` or `high` when latency matters more than depth.

```sh
ax -thinking off "summarize this changelog"   # auto, on, off
ax -effort max "design the migration"         # low, high, max
ax -effort none "quick answer"                # none disables thinking
ax -max-tokens 32768 "long answer"            # DeepSeek allows up to 393216
```

`-thinking auto` (the default) enables thinking for DeepSeek and leaves the
field out for other providers. Explicit `on`/`off` is always sent. Reasoning
streams as `reasoning_delta` events and is stored with the assistant message,
because DeepSeek requires `reasoning_content` to be echoed back when tools are
present. A session written before reasoning was recorded falls back to
non-thinking for that request instead of failing the history.

## Images

The model accepts vision input. `-image` is repeatable:

```sh
ax -image screenshot.png "what is broken in this UI?"
ax -image https://example.com/chart.png -image-detail original "read the chart"
```

`-image` takes a local path or an http(s) URL. Local files may be JPEG, PNG,
GIF or WebP and are limited to 32 MiB inline. `-image-detail` is `low`,
`original` or `auto` (default; `high` is accepted for OpenAI-compatible
endpoints). With `--session`, the image is stored as a content-addressed blob
next to the log and only the digest is recorded; without a session it is
inlined as a data URL.

## Tool catalog

`ax` has no built-in tools. Everything is a catalog source, and the unit of
configuration is a **connection** ([docs/connections.md](../../docs/connections.md)):

```toml
[[connection]]
id      = "web"        # the model calls web.search / web.fetch
kind    = "local"       # local | mcp-stdio | mcp-http
command = "wax"
```

A connection declares an integration; its tools are discovered and indexed.
`capabilities` narrows the list when a bot wants only part of a provider, and
`trust = "read"` keeps only the tools annotated read-only. `ax conn list` shows
what is configured, `ax conn doctor` connects and reports declared vs enabled. The
older spellings still work and keep their ids: `tools`/`AX_TOOLS` become
`kind = "local"` and `[[mcp]]` becomes `kind = "mcp-*"`.

The model always sees two tools:

- `search` is the index. An empty call returns the whole catalog (servers,
  descriptions, tool ids, how to call them). A query ranks matches and always
  includes a fallback menu, so a bad query never returns nothing. Use
  `{"server":"github"}` for one server or `{"tool":"github.create_issue"}` for
  the full schema and an example call.
- `execute` runs one tool by id with a JSON object of arguments. It validates
  arguments against the tool schema before calling anything.

```sh
AX_TOOLS="fsx bashx skillx" ax "read main.go and add the missing test"
ax catalog                     # the same index, from the shell
ax catalog --json              # machine-readable
ax catalog --refresh           # bypass the MCP catalog cache
```

`core_tools` (default: `fsx.read`, `fsx.write`, `fsx.edit`, `bashx.bash`)
appear as a compact cheat sheet in the system prompt, so common file and shell
work can call `execute` directly without paying for a `search` turn.

### MCP servers

```toml
[[mcp]]
name = "github"
command = "npx"                         # stdio server
args = ["-y", "@modelcontextprotocol/server-github"]
pass_env = ["GITHUB_TOKEN"]             # names only; values come from the environment
timeout = 30

[[mcp]]
name = "sentry"
url = "https://mcp.sentry.dev/mcp"      # Streamable HTTP + OAuth
```

- `pass_env` passes environment variables through; `env = { LOG_LEVEL = "debug" }`
  sets literal values; `${VAR}` expands in `env`, `headers` and `url`.
- Secrets never go in `bot.toml`: use `pass_env`, `token_env` or the OAuth
  login. `token_env` sets `Authorization: Bearer <value>`.
- Remote servers without a token use ax's OAuth login; tokens live in
  `state/ax/mcp/auth/<server>.json` (mode `0600`) and refresh by themselves.
- A server that fails to start is shown as `unavailable` in the catalog; it
  does not take down the other integrations.
- Tool lists are cached under `state/ax/mcp/<server>.json` for 10 minutes (or
  the server's `ttlMs`) and refreshed with `ax catalog --refresh`.
- No daemon: `search` uses the cache, `execute` starts the owning server and
  keeps it only for the run.

Connect a common server in one command, import what Cursor or Claude already
configured, or debug what is broken:

```sh
ax mcp add                                 # list the curated registry
ax mcp add sentry                          # OAuth server -> then: ax mcp login sentry
ax mcp add github                          # PAT header: GITHUB_PERSONAL_ACCESS_TOKEN
ax mcp add filesystem --arg /work          # stdio server with an argument
ax mcp import ./mcp.json                   # {"mcpServers": {...}}
ax mcp import ./my-cursor-plugin           # .cursor-plugin/plugin.json
ax mcp list                                # auth kind and login state
ax mcp login sentry                        # browser authorization, PKCE
ax mcp doctor                              # connect to everything, report failures
```

`ax mcp serve` exposes the same `search`/`execute` catalog to any MCP host over
stdio:

```sh
ax mcp serve
```

See the [MCP catalog contract](../../docs/mcp.md) for the full behavior and
current limits (no elicitation, resources or prompts yet).

### Direct mode

The old behavior is still available for one release:

```sh
ax -tool-mode direct "use the bash tool"
```

In direct mode every local provider tool is exposed to the model with its own
schema; `[[mcp]]` servers are ignored. Catalog mode is the default.

Available local providers: `fsx`, `bashx`, `wax`, `bqx`, `pgx`, `slackx`,
`browserx`, `skillx`, `attachx`.

## Session

`ax` emits a JSONL event stream on stdout: reasoning and assistant deltas,
tool events, usage, errors, and one final `done`. Send `{"type":"cancel"}` on
stdin to stop. See the [event protocol](../../docs/protocol.md).

`--session FILE` makes the run durable and resumable:

```sh
ax --session /tmp/work.jsonl "read main.go"
ax --session /tmp/work.jsonl "now add the missing test"
```

The log is append-only JSONL, fsynced per entry and locked while a run holds
it. A run that lost its `run_end` is repaired on the next start: unfinished
assistant turns are dropped and tool calls whose result was never recorded are
reported to the model as interrupted instead of being re-run. Image blobs live
in `FILE.blobs/`. Botdir hosts route these logs under their own namespace
(e.g. `state/ax/sessions/`). See [the session contract](../../docs/session.md).

## Build and test

From the repository root:

```sh
make bin/ax
go test ./cmd/ax
make harness        # black-box end-to-end harness (scripts/harness.py)
```

A real-provider eval lives in `scripts/eval.py`:

```sh
# set DEEPSEEK_API_KEY, AX_BASE_URL, and AX_MODEL, then:
make eval
```

It drives the real binary against a real provider through a real Botdir bot
root and the real tool providers, asserting on observable artifacts. It skips
cleanly until the three variables are set.

See [CONTRIBUTING.md](../../CONTRIBUTING.md) for internals.
