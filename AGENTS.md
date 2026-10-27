# AGENTS.md — working on the AX ecosystem

This repository is the whole AX ecosystem: one Go module, one set of
conventions, one release. Programs are independent binaries but they share the
wire contracts in `internal/axproto` and the Botdir helpers in
`internal/botdir`.

## Non-negotiables

- **The process contracts are public.** `docs/protocol.md` (JSONL events) and
  `docs/tools.md` (describe/run) are consumed by every transport, bridge and
  provider. Change the document and the code together; never break v1
  semantics silently.
- **Botdir is the directory contract.** Consumers read `bot.toml`, `bot.md`,
  `skills/` from the bot root and runtime data from `BOT_DATA`; providers work
  in the resolved workspace. Use `internal/botdir` instead of open-coding
  `BOT_ROOT`/`BOT_DATA`.
- **One binary per `cmd/` directory, no cross-imports.** Shared behavior goes in
  `internal/`, not from one `cmd` package into another.
- **No per-program CI, Makefile or packaging script.** Add the program to
  `PROGRAMS` and `catalog.json`; the root tooling handles the rest.
- **No per-program versions.** One tag releases the ecosystem.

## Layout

- `cmd/<program>/` — `package main`, README, tests.
- `internal/axproto` — event types, outcomes, tool descriptors.
- `internal/botdir` — Botdir paths, config and secrets.
- `docs/` — protocol, tools, Botdir spec.
- `scripts/` — installer, packaging, harnesses; take arguments, don't copy.
- `catalog.json` — program registry; `scripts/render-catalog.py` renders the
  README table.

## Working on a change

1. Edit the code and the matching docs/README.
2. `gofmt -w .`, then `make check` (gofmt, tidy, vet, tests, race, builds,
   size budgets, catalog check, harness).
3. Commit on a topic branch and open a pull request; `main` only moves through
   pull requests.

Never copy a script from one program into another: extend the root script.
Never add a `go.mod` under `cmd/`.

## Releases

`git tag vX.Y.Z && git push origin vX.Y.Z`. The workflow cross-compiles all
programs for Linux x86-64/ARM64 and macOS ARM64 on one Linux runner, publishes
one GitHub release and attaches `install.sh`. The installer downloads `<program>-<os>-<arch>` from that
release; `INSTALL_BASE_URL` overrides the location for local testing.
