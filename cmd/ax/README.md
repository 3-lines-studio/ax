# ax

A minimal headless LLM coding-agent harness for scripts and Unix-style
clients. One static binary, stdlib HTTP, no built-in tools.

`ax` is the engine of the [AX ecosystem](../../README.md). It runs a prompt,
streams JSONL events on stdout and exits. Every capability is an external
process registered explicitly through `AX_TOOLS`.

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
OPENAI_API_KEY=... ax "explain main.go"
```

`ax` is a [Botdir](../../docs/botdir/BOTDIR.md) consumer. The bot root is the
current directory (or `-C DIR`); it reads `bot.toml` there:

```toml
model = "gpt-4.1-mini"
base_url = "https://api.openai.com/v1"
tools = ["fsx", "bashx", "skillx"]
```

Any OpenAI-compatible endpoint works (`-base https://...`: OpenRouter,
DeepSeek, Ollama, vLLM). The model key comes from `OPENAI_API_KEY` or
`secrets/api-key`. Precedence is **flags > `bot.toml` > `~/.config/ax/config` >
defaults**; `OPENAI_API_KEY` and `AX_TOOLS` come from the environment. When the
bot root has `workspace/`, the model and tools run there.

## Tools

`ax` has no built-in tools — register every capability explicitly:

```sh
AX_TOOLS="fsx bashx skillx" ax
```

At startup `ax` runs `<command> describe` (one JSON spec per line: name,
description, JSON-Schema parameters). For a call it runs `<command> run
<name>`, writes the JSON arguments to stdin, reads the result from stdout and
treats a non-zero exit as a tool error. See the [tool
contract](../../docs/tools.md).

Available providers: `fsx`, `bashx`, `wax`, `bqx`, `pgx`, `slackx`,
`browserx`, `skillx`, `attachx`, `mcpx`.

## Session

`ax` emits a JSONL event stream on stdout: assistant deltas, tool events,
usage, errors, and one final `done`. Send `{"type":"cancel"}` on stdin to stop.
Persist a run with `--session FILE` (JSONL, append-only; a Botdir host routes
them under `state/ax/`). See the [event protocol](../../docs/protocol.md).

## Build and test

From the repository root:

```sh
make bin/ax
go test ./cmd/ax
make harness        # black-box end-to-end harness (scripts/harness.py)
```

A real-provider eval lives in `scripts/eval.py`:

```sh
# set OPENAI_API_KEY, AX_BASE_URL, and AX_MODEL, then:
make eval
```

It drives the real binary against a real provider through a real Botdir bot
root and the real tool providers, asserting on observable artifacts. It skips
cleanly until the three variables are set.

See [CONTRIBUTING.md](../../CONTRIBUTING.md) for internals.
