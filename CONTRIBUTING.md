# Contributing

## Quality gate

Run `make check` before finishing any change. It runs gofmt, `go mod tidy
-diff`, vet, `go test`, the race detector, builds every program, enforces the
per-binary size budgets, checks the catalog against the tree, and runs the ax
black-box harness:

```sh
make check
```

Cheaper daily loop:

```sh
gofmt -w . && go vet ./... && go test ./...
```

For Markdown-only changes there is a fast gate that skips build, tests and
harness but still validates formatting, `go.mod` and the catalog:

```sh
./scripts/check.sh --fast
```

The harness needs python3 (already a dev dependency for `scripts/mock-server.py`
and `scripts/eval.py`). Per-program tests run the same way everywhere:

```sh
go test ./cmd/fsx
go test ./...
```

## Layout

- `cmd/<program>/` — one `package main` per program, with its README and tests.
  Programs are independent binaries; do not import one `cmd` package from
  another.
- `internal/axproto` — the wire contract (event types, outcomes, tool
  descriptors). Bridges and transports compile against it.
- `internal/botdir` — Botdir path, config and secret helpers. Consumers and
  providers share it instead of copying `BOT_ROOT`/`BOT_DATA` logic.
- `docs/` — protocol, tool convention, session log and the Botdir
  specification. Change the document and the code together.
- `scripts/` — installer, packaging, harnesses. One script serves every
  program; take the program name as an argument instead of copying it.
- `catalog.json` — the machine-readable list of programs. `scripts/render-catalog.py`
  renders the README table from it; `make catalog` regenerates it.

## Adding a program

1. Create `cmd/<name>/` with a `package main` and a README.
2. Add it to `PROGRAMS` in the `Makefile` and to `catalog.json`.
3. Run `scripts/render-catalog.py` and `make check`.

The release workflow discovers binaries from `PROGRAMS`, so no per-program CI
or packaging script is needed.

## Rules

- Dependencies are allowed where a provider genuinely needs them (chromedp,
  BigQuery, pgx, Slack, MCP), but prefer the standard library and ask before
  adding something new. The engine itself stays stdlib-only apart from TOML
  parsing.
- Extensibility is files-over-code: skills, user commands, `bot.md`. No plugin
  system in the engine.
- Config lives at `~/.config/ax/`; skills at `~/.agents/skills/`.
- The public UX runs through the event protocol ([docs/protocol.md](docs/protocol.md))
  and the Unix tool convention ([docs/tools.md](docs/tools.md)); keep both
  stable, as every transport, bridge and provider depends on them.
- Tool calls in one batch run in parallel; file-mutating tools stay
  `sequential` so they never race on the same file. A tool with unknown side
  effects (MCP, browser) is `sequential` too.
- Session logs are append-only ([docs/session.md](docs/session.md)). Append and
  fsync before acting on an event, never rewrite a line, and never re-run a
  tool call whose result was not recorded: report it as interrupted.
- DeepSeek requires `reasoning_content` on assistant history when tools are
  present. Keep `Message.ReasoningContent` when touching the request builder,
  and keep thinking/max-token options in one place in `cmd/ax/openai.go`.
- Keep the Botdir conventions in one place: use `internal/botdir` instead of
  reading `BOT_ROOT`, `BOT_DATA` and `secrets/` by hand.

## Releases

`main` is production. Work on a topic branch, open a pull request, and merge
through it. A release is one tag for the whole ecosystem:

```sh
git tag vX.Y.Z
git push origin vX.Y.Z
```

The release workflow cross-compiles every program for Linux x86-64/ARM64 and
macOS ARM64 from one Linux runner, publishes `<program>-<os>-<arch>` plus
checksums to a single GitHub release, and attaches `install.sh`. Never tag a
single program separately: there are no per-program versions.

## CI cost

The repository is public, so standard GitHub-hosted runners do not consume
paid minutes (the API reports `billable.UBUNTU.total_ms: 0`). The workflow is
still kept small:

- one Linux job per run, no OS matrix;
- Markdown-only changes run `--fast`; everything else runs the full gate;
- `concurrency` cancels superseded runs on the same ref;
- `timeout-minutes` caps runaway jobs;
- releases cross-compile all targets on one runner and upload no Actions
  artifacts: assets go straight to the GitHub release;
- `actions/setup-go` caches modules and build output, keyed by `go.sum`.

Measured on this repository: ~3.5 min cold, ~1 min warm, and the docs-only
path only runs formatting, tidy and the catalog check.
