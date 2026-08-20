# Plan: correctness + UX upgrades from pi, aligned with README goals

## Status

- [x] Phase 1 — One loop (`src/run.rs`, shared by SDK and TUI)
- [x] Phase 2 — Wire correctness (finish_reason, length-stop guard, arg coercion, sanitize)
- [x] Phase 3 — Retry (2×, 408/409/429/5xx + transport, jittered backoff, abortable)
- [x] Phase 4 — Tool output notices (bash line ranges + full-output path)
- [x] Phase 5 — Edit normalization + line diff feedback
- [x] Phase 6 — Loop UX (live tool output, parallel with ordered results, steering)
- [x] Phase 7 — Compaction + overflow (append-only entries, structured summaries, compact-and-retry-once)
- [x] Phase 8 — Session search (`/search`, `ax --search`)
- [x] Phase 9 — Prompts & skills (tool snippets, bash-style template args, skill validation)
- [x] Phase 10 — README + gate

Deviation from Task 3.1: retry lives in `run.rs::stream()` (the single choke
point for both SDK and TUI) instead of a `RetryingProvider` wrapper — the
`StreamHandle` thread requires `'static` data and `Tool` is not `Clone`, so a
generic wrapper cannot re-issue a borrowed request from its thread. Same
semantics (2 retries, statuses, backoff, no retry after events were emitted).
Retry-After header honoring is skipped: curlffi has no header callback.

---


Hard constraints (README.md + CONTRIBUTING.md):
- Binary < 1 MB (currently ~785 KB). Deps exactly `serde`, `serde_json`, `libc`. No new deps.
- Loop stays minimal and pure; `run` never mutates input; transcript is append-only.
- Composition over hooks: retries = Provider wrapper; compaction = session layer.
- Tool errors return to the model as text; only provider errors abort a run.
- No permission system, no plugins, no MCP. Files-over-code.
- Quality gate per phase: `cargo fmt && cargo clippy --all-targets -- -D warnings && cargo test && cargo build --release`.

README's "No memory, retries, or parallel tool execution in the core" is intentionally superseded; the Design paragraph is edited in Phase 10.

## Phase 1 — One loop (foundation)

Currently two loops: `Agent::run` (lib.rs) and `run_turn` (tui.rs:3148). Unify so fixes land once.

- **1.1** Extract `run_turn` + `stream_request` + `exec_tool` (tui.rs:3148–3268) into `src/loop.rs`: `run_stream(provider, model, system, tools, msgs, sink) -> Vec<Message>` with a `Sink` trait (delta, tokens, tool_start, tool_delta, tool_end, turn_end, pending_user_input). `Agent::run` delegates with a no-op sink. TUI keeps rendering, passes its `TurnEvent` sender as the sink.
  - Checks: one-shot mode byte-identical; TUI transcript identical; `Response` gains `stop_reason`; binary delta < 10 KB.

## Phase 2 — Wire correctness

- **2.1** Capture `finish_reason` in `StreamAcc` (`OaStreamChoice`) and non-streaming `OaResponse`. In `loop.rs`: `stop_reason == "length"` with tool calls → do NOT execute; return each as `error: tool call not executed: response hit the output token limit, arguments may be truncated. Re-issue with complete arguments.` (pi: `failToolCallsFromTruncatedMessage`).
  - Checks: test with a chunk sequence ending `finish_reason:"length"` + a truncated `arguments` that still parses — assert no execution.
- **2.2** Argument coercion in `new_tool` wrapper (tools.rs): coerce against the tool's `parameters` Value — numeric strings → numbers, `true`/`false`/`1`/`0` → booleans, numbers/bools → strings, drop `null` for optional non-nullable fields, recurse `items`/`properties`. On failure: `error: invalid arguments for <tool>: <detail>\nReceived: <raw args>`.
  - Checks: tests for `"timeout":"30"`, `"offset":true`, optional-null drop; error contains the received JSON.
- **2.3** `sanitize(s)` in tools.rs: strip control chars except `\t\n\r` (pi: `sanitizeBinaryOutput`). Apply in `bash` and `read` text paths.
  - Checks: `\x00`, `\x1b`, `\x07` stripped; `\n`/`\t` preserved.

## Phase 3 — Retry as a Provider wrapper

- **3.1** Add `stream()` to the `Provider` trait; `OpenAI` implements it from `complete_stream`. Add `RetryingProvider<P>` in lib.rs: on `Err` with status 408/409/429/5xx or transport error, retry up to 2×, backoff `500ms·2ⁿ` + jitter, honor `Retry-After`/`retry-after-ms`, cap server delay at 60 s (surface as error, don't hang), sleep checks the cancel flag. TUI and `Agent::run` use the wrapper.
  - Checks: fake provider failing 429 twice then succeeding; backoff sequence; abort during sleep cancels.

## Phase 4 — Tool output notices

- **4.1** bash (tools.rs): on truncation keep the temp `.out` file and append `\n\n[Showing last N of M lines (16KB limit). Full output: /tmp/ax-bash-...out]`; count completed lines. Timeout/exit-code errors stay after the notice.
  - Checks: `yes | head -c 100000` → notice with line range + path; file exists after run.
- **4.2** read: byte-aware notices — `[Showing lines X-Y of Z. Use offset=N to continue.]` on the truncated path (sed hint and remaining-lines hint already exist).
  - Checks: 2000-line file → `Showing lines 1-340 of 2000 (16KB limit). Use offset=341`.

## Phase 5 — Edit: normalization + diff feedback

- **5.1** Edit closure deserializes `edits` leniently before `apply_edits`: `edits` as JSON string → parse array; single object → wrap; legacy top-level `oldText`/`newText` → `edits[0]` (pi: `prepareEditArguments`).
  - Checks: tests for all three malformed shapes.
- **5.2** Return a line-numbered diff: hand-rolled hunk diff (common-prefix/suffix per hunk, ~60 lines, no crate) appending `\n\nDiff:\n-12 old line\n+12 new line` after `Successfully replaced N block(s)`. No unified patch.
  - Checks: edit test asserts diff lines with changed line numbers; no-change case returns no diff.

## Phase 6 — Loop UX: live output, parallel, steering

- **6.1** `Tool::run` gains a progress callback (`Fn(&str, &dyn Fn(&str)) -> String`). bash calls it with the current tail, throttled ~100 ms. loop.rs forwards as `tool_delta`; TUI renders inside the tool status line.
  - Checks: `sleep 2; echo hi` shows the live tail; SDK callers unaffected (no-op callback).
- **6.2** Parallel batch execution in loop.rs: **default on** for read/grep/bash; `edit`/`write` marked sequential (file-mutation safety). Threads via `std::thread::scope`; results emitted in original call order; cancel flag checked in the join loop.
  - Checks: stress test with 3 parallel `read` calls — ordered results; concurrent edit+write to same path stays serialized.
- **6.3** Steering: `Sink::pending_user_input()` polled before each assistant request and between turns; TUI enables `Enter` while running, queues the draft, loop injects it as a user message **before the next assistant response** (never mid-stream). `ctrl+c` abort unchanged.
  - Checks: type + Enter mid-bash; message appears as the next user turn after tool results; abort still works.

## Phase 7 — Compaction + overflow (session layer, transcript stays append-only)

- **7.1** session.rs: append-only entries: `Message` | `Compaction { summary, tokens_before, timestamp, retained: Vec<Message> }`. `context_messages()` projects entries → `Vec<Message>` (compaction entry → summary-as-user-message + retained tail). File never rewritten.
  - Checks: old JSONL sessions still load (treat every line as `Message`); projection test.
- **7.2** `compact(provider, model, msgs) -> Result<String>`: pure function; serializes user/assistant/tool messages to text; calls the model with the structured prompt (`## Goal / Constraints & Preferences / Progress / Key Decisions / Next Steps / Critical Context`); returns the summary. The loop doesn't know about it.
  - Checks: fake-provider test asserts the structured format and all messages; summary stored as a new entry.
- **7.3** Driver `run_with_compaction` (wraps `run_stream`, owns the session):
  - `context_window` is a config key in `~/.config/ax/config`, set alongside `model` (per-model; `/login` asks for it after the model, optional). When unset, the model's own default applies: no threshold compaction and no silent-overflow usage check; overflow detection relies on provider error messages only.
  - When set: after each turn, estimate tokens (`chars/4` + last usage); compact when `> context_window − 16384`, keep recent ~20 k tokens.
  - Explicit overflow errors (regex list: `prompt is too long`, `exceeds the context window`, `maximum context length`, `input token count.*exceeds`, non-overflow exclusions for throttling) always trigger: drop the failed assistant message, compact, retry once per turn — window set or not.
  - Checks: overflow-regex unit tests (throttling message must NOT match); integration: big transcript triggers one compaction entry; retry succeeds; compaction failure does not abort the run.

## Phase 8 — Session search (zero deps)

- **8.1** `/search <text>` in the TUI + `ax --search <text>`: read `~/.config/ax/sessions/*.jsonl` + live file, substring match per JSONL line (one message per line), print session title + timestamp + matching line excerpt. Hand-rolled matching. No ranking, no diacritics — noted in README.
  - Checks: two sessions with distinct content; search finds the right one; empty result message; no panic on binary junk lines.

## Phase 9 — Prompts & skills

- **9.1** Tool snippets: `snippet: &'static str` on `Tool`; `system_prompt` builds `Available tools:\n- bash: ...` from the actual list + edit guidelines (`When changing multiple separate locations in one file, use one edit call with multiple entries`, `Merge nearby changes into one edit`).
  - Checks: system prompt test asserts snippet presence and ordering.
- **9.2** Prompt templates: extend `expand_user_command` with `$1`/`$2`, `$@`, `${2:-default}`, `${@:N}`, quoted-arg parsing (pi: `substituteArgs`). `$ARGUMENTS` stays.
  - Checks: extend `expand_user_command_cases`.
- **9.3** Skills: name validation (`^[a-z0-9-]+$`, no `--`), description required, skip `.gitignore`d dirs, first-name-wins on collision with a warning in the `skills` tool output, relative-path hint in the `skills` tool description.
  - Checks: loader tests for invalid names, missing description, collision; `skills` tool output shows warnings.

## Phase 10 — README + gate

- **10.1** Edit README: Design paragraph (retries in provider wrapper, compaction in session layer, loop stays minimal/pure); Files section documents compaction entries and `/search`; drop the "No memory, retries, or parallel tool execution" claim; record actual binary size.
- **10.2** Dead-code sweep for replaced code (old `run_turn` remnants, `limit()` if superseded). Mention, don't delete unrelated code.
- Checks: full CONTRIBUTING gate; `target/release/ax` < 1 MB.

## Exclusions

Images. Permission/trust prompts (README: tools always run). Memory (README). Plugins/MCP (CONTRIBUTING: files-over-code). SQLite FTS (deps/binary). Unified patch output.

## Decisions (confirmed)

1. Parallel default on for read/grep/bash; edit/write sequential.
2. `context_window` is a per-model config key (`model` + `context_window` in ax/config; `/login` asks for it, optional). Unset = model default (no threshold/silent-overflow checks, explicit overflow errors still recover).
3. Retry: 2 retries, 500 ms·2ⁿ, 408/409/429/5xx + transport, Retry-After honored, abortable.
4. Steering injects before the next assistant response.

## Dependency order

Phases 1→7 strict. Phases 8–9 independent, may interleave. Phase 6 is the highest TUI risk.
