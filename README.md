# AX ecosystem

<p align="center"><img src=".github/ax.svg" width="96" height="96" alt="AX ecosystem"></p>

Small Unix programs that compose through process boundaries: a headless agent
engine, tool providers, a transport, a host loader, and an MCP gateway,
wired together by the [Botdir](docs/botdir/BOTDIR.md) portable directory
interface.

Everything lives in this repository: one Go module, one build, one test
suite, one release. Every program is still a single static binary.

## Install

One installer serves every program in the ecosystem:

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh
```

Install specific programs by name:

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- fsx bashx skillx
```

From a checkout:

```sh
make build          # bin/<program> for all programs
make install        # ~/.local/bin, override with PREFIX=/usr/local
```

Linux x86-64/ARM64 and macOS ARM64, all SHA-256 verified.

## Quick start

```sh
DEEPSEEK_API_KEY=... ax "explain this repository"
AX_TOOLS="fsx bashx skillx" ax
ax catalog                     # what is on the plate
```

`ax` defaults to DeepSeek (`deepseek-flash` at `https://api.deepseek.com`) and
accepts any OpenAI-compatible endpoint with `-base` (OpenAI, OpenRouter, Ollama,
vLLM). Capabilities come from a catalog: local providers (`AX_TOOLS`) and MCP
servers (`[[mcp]]` in `bot.toml`, added with `ax mcp add` or imported from an
`mcp.json`/Cursor plugin, OAuth included). The model sees two tools, `search`
and `execute`, so adding integrations does not grow the prompt. See
[docs/mcp.md](docs/mcp.md).

## Programs

<!-- programs:begin -->

### Engine

| Program | Role | Description |
| --- | --- | --- |
| [ax](cmd/ax/README.md) | Headless agent engine | A minimal headless LLM coding-agent harness for scripts and Unix-style clients. |

### Host loader

| Program | Role | Description |
| --- | --- | --- |
| [boti](cmd/boti/README.md) | Botdir host loader | Reads bot.toml, installs the declared binaries, wires the Botdir environment, runs the hooks and starts the consumer from the bot root. |

### Transport

| Program | Role | Description |
| --- | --- | --- |
| [slaxi](cmd/slaxi/README.md) | Slack transport | Connects the ax engine to Slack: listens for mentions, runs the bot and posts replies back. |

### Tool providers

| Program | Role | Description |
| --- | --- | --- |
| [wax](cmd/wax/README.md) | Web-fetch tool | Fetches a web page as Markdown for ax and other LLM tools. |
| [bqx](cmd/bqx/README.md) | BigQuery tool | Read-only BigQuery queries for ax. |
| [pgx](cmd/pgx/README.md) | PostgreSQL tool | Read-only PostgreSQL queries for ax. |
| [slackx](cmd/slackx/README.md) | Slack file-upload tool | Uploads files to Slack for ax. |
| [browserx](cmd/browserx/README.md) | Chromium tool | Visible Chromium browser tool for ax. |
| [bashx](cmd/bashx/README.md) | Bash tool | Runs non-interactive shell commands for ax. |
| [fsx](cmd/fsx/README.md) | Filesystem tools | Reads, writes and edits files inside the workspace. |
| [skillx](cmd/skillx/README.md) | Agent Skills provider | Exposes Agent Skills to ax. |
| [attachx](cmd/attachx/README.md) | Conversation attachment tool | Copies workspace files into the bot's artifact directory so a transport can deliver them to the user. |

### Specification

| Program | Role | Description |
| --- | --- | --- |
| [botdir](docs/botdir/BOTDIR.md) | Botdir specification | The portable directory interface for bots. Not a binary: every other piece conforms to it. |

<!-- programs:end -->

## How it fits together

```text
LLM host ──▶ ax ──┬──▶ provider describe / provider run (fsx, bashx, wax, …)
                  └──▶ MCP servers (stdio, Streamable HTTP)
```

- The **engine** (`ax`) runs one prompt, emits JSONL events on stdout and has
  no built-in tools. The contract is [docs/protocol.md](docs/protocol.md).
- **Providers** implement the Unix tool convention: `<command> describe`
  prints tool descriptors, `<command> run <name>` executes one call. The
  contract is [docs/tools.md](docs/tools.md).
- **MCP** is the integration path: `ax` indexes configured MCP servers and
  exposes two tools, `search` and `execute`, instead of a tool list. Providers
  in the local describe/run convention are indexed the same way, so existing
  integrations keep working.
- **`ax mcp serve`** exposes that same catalog to any MCP host; `slaxi` speaks
  Slack on one side and ax's process protocol on the other.
- **boti** is the host loader: a bot is a directory with `bot.toml`, and `boti`
  installs what it declares, wires the environment and execs the consumer.
  Consumers, hosts and providers derive `workspace/`, `skills/`, `state/`,
  `run/` and `secrets/` from the bot root by convention.

## Repository layout

```text
cmd/<program>/      one binary per directory, with its README and tests
internal/axproto/   event stream and tool descriptor types shared by the programs
internal/botdir/    Botdir path, config and secret helpers shared by consumers
internal/catalog/   tool index, ranking and Markdown rendering for search/execute
internal/mcp/       stdlib MCP client (stdio and Streamable HTTP)
docs/               protocol, tool convention, MCP catalog, Botdir specification
scripts/            installer, packaging, black-box harnesses
catalog.json        machine-readable program registry (renders the table above)
```

## Build, test, release

```sh
make build          # all binaries into bin/
make bin/fsx        # one binary
make test           # go test ./...
make check          # gofmt, tidy, vet, tests, race, builds, size budgets, harness
make dist           # release artifacts for the current platform into dist/
```

Releases are one tag for the whole ecosystem. Pushing `vX.Y.Z` builds every
program for Linux x86-64/ARM64 and macOS ARM64, uploads the artifacts to a
single GitHub release and attaches `install.sh`. All programs share that
version: there are no per-program tags.

## Docs

- [docs/protocol.md](docs/protocol.md) — ax's JSONL event stream.
- [docs/tools.md](docs/tools.md) — the provider describe/run convention.
- [docs/mcp.md](docs/mcp.md) — the MCP-first catalog, search/execute and config.
- [docs/session.md](docs/session.md) — the durable session log contract.
- [docs/botdir/](docs/botdir/README.md) — the Botdir specification and guide.
- [CONTRIBUTING.md](CONTRIBUTING.md) — development workflow and conventions.
- [MIGRATION.md](MIGRATION.md) — where each program came from, and what changed
  when the ecosystem was consolidated into this repository.
