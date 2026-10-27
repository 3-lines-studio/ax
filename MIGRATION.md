# Migration notes

The AX ecosystem used to be sixteen repositories, each with its own
`go.mod`, Makefile, quality workflow, release workflow, packaging script and
tag. That made every cross-cutting change a fifteen-repository change and let
the shared contracts drift. Everything now lives here, with one module and one
release.

This file records what came from where and what a consumer of the old repos
needs to know.

## Imported repositories

Every repository was imported at the commit below, with its README, tests and
test fixtures. The source histories remain on GitHub; this repository is the
only place development continues.

| Program | Previous repository | Imported commit | Last tag |
| --- | --- | --- | --- |
| ax | `3-lines-studio/ax` | `ab3c380` | v0.4.10 |
| boti | `3-lines-studio/boti` | `d1bbaf4` | v0.1.2 |
| slaxi | `3-lines-studio/slaxi` | `8832828` | v0.3.3 |
| acpi | `3-lines-studio/acpi` | `f35d509` | v0.1.0 |
| mcpx | `3-lines-studio/mcpx` | `9f6b771` | v0.1.0 |
| fsx | `3-lines-studio/fsx` | `208fe98` | v0.1.3 |
| bashx | `3-lines-studio/bashx` | `c6aeee9` | v0.2.6 |
| attachx | `3-lines-studio/attachx` | `7b37846` | v0.1.2 |
| browserx | `3-lines-studio/browserx` | `3594441` | v0.2.2 |
| wax | `3-lines-studio/wax` | `27b641d` | v0.3.2 |
| skillx | `3-lines-studio/skillx` | `a42b466` | v0.1.2 |
| slackx | `3-lines-studio/slackx` | `763f5a4` | v0.2.1 |
| bqx | `3-lines-studio/bqx` | `33ad502` | v0.3.2 |
| pgx | `3-lines-studio/pgx` | `51117b1` | v0.2.1 |
| Botdir spec | `3-lines-studio/botdir` | `7975f10` | — |
| Catalog | `3-lines-studio/ax-ecosystem` | `aef67c0` | — |

The catalog repository was folded into `catalog.json` plus
`scripts/render-catalog.py`; the scheduled version-refresh workflow is gone
because all programs now share one version.

## What changed

- **One Go module.** `module github.com/3-lines-studio/ax`, with every program
  under `cmd/<program>`. There are no nested `go.mod` files and
  `go.work` is not needed.
- **One version.** All programs are released together from one `vX.Y.Z` tag.
  One Linux runner cross-compiles `<program>-<os>-<arch>` for every program
  and target, and the release attaches a single `install.sh`; no Actions
  artifacts are stored.
- **One installer.** The release attaches `install.sh` and every program's
  artifacts to `github.com/3-lines-studio/ax/releases`; the README, boti and
  the catalog point there. The old `ax.3lines.studio` mirror is optional and no
  longer required by this repository. `INSTALL_BASE_URL` still points it
  at a local mirror, now flat (all artifacts in one directory) instead of one
  directory per program.
- **Shared contracts.** `internal/axproto` holds the event names, outcomes and
  tool descriptor used by the engine, bridges and transport.
  `internal/botdir` holds the `BOT_ROOT`/`BOT_DATA` path, `bot.toml` and secret
  helpers used by ax, boti, slaxi, fsx and bashx.
- **One quality gate.** `scripts/check.sh` (also `make check`) runs gofmt, tidy,
  vet, tests, race, builds, per-binary size budgets, the catalog check, the
  provider contract check and the ax harness. The per-program workflows,
  Makefiles and `scripts/package.sh` copies were deleted.
- **The botdir spec moved** to `docs/botdir/`, next to the code that implements
  it.

## Compatibility

- Binary names, flags, environment variables and wire protocols are unchanged.
  The only code-level moves are the shared helpers; behavior is covered by the
  imported tests.
- Old release URLs keep working while the old repositories and their tags
  exist. They are frozen at the versions listed above.
- `go install` paths gain a `/cmd/<program>` segment:
  `go install github.com/3-lines-studio/ax/cmd/ax@vX.Y.Z`.
  `go install github.com/3-lines-studio/pgx@v0.2.1` (used by `megingjord`'s
  image) keeps resolving from the old repository; new versions use
  `go install github.com/3-lines-studio/ax/cmd/pgx@vX.Y.Z`.
- The old `ax-ecosystem` README is superseded by the root README; links to
  `ax-ecosystem/blob/main/BOTDIR.md` in downstream docs should point at
  `ax/docs/botdir/BOTDIR.md`.

## Retiring the old repositories

Once the first monorepo release is published, the sixteen imported
repositories can be archived on GitHub with a note pointing here. Archiving
keeps their tags and release assets readable, so old installs keep working.
