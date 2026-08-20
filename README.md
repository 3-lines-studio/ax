# ax

A minimal LLM coding agent harness with the terminal UX of
[fx.sh](https://fx.sh): a transcript TUI with streamed markdown, tool
status lines, and a shell-style input bar.

Release binary: **~514 KB**. No TUI framework, no markdown library — both
hand-rolled. HTTP goes through system libcurl.

## Install

```sh
curl -fsSL https://ax.3lines.studio/install.sh | sh
```

Installs to `~/.local/bin` (override with `AX_PREFIX`). Linux (x86_64, aarch64)
and macOS (arm64). No Rust toolchain, no root. The installer verifies a
sha256 checksum on every download. Pin a version with `AX_VERSION=v0.1.0`.
Uninstall: `rm ~/.local/bin/ax`.

## Build

```sh
cargo +nightly build --release --config 'build.rustflags="-C force-unwind-tables=no"'
./target/release/ax
```

Requires nightly Rust (for `-Zbuild-std`, used to rebuild `std` with
`panic = "immediate-abort"`) with the `rust-src` component
(`rustup component add rust-src`), plus libcurl dev headers
(`libcurl4-openssl-dev` on Debian/Ubuntu).

## Quick start

Interactive TUI:

```sh
OPENAI_API_KEY=... ax
```

One-shot:

```sh
OPENAI_API_KEY=... ax "explain main.go"
```

The TUI is a transcript: prompts render on a `┃` rail, answers stream as
markdown, tool calls collapse to status lines. `enter` sends,
`shift+enter` inserts a newline, `/` and `@` open command and file
completions, `ctrl+c` twice exits. `/help` lists everything.

Any OpenAI-compatible endpoint works: `-base https://...` for OpenRouter,
DeepSeek, Ollama, vLLM.

## Config

`~/.config/ax/config` (or `$XDG_CONFIG_HOME/ax/config`):

```
api_key = "sk-..."        # used when OPENAI_API_KEY is unset
model = "glm-4.5"
base = "http://localhost:11434/v1"
context_window = 131072   # optional: enables proactive context compaction
```

Plain `key = value` lines, `#` comments. Precedence: flags > env > config.
Run `/login` in the TUI to write these interactively. Set `context_window`
to your model's context size to enable automatic compaction; without it,
compaction only runs after an explicit context-overflow error.

## Files

Everything is files; there is no state machine.

- `~/.config/ax/sessions/` — sessions, archived on exit. `/resume` or
  `ax -r` reopens the picker; `ax --resume last` resumes the latest.
- `~/.config/ax/commands/<name>.md` — user commands: prompt templates for
  `/name [args]`. `$ARGUMENTS` and `$@` are all args, `$1..$9` positional,
  `${2:-default}` defaults, `${@:N}` and `${@:N:L}` slices; quoted args are
  parsed.
- `~/.agents/skills/<name>/SKILL.md` — skills. The model discovers them
  through the `skills` tool and reads one with the `skill` tool.
- `~/.config/ax/SYSTEM.md` — optional system prompt, appended to the
  built-in one.
- Search sessions with `/search <text>` in the TUI or `ax --search <text>`.
  Sessions are JSONL: one line per message or compaction entry, so line
  matches map directly to entries.

## Design

- The loop is the only logic: messages → LLM → tool calls → results →
  repeat. It lives in `run` and is shared by the SDK and the TUI.
- `run` never mutates its input. The session file is append-only: compaction
  appends a summary entry (with the recent messages it retains) instead of
  rewriting history; the LLM context is a projection of the entries.
- Tool calls in one batch run in parallel, results returned in call order;
  `edit` and `write` run sequentially to avoid file races.
- Retries (2×, 408/429/5xx + transport, with backoff) and context-overflow
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
