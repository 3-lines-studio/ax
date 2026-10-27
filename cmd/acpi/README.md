# acpi

Agent Client Protocol (ACP) interface for `ax`.

```text
ACP client (e.g. buzz-acp) --stdio (ACP JSON-RPC)--> acpi --subprocess--> ax
```

`ax` has no ACP support of its own: it speaks its own one-shot `--events`
JSONL protocol, not ACP's JSON-RPC. `acpi` is the bridge — it speaks ACP on
stdio (so any ACP-hosting client can spawn it, the same way it would spawn
`goose acp` or `claude-agent-acp`) and runs `ax` as a subprocess per turn,
translating between the two protocols.

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- acpi
```

## Run

```sh
export ACPI_AX_PATH=ax                              # default: ax (must be in PATH)
export ACPI_MODEL=deepseek-flash
export ACPI_BASE_URL=https://api.deepseek.com
export ACPI_SYSTEM_FILE=./prompt.md
export ACPI_SESSION_DIR=./data/sessions
export AX_TOOLS="wax bqx pgx fsx bashx skillx"       # read directly by ax, not acpi
export DEEPSEEK_API_KEY=...                          # or whatever ax's config expects
acpi
```

`acpi` speaks ACP on stdin/stdout — it's meant to be spawned by an ACP
client, not run interactively. Point any ACP-hosting harness at it as the
agent command.

## Design

One `ax --session <file> --events` subprocess per `session/prompt` call.
`ax` is turn-based (spawn, run to completion, exit), so acpi doesn't hold a
long-lived `ax` process; ACP's own session concept maps to an `ax --session`
JSONL file that accumulates across calls.

Event translation, verified against `ax`'s source
(`cmd/ax/main.go`, `eventSink`), not just its README:

| ax event | ACP action |
|---|---|
| `assistant_delta` | `session/update` → `AgentMessageChunk` |
| `tool_start` | `session/update` → `ToolCall` (pending) |
| `tool_delta` | `session/update` → `ToolCallUpdate` (in_progress, accumulated text) |
| `tool_result` (id+output) | `session/update` → `ToolCallUpdate` (completed) |
| `done, outcome:"done"/"compact"` | `PromptResponse{StopReason: end_turn}` |
| `done, outcome:"cancelled"` | `PromptResponse{StopReason: cancelled}` |
| `done, outcome:"max_turns"` | `PromptResponse{StopReason: max_turn_requests}` |
| `done, outcome:"failed"` (+ preceding `error`) | JSON-RPC error — ACP has no `StopReason` for this |

Cancellation (`session/cancel`) writes `{"type":"cancel"}` to the running
`ax` subprocess's stdin, matching ax's documented control-channel protocol.
The `ax` subprocess is deliberately **not** tied to the request's `context.Context`
(no `exec.CommandContext`): the ACP SDK cancels that context as part of
dispatching the matching `session/cancel` notification, which would make Go
SIGKILL the process immediately and race the graceful stdin write below it.
See the comment in `axrun.go`.

## Limitations (v1)

- **Text-only prompts.** `PromptRequest.Prompt` image/audio/resource blocks
  are dropped; only text blocks are forwarded to `ax`.
- **MCP servers require `mcpx`.** ACP's `session/new` can hand an agent a
  list of MCP servers (e.g. `buzz-acp` injects the `buzz` CLI's MCP server
  for Buzz actions like `send_message`). `ax`'s tools are external
  Unix-convention commands (`describe`/`run`), not MCP — it can't consume an
  MCP server directly. `acpi` writes any MCP servers from `session/new` to
  `<axSession>.mcp-servers.json` and adds `mcpx` to that turn's `AX_TOOLS`;
  `mcpx` (in this repository) reads that file and does the actual
  MCP↔ax bridging. Verified working end to end: `mcpx`'s test suite feeds it
  the exact JSON `acpi` produces (via the real ACP SDK) and confirms
  `describe`/`run` both work against it. Requires `mcpx` to be installed and
  on `ax`'s `PATH` wherever `acpi` runs.
- **No session resume/load.** Each ACP session maps 1:1 to one `ax`
  session file for its lifetime; `session/load` and `session/resume` aren't
  implemented.

## Test

```sh
go test ./cmd/acpi
```

`acpi_test.go` drives the real `acpi` binary over stdio exactly as an ACP
client would (`testdata/fake-ax`, `testdata/fake-ax-cancellable` stand in for
`ax`, emitting the same event sequence the real binary does), covering the
event-translation table above and the cancel path.

To also exercise the real `ax` binary (flag compatibility + the
`outcome:"failed"` path, against an intentionally unreachable base URL, no
API key needed):

```sh
ACPI_TEST_AX_PATH=./bin/ax go test ./cmd/acpi -run TestFailedOutcomeWithRealAx
```
