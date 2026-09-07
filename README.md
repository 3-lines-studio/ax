# ax

<p align="center"><img src=".github/ax.svg" width="96" height="96" alt="AX ecosystem"></p>

A minimal headless LLM coding agent harness for scripts and Unix-style clients.

Release binary: **~7 MB**. HTTP uses Go's stdlib `net/http`; no libcurl.

## Install

```sh
curl -fsSL https://ax.3lines.studio/install.sh | sh
```

Installs `ax` to `~/.local/bin` (override with `AX_PREFIX`). Linux
(x86_64, aarch64) and macOS (arm64). No Go toolchain or root required. Every
download is verified with SHA-256. Install any ecosystem CLI by repository
name without changing the installer:

```sh
curl -fsSL https://ax.3lines.studio/install.sh | sh -s -- wax
curl -fsSL https://ax.3lines.studio/install.sh | sh -s -- wax bqx pgx
```

Pin core versions with `AX_VERSION`. Use `VERSION` when
installing one optional CLI. Update installed core tools
with `sh install.sh update`; up-to-date packages are skipped. Uninstall by
removing its file from `~/.local/bin`.

## Build

```sh
go build -trimpath -ldflags='-s -w' -o ax .
```

## Testing

```sh
make harness   # build + black-box end-to-end harness
```

`scripts/harness.py` drives the real binary the way a user would: a hermetic
HOME and XDG_CONFIG_HOME, a scripted mock OpenAI-compatible endpoint, and
one-shot CLI runs. Run one case with
`python3 scripts/harness.py --filter <name>`; list cases with `--list`.
Unit/integration tests stay in `go test`.

## Quick start

Headless one-shot:

```sh
OPENAI_API_KEY=... ax "explain main.go"
```

AX always emits a JSONL event stream on stdout. While AX runs, send
`{"type":"cancel"}` as JSONL on stdin to stop it. The stream contains
assistant deltas, tool events, usage, errors, and one final `done` event.

Persist one-shot calls in an explicit session file:

```sh
ax --session ./thread.jsonl "continue fixing the test"
```

AX loads the file, appends the turn, then emits the event stream. Reusing the
path continues the same session. The `--events` flag is accepted as a no-op.

Any OpenAI-compatible endpoint works: `-base https://...` for OpenRouter,
DeepSeek, Ollama, vLLM.

Set `AX_TOOLS` to a space-separated list of external tool commands:

```sh
AX_TOOLS="bqx pgx" ax
```

AX runs `<command> describe` at startup. The command prints one JSON tool
specification per line with `name`, `description`, and JSON Schema `parameters`.
For a tool call, AX runs `<command> run <name>`, writes the JSON arguments to
stdin, reads the result from stdout, and treats a non-zero exit as a tool error. See the [Unix tool convention](docs/tools.md) for the complete contract.

AX has no built-in tools. Register every capability explicitly:

```sh
AX_TOOLS="fsx bashx skillx" ax
```

Fsx and Bashx use AX's working directory. Skillx reads the bot's `skills/` (and home skills). In a Botdir bot the working directory is `workspace/`. Use `-C` to select another bot root:

```sh
AX_TOOLS="fsx bashx skillx" ax -C /path/to/project
```

AX runs without tools when `AX_TOOLS` is unset.

## Config

ax is a Botdir consumer. The bot root is the current directory or `-C DIR`. It reads `bot.toml` at the bot root:

```toml
model = "glm-4.5"
base_url = "http://localhost:11434/v1"
tools = ["fsx", "bashx", "skillx"]    # optional: external providers
```

The standard Botdir keys are `model`, `base_url`, and `tools`. Unknown keys and tables (e.g. `[slack]`) are ignored. `AX_TOOLS` overrides `tools`. Precedence is flags > env > `bot.toml` > the legacy `~/.config/ax/config` > defaults.

The legacy `~/.config/ax/config` (or `$XDG_CONFIG_HOME/ax/config`) is still accepted:

```
api_key = "sk-..."
model = "glm-4.5"
base = "http://localhost:11434/v1"
tools = "fsx bashx skillx"
```

The model API key comes from `OPENAI_API_KEY` or `secrets/api-key`; bot instructions come from `bot.md`. When the bot root is a Botdir bot (has `workspace/`), the model's working directory is `workspace/` and tool providers run there; otherwise it is the bot root.

## Files

Everything is files; there is no state machine.

- `~/.config/ax/commands/<name>.md` — user commands: prompt templates for
  `/name [args]`. `$ARGUMENTS` and `$@` are all args, `$1..$9` positional,
  `${2:-default}` defaults, `${@:N}` and `${@:N:L}` slices; quoted args are
  parsed.
- bot `skills/<name>/SKILL.md` (and `$HOME/.agents/skills` for shared skills) —
  skills, exposed through the `skills` and `skill` tools provided by Skillx.
- `~/.config/ax/SYSTEM.md` — optional user system prompt, appended to the
  built-in one after the bot's `bot.md`.
- Sessions are JSONL: one line per message or usage entry. `--session FILE`
  points at the file; there is no per-project store. A Botdir host routes
  session files under `state/ax/`.

## Design

- The loop is the only logic: messages → LLM → tool calls → results →
  repeat. It lives in `runStream` and is shared by the headless CLI and the
  SDK-style agent sink.
- The run never mutates its input. The session file is append-only: turns are
  added to `--session FILE`, never rewritten.
- Tool calls in one batch run in parallel, results returned in call order;
  `edit` and `write` run sequentially to avoid file races.
- Retries (2×, 408/409/429/5xx + transport, with backoff) live in the
  transport layer, not the turn loop. Tool calls from a response cut off by
  the output token limit are never executed.
- Tool errors return to the model as text so it can self-correct; only
  provider errors abort a run. You can type while the agent runs; the
  message is injected before the next assistant response.
- ax uses Go's stdlib `net/http` for streaming OpenAI-compatible responses
  and `os/exec` (with per-child process groups and SIGTERM reaping) for
  external tool providers.

## Notes

- No permission system — tools always run. Sandboxing lives outside ax.
- Contributions and internals: see [CONTRIBUTING.md](CONTRIBUTING.md).
