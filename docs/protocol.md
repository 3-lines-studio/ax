# AX process protocol

AX protocol version 1 uses one JSON object per line on stdout. Start AX with
`--events`. The Go constants for this contract live in `internal/axproto`;
change the document and the constants together.

The first event is always:

```json
{"type":"protocol","version":1}
```

A client must reject an unsupported version and may ignore unknown event types within a supported version.

## Input

Pass a prompt as the final command argument.

While AX runs, stdin accepts JSONL controls:

```json
{"type":"cancel"}
{"type":"steer","text":"also update the changelog"}
```

`cancel` stops the run at the next safe point and ends with `outcome:
"cancelled"`. `steer` adds a user message to the same run: AX applies it at the
next turn boundary (after the running tool batch finishes, before the next model
call), appends it to the session log as a user message and echoes
`{"type":"steer","text":"..."}`. A message that arrives after the final answer is
ignored; nothing is queued for a later run.

## Output events

```json
{"type":"assistant_delta","text":"partial text"}
{"type":"reasoning_delta","text":"partial reasoning"}
{"type":"assistant_done"}
{"type":"tool_start","id":"call_1","name":"execute","target":"fsx.read","arguments":"{\"tool\":\"fsx.read\",\"arguments\":{\"path\":\"main.go\"}}"}
{"type":"tool_delta","id":"call_1","text":"partial output"}
{"type":"tool_result","id":"call_1","output":"result"}
{"type":"tool_done","id":"call_1"}
{"type":"usage","input":100,"output":20,"cached_input":0}
{"type":"compaction","upto":42,"summary":"Goal: ... Decisions: ..."}
{"type":"steer","text":"also update the changelog"}
{"type":"message","message":{"Role":"assistant","Content":"done"}}
{"type":"result","messages":[],"usage":{"input":100,"output":20,"cached_input":0}}
{"type":"error","message":"provider error"}
{"type":"done","outcome":"done"}
```

`compaction` is emitted when the engine summarized older messages to stay under
`compact_at` of `context_window`. `upto` is how many messages the summary
replaces, and the summary also goes into the system prompt of every later
request. A host can render a divider and keep going; no action is required.

In catalog mode `tool_start` names the model-facing tool (`search` or
`execute`). For `execute`, the optional `target` field carries the resolved
catalog id, which is what hosts should display and audit. In direct mode the
event names the provider tool itself and `target` is absent.

AX emits exactly one final `done` event. Outcomes are `done`, `cancelled`, `compact`, `max_turns`, or `failed`. Failed runs exit non-zero. `compact` is the outcome of `ax compact`, which summarizes a session on demand instead of running a turn.

`reasoning_delta` carries the model's thinking text. It arrives before and
separately from `assistant_delta`; clients that only render the answer can
ignore it. `assistant_done` closes the turn in either case.

## Message content

The tool surface is described in [mcp.md](mcp.md): two tools, `search` and
`execute`, over a catalog of MCP servers and local providers.

`message` events carry the stored `Message`: `Role`, `Content`, optional
`ReasoningContent`, optional `ContentParts` and optional `ToolCalls`. A user
message with images has `ContentParts` with `text` and `image` blocks plus a
plain-text `Content` rendering; `image` blocks reference a session blob or an
http(s) URL. A tool message carries `ToolCallID`.

## Durable session log

With `--session FILE`, AX appends the run to a JSONL log instead of keeping it
only in memory. The log is the recovery contract described in
[session.md](session.md): append-only, fsynced per entry, and resumed by the
next invocation with the same `FILE`.

## External CLI tools

Discovery runs `<command> describe`. The command writes one tool descriptor per line:

```json
{"name":"data_query","description":"Run a read-only query","parameters":{"type":"object","properties":{"sql":{"type":"string"}},"required":["sql"]}}
```

Execution runs `<command> run <name>`. AX writes JSON arguments to stdin. Tool output belongs on stdout, diagnostics on stderr, and failure uses a non-zero exit status.
