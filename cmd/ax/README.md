# ax

A minimal headless LLM coding-agent harness for scripts and Unix-style
clients. One static binary, stdlib HTTP, no built-in tools.

`ax` is the engine of the [AX ecosystem](../../README.md). It runs a prompt,
streams JSONL events on stdout and exits. Every capability is an external
process registered explicitly through `AX_TOOLS`.

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
thinking = true
reasoning_effort = "high"
max_tokens = 65536
```

Every key is optional. The API key comes from `DEEPSEEK_API_KEY`, then
`OPENAI_API_KEY`, then `secrets/api-key`, then `~/.config/ax/config`.
Precedence is **flags > `bot.toml` > `~/.config/ax/config` > defaults**;
`AX_TOOLS` comes from the environment and overrides `tools`. When the bot root
has `workspace/`, the model and tools run there.

`~/.config/ax/config` is a plain `key=value` file with the same keys as the
`bot.toml` fields above (`api_key`, `model`, `base`, `tools`, `thinking`,
`effort`, `max_tokens`, `image_detail`).

## Thinking and output budget

DeepSeek runs in thinking mode by default. `ax` mirrors that: with a DeepSeek
base URL or model it sends `"thinking":{"type":"enabled"}` and
`"reasoning_effort":"high"`.

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

## Tools

`ax` has no built-in tools — register every capability explicitly:

```sh
AX_TOOLS="fsx bashx skillx" ax
```

At startup `ax` runs `<command> describe` (one JSON spec per line: name,
description, JSON-Schema parameters, and optional `sequential`, `timeout` and
`replay`). For a call it runs `<command> run <name>`, writes the JSON
arguments to stdin, reads the result from stdout and treats a non-zero exit as
a tool error. Arguments are validated against the schema before execution;
`sequential` tools run alone in call order; a call that exceeds its timeout is
cancelled with its process group. See the [tool contract](../../docs/tools.md).

Available providers: `fsx`, `bashx`, `wax`, `bqx`, `pgx`, `slackx`,
`browserx`, `skillx`, `attachx`, `mcpx`.

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
