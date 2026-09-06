# Contributing

## Quality gate

Run `make check` before finishing any change. It runs gofmt, vet, `go test`,
the race detector, a release build, and the black-box harness:

```bash
make check
```

`scripts/harness.py` (also runnable standalone via `make harness`) drives the
real binary end to end with a mock OpenAI endpoint and one-shot CLI runs; it
needs python3, already a dev dependency for `scripts/mock-server.py`.

Cheaper daily loop (autofixes without the gate):

```bash
gofmt -w . && go vet ./... && go test ./...
```

Install the current checkout to `~/.local/bin/ax` for local use:

```bash
make install
```

Set `PREFIX` to install elsewhere.

## Project rules

- Dependencies are Go stdlib only. No new deps without asking; no compile-time
  curl or other external C libraries.
- Extensibility is files-over-code: skills, user commands, SYSTEM.md. No
  plugin system, no MCP, no permissions layer.
- Config lives at `~/.config/ax/`; skills at `~/.agents/skills/`.
- The public UX runs through the event protocol ([docs/protocol.md](docs/protocol.md))
  and the Unix tool convention ([docs/tools.md](docs/tools.md)); keep both
  stable, as slaxi and every provider depend on them.
- Tool calls in one batch run in parallel; file-mutating tools must stay
  `sequential` so they never race on the same file.
