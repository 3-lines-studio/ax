# AX ecosystem

<p align="center"><img src=".github/ax.svg" width="96" height="96" alt="AX ecosystem"></p>

Small Unix programs that compose through process boundaries: a headless agent
engine, tool providers, a transport, a host loader, and an MCP gateway,
wired together by the [Botdir](docs/botdir/BOTDIR.md) portable directory
interface.

Everything lives in this repository: one Go module, one build, one test
suite, one release. Every program is still a single static binary.

## Install

The ecosystem is one product: a release is **one archive per platform** with
every program inside, so installing the whole set costs the same as installing
one tool.

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh
```

That installs every program into `~/.local/bin`. Name the ones you want if you
would rather not have all of them, or manage an existing install:

```sh
sh install.sh ax fsx bashx    # only these
sh install.sh update          # re-fetch, replace only what changed
sh install.sh uninstall ax    # remove
```

`AX_PREFIX` (or `PREFIX`) moves the install root, `VERSION` pins a tag, and
`INSTALL_BASE_URL` points at a mirror, which is also how the installed
`install.sh` is tested locally. Linux x86-64/ARM64 and macOS ARM64, all
SHA-256 verified against the release's `SHA256SUMS`.

From a checkout:

```sh
make build          # bin/<program> for all programs
make install        # ~/.local/bin, override with PREFIX=/usr/local
```

## Start with the UI

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh
webi setup
```

`setup` starts the UI at login, opens it and asks one question: whether you
want to use it from your phone too. There is no folder or port to choose. The
first page asks for a vault PIN (8+ characters) and an API key, once, and then
it is just chat.

It works on Linux (systemd user unit) and macOS (LaunchAgent). On another
system, run `webi` in a terminal or add it to your login items. For a bot in a
project directory, `webi -C ~/project` (or add `-C` to `setup`).

To update, re-run the installer with `update` and restart the UI; the vault
asks for the PIN once. The supervised `boti serve` path (builds, rollback and
the key kept across restarts) is in [docs/deploy.md](docs/deploy.md).

## CLI quick start

In a bot directory, declare the local providers in `bot.toml`:

```toml
[[connection]]
id = "fsx"
kind = "local"
command = "fsx"

[[connection]]
id = "bashx"
kind = "local"
command = "bashx"
```

```sh
DEEPSEEK_API_KEY=... ax "explain this repository"
ax catalog                     # what is on the plate
```

`ax` defaults to DeepSeek (`deepseek-flash` at `https://api.deepseek.com`) and
accepts any OpenAI-compatible endpoint with `-base` (OpenAI, OpenRouter, Ollama,
vLLM). Capabilities come from a catalog: local providers and MCP servers are
`[[connection]]` entries in `bot.toml` (MCP servers can be added with `ax mcp add`
or imported from an `mcp.json`/Cursor plugin, OAuth included). The model sees two tools, `search`
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
| [webi](cmd/webi/README.md) | Browser UI | Serves the ax engine as a web app: channels are bot directories, threads are ax sessions. |

### Tool providers

| Program | Role | Description |
| --- | --- | --- |
| [wax](cmd/wax/README.md) | Web search and fetch | Searches the web (DeepSeek Search) and fetches pages as Markdown, for ax and other LLM tools. |
| [bqx](cmd/bqx/README.md) | BigQuery tool | Read-only BigQuery queries for ax. |
| [pgx](cmd/pgx/README.md) | PostgreSQL tool | Read-only PostgreSQL queries for ax. |
| [slackx](cmd/slackx/README.md) | Slack file-upload tool | Uploads files to Slack for ax. |
| [browserx](cmd/browserx/README.md) | Chromium tool | Visible Chromium browser tool for ax. |
| [bashx](cmd/bashx/README.md) | Bash tool | Runs non-interactive shell commands for ax. |
| [fsx](cmd/fsx/README.md) | Filesystem tools | Reads, writes and edits files inside the workspace. |
| [sshx](cmd/sshx/README.md) | Remote filesystem and shell | Reads, writes, edits and runs commands on a remote host over SSH, so a bot can work on a remote workspace with the same tool names as fsx and bashx. |
| [skillx](cmd/skillx/README.md) | Agent Skills provider | Exposes Agent Skills to ax. |
| [memoryx](cmd/memoryx/README.md) | Personal memory | Durable personal facts as readable JSON files under state/memory, exposed as search/get/verify/upsert/delete tools for ax. |
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
  Consumers, hosts and providers derive `workspace/`, `skills/`, `routines/`,
  `state/`, `run/` and `secrets/` from the bot root by convention.
- **Routines** are deterministic executables under `routines/`; `webi` runs
  enabled cron schedules with no model in the loop. See
  [docs/routines.md](docs/routines.md).

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
make dist           # loose binaries for the current platform into dist/
scripts/package.sh --release all   # the three release archives + SHA256SUMS
```

Releases are one tag for the whole ecosystem. Pushing `vX.Y.Z` builds every
program for Linux x86-64/ARM64 and macOS ARM64, packs each platform into one
`ax-<os>-<arch>.tar.gz`, and publishes the three archives, `SHA256SUMS` and
`install.sh` as a single GitHub release. All programs share that version: there
are no per-program tags, and no per-program assets to map by name.

## Docs

- [docs/protocol.md](docs/protocol.md) — ax's JSONL event stream.
- [docs/tools.md](docs/tools.md) — the provider describe/run convention.
- [docs/mcp.md](docs/mcp.md) — the MCP-first catalog, search/execute and config.
- [docs/connections.md](docs/connections.md) — connections, capabilities and the
  local/remote compute plan.
- [docs/session.md](docs/session.md) — the durable session log contract.
- [docs/memory.md](docs/memory.md) — personal memory as runtime data and the
  `memoryx` tools.
- [docs/routines.md](docs/routines.md) — deterministic routines: executable
  entries, cron schedules and run logs, with no model in the loop.
- [docs/deploy.md](docs/deploy.md) — systemd, container, and what goes in front.
- [docs/botdir/](docs/botdir/README.md) — the Botdir specification and guide.
- [CONTRIBUTING.md](CONTRIBUTING.md) — development workflow and conventions.
- [MIGRATION.md](MIGRATION.md) — where each program came from, and what changed
  when the ecosystem was consolidated into this repository.
