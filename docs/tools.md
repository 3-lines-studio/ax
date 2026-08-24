# Unix tool convention

AX uses ordinary processes as external tool providers. The convention is independent of AX and can be implemented in any language.

## Registration

Providers are explicit. `AX_TOOLS` contains whitespace-separated executable names or paths:

```sh
AX_TOOLS="wax browserx bqx"
```

Names and paths in `AX_TOOLS` cannot contain whitespace. AX does not scan `PATH` for providers.

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
- `snippet`: an optional string.

Tool names must be unique across all providers. Unknown descriptor fields are ignored. A provider with no available tools prints nothing and exits successfully.

Discovery diagnostics belong on stderr. A nonzero status fails discovery.

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
