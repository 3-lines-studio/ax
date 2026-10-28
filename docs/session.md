# Durable session log

`ax --session FILE` keeps a run in an append-only JSONL file. The same file
resumes the conversation: the next `ax --session FILE "..."` projects it into
provider history and continues from there. Keeping the log is what makes a run
survivable: after a crash, the next process can tell what happened and never
silently repeats a tool call.

This document is the log contract. The code lives in `cmd/ax/session.go`.

## Guarantees

- **Append-only.** Older lines are never rewritten or deleted. Each entry is
  one line, written with `O_APPEND` and `fsync`ed before the engine acts on the
  event it records (sends a request, starts a tool, returns a result).
- **One writer.** An open session holds an exclusive `flock` (on Unix); a
  second process gets `lock session ...: already in use` instead of
  interleaving two runs.
- **Torn tail tolerated.** A crash between write and fsync can leave a partial
  last line. Readers ignore it with a warning on stderr. A corrupt line
  anywhere else fails the run.
- **No replay of unknown side effects.** A tool call without a recorded result
  is never executed again. The model receives an interrupted-tool result.

## Entries

Every line is a JSON object with a `type` and, once a run starts, `run_id`.
`at` is unix milliseconds.

| `type` | Fields | Meaning |
| --- | --- | --- |
| `run_start` | `prompt`, `model`, `base_url`, `tools` | A run began. Written before the user message. |
| `message` | `message`, `turn` | A complete message: user, assistant (with `ToolCalls`) or tool result. |
| `assistant_partial` | `message`, `turn` | Throttled copy of the assistant text and reasoning streamed so far (at most once a second or per 4 KiB). Audit only; never sent back to the model. |
| `tool_intent` | `call`, `target`, `turn` | The engine is about to execute a tool call. Written before the process starts. `target` is the resolved catalog id for `execute` (e.g. `fsx.write`). |
| `tool_result` | `call`, `target`, `output`, `turn` | The tool finished. Written after the output is known. |
| `usage` | `usage`, `turn` | Token accounting for an assistant turn. |
| `compaction` | `upto`, `summary` | Older messages were summarized. `upto` counts projected messages from the start of the current history; readers drop that prefix and put `summary` in the system prompt. |
| `run_end` | `outcome`, `usage` | The run finished with an outcome from the [event protocol](protocol.md). |

The legacy bare-message shape (a line that is just a `Message` object) is still
read as a `message` entry.

### Compaction

When the projected prompt reaches `compact_at` of `context_window` (defaults
`0.5` and `1000000`), the engine summarizes the older messages and appends one
`compaction` entry instead of rewriting anything. Projecting the log applies
every compaction in order: the messages before `upto` are dropped, the summary
becomes the *Summary of the earlier conversation* section of the system prompt,
and a mid-run compaction also appends the `Continue the task...` user turn so
the model keeps something to act on. `ax compact --session FILE` writes the
same entry on demand.

## Recovery

`run_start` without a matching `run_end` marks an interrupted run. On the next
invocation AX prints a warning and projects the log into a history that a
provider accepts:

- assistant turns that never finished (leftovers from a crash mid-stream) are
  dropped; `assistant_partial` is kept in the log for audit only;
- every tool call in a pending batch gets an interrupted result — in call
  order, including calls whose intent was written but whose process never
  started. When the intent carried a `target`, the message names it:
  `error: tool call interrupted before its result was recorded (target:
  fsx.write): its side effects are unknown.`
- tool results without a matching call are dropped, so a duplicated or
  orphaned result cannot corrupt the history.

The log itself is not modified during recovery. The repaired history exists in
memory for the resumed request.

## Images

Local `-image` files are stored as content-addressed blobs next to the log:
`FILE.blobs/<sha256>`. The log only records the digest (`ImageRef.Blob`,
`MediaType`, `Detail`, `Name`); the data URL is built when the request is
built. Re-adding the same file reuses the blob. Without `--session` the image
is inlined as a data URL and no blob is written. http(s) URLs are stored as
URLs and fetched by the provider.

## Inspecting a session

The log is plain JSONL, so ordinary tools work:

```sh
jq -c 'select(.type=="tool_intent" or .type=="tool_result")' session.jsonl
jq -r 'select(.type=="run_end") | .outcome' session.jsonl
```

A host that routes sessions (Botdir conventions place them under
`state/ax/`) reads the same entries; it must not rewrite existing lines.

## Resuming

A log whose last `run_start` has no `run_end` is an interrupted run. `ax
--resume --session FILE` appends one user turn describing where the run stopped
(`resumePrompt`) and continues; the interrupted calls are already in the log as
`tool` messages with an error that says their side effects are unknown, so the
model verifies state instead of repeating them.

The log is never rewritten to resume: a resume is one more `run_start`, one
more user message and a final `run_end`, in that order.
