# ax

A minimal LLM coding agent harness with the terminal UX of
[fx.sh](https://fx.sh): a transcript TUI with streamed markdown, tool
status lines, and a shell-style input bar.

Release binary: **~768 KB**. No TUI framework, no markdown library — both
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
cargo build --release
./target/release/ax
```

Requires stable Rust and libcurl dev headers (`libcurl4-openssl-dev` on
Debian/Ubuntu).

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
```

Plain `key = value` lines, `#` comments. Precedence: flags > env > config.
Run `/login` in the TUI to write these interactively.

## Files

Everything is files; there is no state machine.

- `~/.config/ax/sessions/` — sessions, archived on exit. `/resume` or
  `ax -r` reopens the picker; `ax --resume last` resumes the latest.
- `~/.config/ax/commands/<name>.md` — user commands: prompt templates for
  `/name [args]`; `$ARGUMENTS` is replaced by the args.
- `~/.agents/skills/<name>/SKILL.md` — skills. The model discovers them
  through the `skills` tool and reads one with the `skill` tool.
- `~/.config/ax/SYSTEM.md` — optional system prompt, appended to the
  built-in one.

## Design

- The loop is the only logic: messages → LLM → tool calls → results →
  repeat. No memory, retries, or parallel tool execution in the core.
- `run` is a pure function. It never mutates its input; the transcript is
  append-only. The agent is frozen after construction.
- Extend by composition, not hooks: wrap `Provider`, pass your own `Tool`
  values, own the transcript.
- Tool errors return to the model as text so it can self-correct; only
  provider errors abort a run.

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
- libcurl is `dlopen`'d lazily on the first request: exec and first paint
  stay fast, and the binary has no link-time dependency on curl's
  dependency tree.
- Contributing: see [CONTRIBUTING.md](CONTRIBUTING.md).
