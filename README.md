# ax

A minimal headless LLM coding agent harness for scripts and Unix-style clients.
Use `axi` for the interactive terminal interface.

Release binary: **~389 KB**. HTTP goes through system libcurl.

## Install

```sh
curl -fsSL https://ax.3lines.studio/install | sh
```

Installs `ax` and `axi` to `~/.local/bin` (override with `AX_PREFIX`). Linux
(x86_64, aarch64) and macOS (arm64). No Rust toolchain or root required. Every
download is verified with SHA-256. Install any ecosystem CLI by repository
name without changing the installer:

```sh
curl -fsSL https://ax.3lines.studio/install | sh -s -- wax
curl -fsSL https://ax.3lines.studio/install | sh -s -- wax bqx pgx
```

Pin core versions with `AX_VERSION` and `AXI_VERSION`. Use `VERSION` when
installing one optional CLI. Uninstall by removing its file from
`~/.local/bin`.

## Build

```sh
cargo +nightly build --release --config 'build.rustflags="-C force-unwind-tables=no"'
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

Interactive terminal:

```sh
OPENAI_API_KEY=... axi
```

Headless one-shot:

```sh
OPENAI_API_KEY=... ax "explain main.go"
```

Persist one-shot calls in an explicit session file:

```sh
ax --session ./thread.jsonl "continue fixing the test"
```

AX loads the file, appends the turn, prints the final answer to stdout, and
writes status to stderr. Reusing the path continues the same session.

Use `--events` for a JSONL stream on stdout. While AX runs, send
`{"type":"steer","text":"..."}` or `{"type":"cancel"}` as JSONL on stdin.
The stream contains assistant deltas, tool events, usage, errors, and one final
`done` event.

Axi provides the transcript TUI, streamed Markdown, tool status, commands,
file completion, steering, and session screens while running AX as a sidecar.

Any OpenAI-compatible endpoint works: `-base https://...` for OpenRouter,
DeepSeek, Ollama, vLLM.

Set `AX_TOOLS` to a space-separated list of external tool commands:

```sh
AX_TOOLS="bqx pgx" ax
```

AX runs `<command> ax-tools` at startup. The command prints one JSON tool
specification per line with `name`, `description`, and JSON Schema `parameters`.
For a tool call, AX runs `<command> ax-run <name>`, writes the JSON arguments to
stdin, reads the result from stdout, and treats a non-zero exit as a tool error.

## Config

`~/.config/ax/config` (or `$XDG_CONFIG_HOME/ax/config`):

```
api_key = "sk-..."        # used when OPENAI_API_KEY is unset
model = "glm-4.5"
base = "http://localhost:11434/v1"
context_window = 1000000       # optional: model context size
compaction_threshold = 250000  # optional: compact before the model limit
```

Plain `key = value` lines, `#` comments. Precedence: flags > env > config.
Run `/login` in Axi to write these interactively. Set `context_window`
to enable automatic compaction 16,384 tokens before the model limit. Set
`compaction_threshold` to compact at a lower token count. AX uses the provider's
reported context usage. During tool-driven work, AX finishes the current model
response and its full tool batch, compacts, then resumes the same run. Without
usage data or either setting, compaction runs only after an explicit
context-overflow error.

## Files

Everything is files; there is no state machine.

- `~/.config/ax/projects/<cwd-hash>/sessions/` — sessions scoped to the work
  dir and archived on exit. `/resume` or `ax -r` reopens the picker;
  `ax --resume last` resumes the latest session for that work dir.
- `~/.config/ax/commands/<name>.md` — user commands: prompt templates for
  `/name [args]`. `$ARGUMENTS` and `$@` are all args, `$1..$9` positional,
  `${2:-default}` defaults, `${@:N}` and `${@:N:L}` slices; quoted args are
  parsed.
- `~/.agents/skills/<name>/SKILL.md` — skills. The model discovers them
  through the `skills` tool and reads one with the `skill` tool.
- `~/.config/ax/SYSTEM.md` — optional system prompt, appended to the
  built-in one.
- Search sessions with `/search <text>` in Axi or `ax --search <text>`.
  Sessions are JSONL: one line per message or compaction entry, so line
  matches map directly to entries.

## Design

- The loop is the only logic: messages → LLM → tool calls → results →
  repeat. It lives in `run` and is shared by the SDK and headless CLI.
- `run` never mutates its input. The session file is append-only: compaction
  appends a summary entry (with the recent messages it retains) instead of
  rewriting history; the LLM context is a projection of the entries.
- Tool calls in one batch run in parallel, results returned in call order;
  `edit` and `write` run sequentially to avoid file races.
- Retries (2×, 408/409/429/5xx + transport, with backoff) and context-overflow
  recovery (compact + retry once) live in the transport/session layers, not
  the turn loop. Tool calls from a response cut off by the output token
  limit are never executed.
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
    .tools(vec![ax::tools::read(), ax::tools::write(), ax::tools::edit(), ax::tools::bash("")]);

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
