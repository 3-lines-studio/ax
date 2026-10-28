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

`acpi` and `mcpx` were imported and then removed: the ACP path is retired,
and MCP is native to `ax` now (servers are configured under `[[mcp]]` in
`bot.toml`; there is no bridge binary). Bots that still list `mcpx` in
`AX_TOOLS` or `tools` can drop it.

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
  tool descriptor used by the engine and transport. `internal/botdir` holds
  the `BOT_ROOT`/`BOT_DATA` path, `bot.toml` and secret helpers used by ax,
  boti, slaxi, fsx and bashx.
- **MCP-first catalog.** `ax` now indexes MCP servers and local providers and
  exposes two tools, `search` and `execute`; `mcpx` and `acpi` were removed.
  `bot.toml` gained `[[mcp]]`, `tool_mode` and `core_tools`. The old
  per-provider surface is available for one release with
  `-tool-mode direct`. Remote servers authenticate with a token header or with
  `ax mcp login` (OAuth, PKCE, tokens in `state/ax/mcp/auth/`);
  `ax mcp add`/`ax mcp import` connect registry servers or an existing
  `mcp.json`/Cursor plugin. See `docs/mcp.md`.
- **One quality gate.** `scripts/check.sh` (also `make check`) runs gofmt, tidy,
  vet, tests, race, builds, per-binary size budgets, the catalog check, the
  provider contract check and the ax harness. The per-program workflows,
  Makefiles and `scripts/package.sh` copies were deleted.
- **The botdir spec moved** to `docs/botdir/`, next to the code that implements
  it.

## Engine changes after the import

The ax engine kept moving while the repositories were merged:

- **DeepSeek is the default provider.** `base_url` defaults to
  `https://api.deepseek.com` and `model` to `deepseek-flash`; the API key is
  read from `DEEPSEEK_API_KEY` before `OPENAI_API_KEY`. Existing bots that set
  `base_url`/`model` in `bot.toml` or flags are unaffected. Set
  `OPENAI_API_KEY`/`-base`/`-model` as before for other providers.
- **Thinking is on by default for DeepSeek, at `reasoning_effort: max`.**
  `-thinking auto|on|off`,
  `-effort low|high|max` (`none` disables) and `-max-tokens` control it;
  `bot.toml` gained `thinking`, `reasoning_effort` and `max_tokens`.
- **Images.** `-image PATH|URL` (repeatable) and `-image-detail` add vision
  input; images are content-addressed blobs next to the session log.
- **Durable sessions.** `--session FILE` is now an append-only, fsynced JSONL
  log that resumes and repairs interrupted runs instead of a convenience dump.
  See `docs/session.md`.
- **New event and descriptor fields.** `reasoning_delta` was added to the
  event stream and `sequential`, `timeout` and `replay` are accepted in tool
  descriptors. All are additive; protocol version stays 1, and clients may
  ignore unknown events and fields.

## Compatibility

- Binary names, existing flags, environment variables and wire protocols keep
  their meaning. Changes to defaults and new fields are listed above; the
  protocol stays at version 1 and the added event and descriptor fields are
  optional.
- Bots that list `mcpx` in `tools`/`AX_TOOLS` should drop it and move those
  servers to `[[mcp]]`; `acpi` users should point their MCP host at
  `ax mcp serve` instead.
- Sessions written in direct mode contain provider tool calls. Resume them
  with `-tool-mode direct`, or start a new session; catalog sessions use
  `search` and `execute`.
- Old release URLs keep working while the old repositories and their tags
  exist. They are frozen at the versions listed above.
- `go install` paths gain a `/cmd/<program>` segment:
  `go install github.com/3-lines-studio/ax/cmd/ax@vX.Y.Z`; old module paths
  keep resolving from the frozen repositories.
- The old `ax-ecosystem` README is superseded by the root README; links to
  `ax-ecosystem/blob/main/BOTDIR.md` in downstream docs should point at
  `ax/docs/botdir/BOTDIR.md`.

## Retiring the old repositories

The fifteen imported repositories are **archived** on GitHub, with their
description pointing here: `boti`, `slaxi`, `acpi`, `mcpx`, `fsx`, `bashx`,
`attachx`, `browserx`, `wax`, `skillx`, `slackx`, `bqx`, `pgx`, `botdir` and
`ax-ecosystem`. Archiving keeps their tags and release assets readable, so old
installs keep working; development happens only in this repository.

`3-lines-studio/ax` is not archived: the monorepo took over that name, and its
earlier tags (`v0.4.10` and below) still resolve.

## Installing after the merge

A release is one archive per platform (`ax-linux-x86_64.tar.gz`,
`ax-linux-aarch64.tar.gz`, `ax-darwin-aarch64.tar.gz`) plus `SHA256SUMS` and
`install.sh`; the per-program `<program>-<os>-<arch>` assets are gone. Old
release URLs keep serving them from the frozen tags.
