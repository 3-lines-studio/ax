# Botdir

A portable directory interface for bots.

## Terms

The words **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT**, and **MAY** are normative.

A **bot** is a directory. A **consumer** is a program that reads or operates a bot. A **host** prepares a bot and starts consumers.

## Core contract

A consumer MUST start with the bot directory as its current working directory. That directory is the bot root.

The bot root holds the portable definition. A host MAY provide a **runtime data root** separate from the bot root; it holds the runtime paths `state/`, `workspace/`, and `run/`. When no runtime data root is provided, the runtime paths live at the bot root.

An empty directory is a valid bot. Every standard path is optional.

A consumer MUST ignore unknown paths and unknown `bot.toml` keys.

Standard path names are case-sensitive lowercase ASCII. A standard path MUST NOT have another meaning.

A standard path MAY be a mount point or symbolic link. This specification does not restrict its filesystem, storage backend, or implementation. A path only needs to provide the filesystem behavior required by its consumers.

## Runtime data root

A host MAY provide a separate **runtime data root** for the runtime paths. It is communicated to consumers as the environment variable `BOT_DATA` (an absolute path). The definition paths — `bot.toml`, `bot.md`, `skills/`, and `resources/` — always live at the bot root. When `BOT_DATA` is unset, consumers resolve the runtime paths at the bot root.

| Path | Class | Root |
| --- | --- | --- |
| `bot.toml`, `bot.md`, `skills/`, `resources/` | Definition | bot root |
| `state/`, `workspace/`, `run/` | Runtime | `BOT_DATA` (default bot root) |
| `secrets/` | Volatile | host-provided, never persisted |

A runtime consumer MUST resolve each runtime path relative to `BOT_DATA` when it is set, and relative to the bot root otherwise. A host loader SHOULD export `BOT_DATA` for the consumers it starts, defaulting it to the bot root when unset.

`secrets/` is host-provided and **volatile**. A host MUST NOT persist secrets in the runtime data root. It provides secrets through the environment or a non-persistent mount; the `secrets/` standard path, when present, is an ephemeral host view. A runtime consumer MUST NOT write secrets anywhere.

## Standard paths

```text
.
├── bot.toml
├── bot.md
├── skills/
├── resources/
├── state/
├── workspace/
├── secrets/
└── run/
```

| Path | Meaning | Normal access | Lifecycle |
| --- | --- | --- | --- |
| `bot.toml` | Bot configuration | Read-only | Portable definition |
| `bot.md` | Bot instructions | Read-only | Portable definition |
| `skills/` | Agent Skills | Read-only | Portable definition |
| `resources/` | Other bot-provided input | Read-only | Portable definition |
| `state/` | Machine-managed runtime data | Read/write | Durable |
| `workspace/` | User-visible input, work, and output | Read/write | Durable |
| `secrets/` | Secret runtime input | Read-only | Host-provided |
| `run/` | Process data | Read/write | Ephemeral |

A host MAY grant a consumer one or more roles:

| Role | Write access |
| --- | --- |
| Runtime | `state/`, `workspace/`, `run/` |
| Definition editor | Definition paths |
| Secret manager | `secrets/` |

A consumer MUST write only to paths allowed by its granted roles. Normal runtime consumers MUST NOT mutate definition paths or `secrets/`.

Runtime consumers MAY create `state/`, `workspace/`, and `run/` when needed. They MUST NOT create absent definition paths.

## Definition

### `bot.toml`

`bot.toml` is optional UTF-8 TOML. All keys are optional.

```toml
model = "gpt-5"
base_url = "https://api.openai.com/v1"
tools = ["wax", "fsx"]
```

`base_url` identifies an OpenAI-compatible API endpoint.

Each `tools` entry is one executable name resolved through `PATH`. It MUST NOT contain arguments. Consumers MUST preserve order and ignore duplicate entries after the first.

`[[connection]]` declares one integration as a unit: an id the model sees, a
kind and how to reach it. Consumers MUST treat the id as the tool namespace
(`<id>.<tool>`); tools are discovered from the connection, never listed in
`bot.toml`. `tools` and `[[mcp]]` are legacy spellings of the same thing and
keep their ids.

```toml
[[connection]]
id           = "web"
kind         = "local"          # local | ssh | mcp-stdio | mcp-http
command      = "wax"
trust        = "auto"           # auto | read
compute      = false            # true: defines the bot's workspace and core tools
root         = "vol"            # workdir; relative to the bot root (remote path for ssh)
capabilities = ["web.search"]   # optional allowlist
```

Ids are lowercase letters, digits, `_` and `-` (1–64 characters) and unique
across the bot. `capabilities` only narrows what a connection declares; an empty
list enables every tool. `trust = "read"` keeps only tools annotated read-only. See [docs/connections.md](../connections.md).

A missing key uses the consumer default.

Root keys are reserved by Botdir. Consumer-specific configuration MUST use a top-level table named after that consumer:

```toml
[slack]
mention_only = true
```

A consumer MUST ignore tables it does not own and unknown keys for forward compatibility.

A host loader MAY read a top-level `[env]` table and export each entry as an
environment variable for the consumer. Values are literal strings; host
environment wins. `env` is not a reserved root key and is ignored by
consumers:

```toml
[env]
WAX_NO_SANDBOX = "1"
```

A host loader starts all consumers. The **`consumer`** key names the primary
consumer to launch (one executable name resolved through `PATH`); a missing key
defaults to the host's default (the AX ecosystem defaults to `slaxi`):

```toml
consumer = "slaxi"
```

A host loader MAY run **`pre_run`** hooks before starting the consumer. Each
`command` is executed through a shell with the bot environment set, in order; a
non-zero exit aborts startup unless the command handles it. Hooks are host-owned
setup, not consumer behavior:

```toml
[[pre_run]]
command = "refresh-skill --name metrics"
```

The model API key is a single provider-independent name, `OPENAI_API_KEY`;
`base_url` selects the endpoint. Hosts provide the key as that environment
variable or as `secrets/api-key`.

### `bot.md`

`bot.md` is optional UTF-8 Markdown containing free-form bot instructions. Consumers MUST treat it literally. This version defines no includes, variables, or interpolation.

### `skills/`

`skills/` optionally contains skills using the external Agent Skills format.

### `resources/`

`resources/` optionally contains read-only text or binary input supplied with the bot. It has no standard internal layout. Consumers MUST NOT automatically add all resources to model context.

## Runtime

### `state/`

`state/` contains private, durable, machine-managed data.

`state/` resolves relative to `BOT_DATA` when set, otherwise relative to the bot root.

Each consumer runtime MUST own a lowercase namespace such as `state/ax/`. Other consumers MUST treat it as opaque unless they implement the same state protocol.

State MUST survive normal stops, restarts, and definition updates. Its deletion MUST be explicit.

Consumers MUST make state writes safe against interruption.

### `workspace/`

`workspace/` contains private, durable, user-visible input, working files, and output. It has no standard internal layout.

`workspace/` resolves relative to `BOT_DATA` when set, otherwise relative to the bot root.

Workspace data MUST survive normal stops, restarts, and definition updates. Its deletion MUST be explicit and separate from state deletion.

### `secrets/`

Each secret MUST be one regular file directly under `secrets/`. Its name MUST use lowercase ASCII kebab-case. Its content is an exact sequence of arbitrary bytes.

The standard model API key path is `secrets/api-key`.

`secrets/` is **volatile**: the host provides secrets through the environment or a non-persistent mount, and MUST NOT persist them in the runtime data root. The `secrets/` path, when present, maps to that ephemeral host view.

A normal runtime consumer MUST NOT modify or persist secrets. A missing required secret MUST fail readiness without exposing secret values.

The host chooses the secret source and provides the runtime view.

### `run/`

`run/` contains private data for one run, such as sockets, locks, process identifiers, and short-lived shared files.

`run/` resolves relative to `BOT_DATA` when set, otherwise relative to the bot root.

Each consumer runtime MUST use a lowercase namespace such as `run/ax/`.

`run/` MUST be empty at the start of a run. Consumers MUST NOT require its contents to survive a stop, crash, or restart.

## Process contract

The host starts a consumer from the bot root:

```sh
cd "$bot"
exec consumer
```

No bot-specific environment variable, command, argument, output protocol, transport, or supervisor is required.

Several consumers MAY operate one bot concurrently. A consumer MUST NOT mutate another consumer's `state/` or `run/` namespace unless both implement the same protocol.

The host MUST preserve state and workspace data unless explicitly told to erase them. It MUST remove stale run data before a new run.

## Compatibility

New standard paths MUST initially be optional.

A standard path's meaning MUST NOT change within a major specification version.

An optional path MUST NOT become required within a major version.

A consumer runtime owns the format and migration of its state namespace.

## Conformance

A bot conforms when its present standard paths follow this specification. An empty directory conforms.

A consumer conforms when it:

- uses its initial current working directory as the bot root
- respects standard path meanings and access rules
- resolves runtime paths relative to `BOT_DATA` when set, otherwise the bot root
- ignores unknown paths and `bot.toml` keys
- namespaces its state and run data
- preserves durable data
- does not write secrets to persisted storage

A host conforms when it:

- starts consumers from the bot root
- exports `BOT_DATA` for consumers, defaulting to the bot root
- provides required secrets without persisting them in the runtime data root
- preserves state and workspace data
- removes stale run data before a new run
