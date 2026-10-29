# AX

A headless agent engine (`ax`), a bot host (`boti`), transports (`webi`, `slaxi`) and tool providers (`fsx`, `bashx`, `skillx`, `memoryx`, `wax`, …) that compose through process boundaries. One Go module, one test suite, one release; every program is a single static binary.

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh
```

That installs every program into `~/.local/bin`. Name the ones you want, or manage an existing install:

```sh
sh install.sh ax fsx bashx
sh install.sh update
sh install.sh uninstall ax
```

From a checkout: `make build` puts the binaries in `bin/`, `make install` copies them to `~/.local/bin` (override with `PREFIX`).

## Quick start

```sh
webi setup                                   # browser UI, first run asks for a PIN and an API key
DEEPSEEK_API_KEY=... ax "explain this repository"
```

`ax` defaults to DeepSeek (`deepseek-flash`) and speaks any OpenAI-compatible endpoint with `-base` (OpenAI, OpenRouter, Ollama, vLLM). Tools come from `[[connection]]` entries in a bot's `bot.toml`:

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

The model always sees two tools, `search` and `execute`, so integrations never grow the prompt. MCP servers are added with `ax mcp add` or imported from an `mcp.json`.

## Programs

<!-- programs:begin -->

### Engine

| Program | Description |
| --- | --- |
| `ax` | A minimal headless LLM coding-agent harness for scripts and Unix-style clients. |

### Bot host

| Program | Description |
| --- | --- |
| `boti` | Reads bot.toml, installs the declared binaries, wires the Botdir environment, runs the hooks and starts the consumer from the bot root. |

### Transports

| Program | Description |
| --- | --- |
| `slaxi` | Connects the ax engine to Slack: listens for mentions, runs the bot and posts replies back. |
| `webi` | Serves the ax engine as a web app: channels are bot directories, threads are ax sessions. |

### Tool providers

| Program | Description |
| --- | --- |
| `wax` | Searches the web (DeepSeek Search) and fetches pages as Markdown, for ax and other LLM tools. |
| `bqx` | Read-only BigQuery queries for ax. |
| `pgx` | Read-only PostgreSQL queries for ax. |
| `slackx` | Uploads files to Slack for ax. |
| `browserx` | Visible Chromium browser tool for ax. |
| `bashx` | Runs non-interactive shell commands for ax. |
| `fsx` | Reads, writes and edits files inside the workspace. |
| `sshx` | Reads, writes, edits and runs commands on a remote host over SSH, so a bot can work on a remote workspace with the same tool names as fsx and bashx. |
| `skillx` | Exposes Agent Skills to ax. |
| `memoryx` | Durable personal facts as readable JSON files under state/memory, exposed as search/get/verify/upsert/delete tools for ax. |
| `attachx` | Copies workspace files into the bot's artifact directory so a transport can deliver them to the user. |

<!-- programs:end -->

## Layout

```text
cmd/<program>/      one binary per directory, with its tests
internal/           shared contracts: events, Botdir paths, catalog, MCP client
scripts/            installer, packaging, black-box harnesses
catalog.json        program registry that renders the table above
```

A bot is a directory with `bot.toml`; `boti` installs what it declares, wires the environment and starts the consumer from the bot root.

## Develop

```sh
make build
make test
make check           # gofmt, tidy, vet, tests, race, builds, size budgets, harness
make dist
```

A release is one tag for the whole ecosystem: `vX.Y.Z` cross-compiles every program for Linux x86-64/ARM64 and macOS ARM64, packs one `ax-<os>-<arch>.tar.gz` per platform, and publishes them with `SHA256SUMS` and `install.sh`.
