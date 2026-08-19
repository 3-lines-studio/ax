# ax

A minimal LLM coding agent harness — Rust port of [ax-go](../ax-go),
with the terminal UX of [fx.sh](https://fx.sh) (vercel-labs/fx): a
full-screen transcript, streamed markdown, tool status lines, and a
shell-style input bar.

Release binary: **~690 KB** (stripped, `x86_64-linux`, dynamically linked
against system libcurl). No TUI framework, no markdown library — both are
hand-rolled ports of fx's presentation layer.

## Build

```
cargo build --release
./target/release/ax
```

Requires stable Rust and libcurl dev headers (`libcurl4-openssl-dev` on
Debian/Ubuntu). HTTP/TLS is delegated to system libcurl to keep the binary
tiny.

## Usage

Interactive — a full-screen transcript TUI:

```
OPENAI_API_KEY=... ./target/release/ax -model gpt-4.1-mini
𝒂x v0.1.0 · Run /help for commands

┃ fix the failing test
  ● Running go test ./...
  │ ok ax 0.003s

fixed the test in ax_test.go

❯
auto · gpt-4.1-mini
```

- user prompts render as cards on a `┃` rail; assistant answers stream as
  markdown (headings, tables, task lists, blockquotes, highlighted code)
- tool calls show as `● Running …` / `● Ran …` with `│` output rails
- `• Thinking (Ns)` blinks while the model is working; `ctrl+c` interrupts
- **enter** sends, **up/down** history, **left/right/home/end** edit,
  **ctrl+w/u/k/a/e** edit words/lines, **tab** completes slash commands
- `/help /clear /new /reset /resume /model /system /status /stats /version`
  `/quit` — typing `/` opens the command picker
- the session (`.ax/session.jsonl`, ax-go field names) is caller-owned and
  auto-resumed on start; `/resume` reloads it

One-shot (like `fx ask`):

```
OPENAI_API_KEY=... ./target/release/ax -model gpt-4.1-mini "explain main.go"
```

Renders the final answer as markdown with ANSI on a TTY, raw when piped.

Works with any OpenAI-compatible endpoint (`-base`): OpenRouter, DeepSeek,
Ollama, vLLM.

## CLI flags

| Flag       | Default                     | Meaning                      |
|------------|-----------------------------|------------------------------|
| `-base`    | `https://api.openai.com/v1` | OpenAI-compatible base URL   |
| `-model`   | `gpt-4.1-mini`              | model name                   |
| `-system`  | built-in                    | system prompt                |
| `-C`       | current dir                 | working directory for tools  |

With no prompt and a TTY, starts the TUI. With no prompt and no TTY, reads
the prompt from stdin.

## Design

- **The loop is the only logic.** messages -> LLM -> tool calls -> results ->
  repeat. No memory, sessions, retries, parallel tool execution, or
  streaming in the SDK core (the TUI adds SSE streaming on top).
- **`run` is a pure function.** It never mutates its input; the returned
  transcript is append-only. The agent is frozen after construction.
- **Extend by composition, not hooks:**
  - wrap `Provider` (the `complete` trait method) for retries/logging/caching,
  - pass your own `Tool` values alongside or instead of the built-ins,
  - own the transcript for multi-turn, memory, or subagents.

Tool errors are returned to the model as text so it can self-correct; only
provider errors abort a run. Max turns stops the loop and returns the partial
transcript.

## Library

The SDK (`src/lib.rs`) exposes the minimal core — agent loop, provider trait,
tools, OpenAI client (blocking + SSE streaming with cancellation):

```rust
let mut agent = ax::Agent::new(ax::OpenAI::new(base, key))
    .model("gpt-4.1-mini")
    .system("You are a coding agent.")
    .tools(vec![ax::tools::read(), ax::tools::write(), ax::tools::edit(), ax::tools::bash("")]);

let msgs = agent.run(&[ax::Message { role: "user".into(), content: "fix the failing test".into(), ..Default::default() }])?;
```

## Ported from fx

- `src/markdown.rs` — byte-exact port of fx's markdown-to-ANSI renderer
  (blocks, inline styles, tables, footnotes, links, code highlighting)
- `src/tui.rs` — transcript TUI: user cards, streamed blocks, tool status,
  activity line, footer input bar, command picker
- `src/term.rs` — raw terminal layer (termios, keys, bracketed paste)

## Differences from ax-go

- Full-screen transcript TUI instead of a scrolling REPL (no rich slash
  commands beyond the ported set; no markdown dropped — it's exact).
- No in-run abort beyond ctrl+c (same behavior).
- HTTP uses system libcurl instead of Go's net/http; session files are
  byte-compatible with ax-go.

## Size notes

| Variant                              | Size   |
|--------------------------------------|--------|
| stable + system libcurl + TUI (this) | ~690 KB |
| stable + rustls/ring, no TUI         | ~1.48 MB |

The pure-Rust TLS stack (rustls + ring) is the size floor for a self-contained
binary; system libcurl is the pragmatic choice for a sub-1 MB target.
