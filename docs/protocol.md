# AX process protocol

AX protocol version 1 uses one JSON object per line on stdout. Start AX with `--events`.

The first event is always:

```json
{"type":"protocol","version":1}
```

A client must reject an unsupported version and may ignore unknown event types within a supported version.

## Input

Pass a prompt as the final command argument, or pass a JSON array of messages with `--messages FILE`.

While AX runs, stdin accepts JSONL controls:

```json
{"type":"steer","text":"focus on the parser"}
{"type":"cancel"}
```

## Output events

```json
{"type":"assistant_delta","text":"partial text"}
{"type":"assistant_done"}
{"type":"tool_start","id":"call_1","name":"read","arguments":"{\"path\":\"main.rs\"}"}
{"type":"tool_delta","id":"call_1","text":"partial output"}
{"type":"tool_result","id":"call_1","output":"result"}
{"type":"tool_done","id":"call_1"}
{"type":"usage","input":100,"output":20,"cached_input":0}
{"type":"message","message":{"Role":"assistant","Content":"done"}}
{"type":"result","messages":[],"usage":{"input":100,"output":20,"cached_input":0}}
{"type":"error","message":"provider error"}
{"type":"done","outcome":"done"}
```

AX emits exactly one final `done` event. Outcomes are `done`, `cancelled`, `compact`, `max_turns`, or `failed`. Failed runs exit non-zero.

## External CLI tools

Discovery runs `<command> describe`. The command writes one tool descriptor per line:

```json
{"name":"data_query","description":"Run a read-only query","parameters":{"type":"object","properties":{"sql":{"type":"string"}},"required":["sql"]}}
```

Execution runs `<command> run <name>`. AX writes JSON arguments to stdin. Tool output belongs on stdout, diagnostics on stderr, and failure uses a non-zero exit status.
