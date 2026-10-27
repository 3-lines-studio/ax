# Unix tool convention

AX uses ordinary processes as external tool providers. The convention is independent of AX and can be implemented in any language.

## Registration

Providers are explicit. `AX_TOOLS` contains whitespace-separated executable names or paths:

```sh
AX_TOOLS="wax browserx bqx"
```

Names and paths in `AX_TOOLS` cannot contain whitespace. AX does not scan `PATH` for providers.

AX has no built-in tools. When `AX_TOOLS` is unset, AX starts without local
providers. Providers are indexed into ax's tool catalog alongside MCP servers;
the model sees `search` and `execute`, not each provider tool. See
[mcp.md](mcp.md). `-tool-mode direct` restores the old per-tool surface.

## Discovery

AX runs:

```sh
provider describe
```

The provider writes zero or more UTF-8 JSON objects to stdout, one object per line. Blank lines are ignored.

```json
{"name":"web_fetch","description":"Fetch a URL as Markdown","parameters":{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}}
```

Each descriptor contains:

- `name`: 1 to 64 ASCII letters, digits, underscores, or hyphens;
- `description`: a non-empty string;
- `parameters`: a JSON Schema object;
- `snippet`: an optional short hint the engine adds to the system prompt;
- `sequential`: optional boolean; run this tool alone, in call order, instead
  of in parallel with other calls in the same batch;
- `timeout`: optional per-call limit in seconds; `0` uses the engine default
  (300 s);
- `replay`: optional `"safe"` when re-running an interrupted call has no side
  effects;
- `read_only`, `destructive`, `open_world`: optional catalog hints used for
  search tags and batching.

Tool names must be unique across all providers. Unknown descriptor fields are ignored. A provider with no available tools prints nothing and exits successfully.

Mutating tools (`write`, `edit`, `bash`, browser actions, MCP tools) set
`sequential`. The engine then runs the batch one call at a time and keeps the
model's call order, so two writes cannot race on the same file.

`replay` records intent for hosts; it does not make the engine re-run a call
automatically. After a crash, an interrupted call is reported to the model
with its side effects unknown — see [session.md](session.md).

The engine validates arguments against `parameters` before executing. A
violation is returned to the model as a tool error instead of being passed to
the provider.

Discovery diagnostics belong on stderr. A nonzero status fails discovery. AX rejects combined descriptor output larger than 64 KiB.

## Execution

AX runs:

```sh
provider run NAME
```

AX writes one UTF-8 JSON object to stdin and closes stdin. The provider writes the tool result as UTF-8 text to stdout.

```sh
printf '%s\n' '{"url":"https://example.com"}' |
  wax run web_fetch
```

The result has no envelope. AX treats stdout as opaque text.

Diagnostics belong on stderr. Successful execution must produce non-empty stdout.

Exit statuses are:

```text
0  success
1  runtime failure
2  invalid usage or input
```

Other nonzero statuses are failures.

The engine stops a call that exceeds its timeout, cancelling the provider's
process group; the model sees a timeout error that says the side effects are
unknown.

## Cancellation

Providers must stop cleanly on `SIGTERM` and pass termination to their child processes. Consumers may use `SIGKILL` if a provider does not stop.

## Process channels

```text
argv    operation and tool name
stdin   one JSON input object
stdout  descriptors or result
stderr  diagnostics
status  outcome
signals cancellation
```

There is no handshake, registry, daemon, socket, result envelope, or version negotiation.

## Conformance check

Run the black-box contract check against built provider binaries:

```sh
./scripts/provider-contract.py /path/to/fsx /path/to/bashx /path/to/skillx
```

The check validates discovery framing, descriptors, obsolete command rejection, unknown tools, malformed JSON, and command statuses.
