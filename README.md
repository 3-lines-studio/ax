# ax

<p align="center"><img src=".github/ax.svg" width="96" height="96" alt="AX ecosystem"></p>

A minimal headless LLM coding agent harness for scripts and Unix-style clients.

Release binary: **~389 KB**. HTTP goes through system libcurl.

## Install

```sh
curl -fsSL https://ax.3lines.studio/install.sh | sh
```

Installs `ax` to `~/.local/bin` (override with `AX_PREFIX`). Linux
(x86_64, aarch64) and macOS (arm64). No Rust toolchain or root required. Every
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
cargo +nightly build --release
./target/release/ax
```

Requires nightly Rust (for `-Zbuild-std`, used to rebuild `std` with
`panic = "immediate-abort"`) with the `rust-src` component
(`rustup component add rust-src`), plus libcurl dev headers
(`libcurl4-openssl-dev` on Debian/Ubuntu).

## Testing

```sh
make harness   # release build + black-box end-to-end harness
```

`scripts/harness.py` drives the real binary the way a user would: a hermetic
HOME and XDG_CONFIG_HOME, a scripted mock OpenAI-compatible endpoint, and
one-shot CLI runs. Run one case with
`python3 scripts/harness.py --filter <name>`; list cases with `--list`.
Unit/integration tests stay in `cargo test`.

## Quick start

Headless one-shot:

```sh
OPENAI_API_KEY=... ax "explain main.go"
```

AX always emits a JSONL event stream on stdout. While AX runs, send
`{"type":"steer","text":"..."}` or `{"type":"cancel"}` as JSONL on stdin.
The stream contains assistant deltas, tool events, usage, errors, and one final
`done` event. Process hosts may provide JSON tool definitions through
`AX_HOST_TOOLS`. AX executes those tools in-process and emits a structured
`host_call` event so the host can act after AX exits without a callback.

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

Fsx and Bashx use AX's working directory. Skillx merges project and home skills. Use `-C` to select another project:

```sh
AX_TOOLS="fsx bashx skillx" ax -C /path/to/project
```

AX runs without tools when `AX_TOOLS` is unset.

## Config

`~/.config/ax/config` (or `$XDG_CONFIG_HOME/ax/config`):

```
api_key = "sk-..."        # used when OPENAI_API_KEY is unset
model = "glm-4.5"
base = "http://localhost:11434/v1"
tools = "fsx bashx skillx"    # optional: external providers
```

Plain `key = value` lines, `#` comments. `AX_TOOLS` overrides `tools`. Other precedence is flags > env > config.

## Files

Everything is files; there is no state machine.

- `~/.config/ax/commands/<name>.md` — user commands: prompt templates for
  `/name [args]`. `$ARGUMENTS` and `$@` are all args, `$1..$9` positional,
  `${2:-default}` defaults, `${@:N}` and `${@:N:L}` slices; quoted args are
  parsed.
- `~/.agents/skills/<name>/SKILL.md` — skills. The model discovers them
  through the `skills` tool and reads one with the `skill` tool.
- `~/.config/ax/SYSTEM.md` — optional system prompt, appended to the
  built-in one.
- Sessions are JSONL: one line per message or usage entry. `--session FILE`
  points at the file; there is no per-project store.

## Design

- The loop is the only logic: messages → LLM → tool calls → results →
  repeat. It lives in `run` and is shared by the SDK and headless CLI.
- `run` never mutates its input. The session file is append-only: turns are
  added to `--session FILE`, never rewritten.
- Tool calls in one batch run in parallel, results returned in call order;
  `edit` and `write` run sequentially to avoid file races.
- Retries (2×, 408/409/429/5xx + transport, with backoff) live in the
  transport layer, not the turn loop. Tool calls from a response cut off by
  the output token limit are never executed.
- Extend by composition: wrap `Provider`, pass your own `Tool` values, own
  the transcript.
- Tool errors return to the model as text so it can self-correct; only
  provider errors abort a run. You can type while the agent runs; the
  message is injected before the next assistant response.

## Library

The SDK (`src/lib.rs`) exposes the minimal core — agent loop, provider
trait, tools, OpenAI client (blocking + SSE streaming with cancellation):

```rust
let mut agent = ax::Agent::new(ax::OpenAI::new(base, key))
    .model("gpt-4.1-mini")
    .system("You are a coding agent.")
    .tools(ax::tools::external_tools("fsx bashx skillx"));

let msgs = agent.run(&[ax::Message {
    role: "user".into(),
    content: "fix the failing test".into(),
    ..Default::default()
}])?;
```

## Notes

- No permission system — tools always run. Sandboxing lives outside ax.
- `std` is rebuilt nightly (`-Zbuild-std`) with `panic = "immediate-abort"`
  and `-C force-unwind-tables=no`; panic messages and `RUST_BACKTRACE` are
  gone, and any panic aborts the process. That's most of the size story:
  prebuilt `std` alone drags in ~350 KB of panic/backtrace machinery and
  unwind tables.
- libcurl is `dlopen`'d lazily on the first request: exec and first paint
  stay fast, and the binary has no link-time dependency on curl's
  dependency tree.
- Contributing: see [CONTRIBUTING.md](CONTRIBUTING.md).
