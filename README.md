# ax

A minimal LLM coding agent harness — Rust port of [ax-go](../ax-go),
with the terminal UX of [fx.sh](https://fx.sh) (vercel-labs/fx): a
transcript TUI with streamed markdown, tool status lines, and a
shell-style input bar.

Release binary: **~760 KB** (stripped, `x86_64-linux`, dynamically linked
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

Interactive — a transcript TUI:

```
OPENAI_API_KEY=... ./target/release/ax -model gpt-4.1-mini
𝒂x v0.1.0 · Run /help for commands

┃ fix the failing test
  ● 1 tool call · 1 command
  └ Ran go test ./...

fixed the test in ax_test.go

┃

auto · gpt-4.1-mini
```

- user prompts render as cards on a `┃` rail; assistant answers stream as
  markdown (headings, tables, task lists, blockquotes, highlighted code)
- tool calls collapse to an fx-style group — `● N tool calls · M kind` with
  `├`/`└` branch statuses (`● Running …` shows live while a tool executes,
  `● 1 tool call · 1 command` + `└ Ran …` once done; the raw output goes to
  the model, not the transcript)
- `• Thinking (Ns)` blinks while the model works; once content streams it
  drops to a dim `(↑x ↓y)` counter row, then a `Ns (↑x ↓y)` summary
- the transcript streams into the **terminal scrollback**: mouse wheel and
  Shift+PgUp scroll the whole session natively, exactly like fx — the chrome
  (title, input, status line) floats below the transcript at the top and pins
  to the bottom once the transcript fills the terminal; **Ctrl+O** opens a
  full-transcript view with internal PgUp/PgDn/wheel scrolling (Esc or Ctrl+O
  closes)
- **enter** sends, **shift+enter**/**alt+enter** insert a newline (the input
  is a wrapping multiline textarea, each line on a `┃` rail), **up/down**
  move between lines (first/last line fall back to history),
  **left/right** edit, **Ctrl+a/e/b/f/p/n/w/u/k/d/l/o** edit words/lines,
  **Alt+left/right** word moves
- **`/` completions**: type `/` and the command picker opens below the input
  (fx style — divider, `Commands N · Type to filter`, up to 6 rows with the
  selection highlighted); type to filter, **↑↓** move, **Tab** completes,
  **Enter** runs the selected command, **Esc** closes
- **`@` file completions**: type `@` anywhere after a word boundary and the
  workspace file picker opens (recursive, `.git`/`target`/`node_modules` and
  hidden dirs skipped); filter by name or path, **Enter**/**Tab** insert the
  full path and keep the `@` reference, selecting a directory keeps
  drilling into it, **Esc** closes
- **ctrl+c** twice within 3s exits (fx contract): the first press cancels
  any running turn, clears the draft, and shows "press ctrl+c again to
  exit"; any other key disarms, and the prompt expires after 3s. On exit the
  footer is cleared and a newline is printed so the shell prompt lands on a
  fresh line

### Sessions

Every `ax` launch starts a **fresh session**. The previous session is
archived automatically, so nothing is lost:

- `/resume` opens the session picker (titles, age, turn count; type to filter)
- `/new` archives the current session and starts fresh; `/clear` does the same
- `/rename <title>` names the current session
- CLI: `ax -r` / `ax resume` / `ax --resume` open the picker;
  `ax --resume last` resumes the most recent session;
  `ax --resume <id>` resumes by id
- sessions live in `.ax/sessions/`; the live transcript is
  `.ax/session.jsonl` (ax-go field names, byte-compatible)

### Slash commands

`/help` opens a searchable command catalog. Supported:
`/help /clear /new /reset /resume /rename /status /stats /model /models
/copy /version /quit`.

- `/models` browses models from `{base}/models`; Enter selects
- `/copy` copies the last assistant response (OSC 52)

### One-shot

```
OPENAI_API_KEY=... ./target/release/ax -model gpt-4.1-mini "explain main.go"
```

Renders the final answer as markdown with ANSI on a TTY, raw when piped.
Works with any OpenAI-compatible endpoint (`-base`): OpenRouter, DeepSeek,
Ollama, vLLM.

## CLI flags

| Flag          | Default                     | Meaning                      |
|---------------|-----------------------------|------------------------------|
| `-base`       | `https://api.openai.com/v1` | OpenAI-compatible base URL   |
| `-model`      | `gpt-4.1-mini`              | model name                   |
| `-system`     | built-in                    | system prompt                |
| `-C`          | current dir                 | working directory for tools  |
| `-r`, `--resume` | —                        | open the session picker      |
| `--resume last\|ID` | —                    | resume a saved session       |

With no prompt and a TTY, starts the TUI (fresh session). With no prompt
and no TTY, reads the prompt from stdin.

## System prompt

Default is minimal: identity + tool list + working directory. A user-owned
system prompt can be placed at `~/.config/ax/SYSTEM.md` (or
`$XDG_CONFIG_HOME/ax/SYSTEM.md`); it is appended after the built-in
preamble. `-system TEXT` replaces the prompt entirely for that run.

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
- `src/tui.rs` — transcript TUI: top-anchored fx chrome (title, input,
  status line) with native-scroll streaming, user cards, streamed blocks,
  tool status, activity/summary lines, command catalog screens
  (help/resume/models), session store, full-transcript mode
- `src/term.rs` — raw terminal layer (termios, keys incl. CSI modifiers and
  SGR mouse, bracketed paste, alternate screen)

## Differences from ax-go

- Full transcript TUI instead of a scrolling REPL; fresh sessions with an
  archive + resume picker instead of auto-resume.
- No permission system — tools always run (yolo). Sandboxing is expected to
  live outside ax (containers, seccomp, etc.).
- No in-run abort beyond ctrl+c (same behavior).
- HTTP uses system libcurl instead of Go's net/http; session files are
  byte-compatible with ax-go. libcurl is `dlopen`'d lazily on the first
  request, so the binary has no hard link-time dependency on libcurl's
  dependency tree (openssl, krb5, zstd, brotli, nghttp2/3, ssh2, …).

## Size and cold start

| Variant                                  | Size   | exec to first paint |
|------------------------------------------|--------|--------------------|
| stable + lazy libcurl + TUI (this)       | ~747 KB | ~0.6 ms            |
| stable + rustls/ring, no TUI             | ~1.48 MB | —                 |

The pure-Rust TLS stack (rustls + ring) is the size floor for a self-contained
binary; system libcurl is the pragmatic choice for a sub-1 MB target. The
lazy `dlopen` keeps exec fast: the binary only loads libc at start
(`ldd` shows libcurl and its 29-library tree only after the first request),
and the first paint happens before the input loop's first poll, so the prompt
renders in ~0.6 ms instead of waiting for the 40 ms poll tick.
