# Botdir Guide

Version 0 draft

## 1. Terms

The key words **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT**, and **MAY** are normative.

A **bot** is a directory.

A **consumer** is a program that reads or operates a bot.

A **host** prepares a bot and starts consumers. A host may be a shell, laptop, server, container, or other system.

The directory from which a consumer starts is the **bot root**.

## 2. Scope

This specification defines:

- standard bot paths
- path ownership and lifecycle
- consumer startup
- compatibility rules

It does not define:

- agent behavior
- model or tool protocols
- transports
- orchestration
- scheduling
- remote APIs
- authentication
- source control
- backup policy
- secret backends
- storage backends
- user interfaces

The specification is independent of AX and any specific consumer or host.

## 3. Core contract

A bot MUST be a directory.

A consumer MUST treat its initial current working directory as the bot root.

No bot-specific environment variable is required.

An empty directory is a valid bot.

Every standard path is optional. A consumer MUST define the behavior of any standard path it requires when that path is absent.

A consumer MUST ignore unknown files, directories, and unknown `bot.toml` keys.

Standard path names are case-sensitive lowercase ASCII names. A standard path MUST NOT be used with another meaning.

A standard path MAY be a mount point or symbolic link. The specification does not require all paths to use the same filesystem or storage backend. Every path only needs to provide the filesystem behavior required by the consumers that use it.

Nested directories have no implicit bot meaning. A host selects bot roots explicitly.

### Runtime data root

A host MAY provide a separate **runtime data root** for the runtime paths. That location is communicated to consumers as the environment variable `BOT_DATA` (an absolute path). When `BOT_DATA` is unset, consumers resolve the runtime paths at the bot root.

The definition paths — `bot.toml`, `bot.md`, `skills/`, and `resources/` — always live at the bot root.

| Path | Class | Root |
| --- | --- | --- |
| `bot.toml`, `bot.md`, `skills/`, `resources/` | Definition | bot root |
| `state/`, `workspace/`, `run/` | Runtime | `BOT_DATA` (default bot root) |
| `secrets/` | Volatile | host-provided, never persisted |

A runtime consumer MUST resolve each runtime path relative to `BOT_DATA` when it is set, and relative to the bot root otherwise. A host loader SHOULD export `BOT_DATA` for the consumers it starts, defaulting it to the bot root when unset.

This separates the immutable definition from the mutable per-instance data, so a host can mount one persistent volume at `BOT_DATA` without shadowing the definition and without persisting secrets.

## 4. Standard paths

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

| Path | Class | Consumer access | Lifecycle |
| --- | --- | --- | --- |
| `bot.toml` | Definition | Read-only | Portable |
| `bot.md` | Definition | Read-only | Portable |
| `skills/` | Definition | Read-only | Portable |
| `resources/` | Definition | Read-only | Portable |
| `state/` | Durable runtime data | Read/write | Persistent |
| `workspace/` | Durable user data | Read/write | Persistent |
| `secrets/` | Secret runtime input | Read-only by default | Host-provided |
| `run/` | Ephemeral runtime data | Read/write | One run |

Definition paths describe the bot and are suitable for copying and source control.

Normal runtime consumers MUST NOT mutate definition paths or `secrets/`.

Consumers MAY create `state/`, `workspace/`, and `run/` when needed. They MUST NOT create absent definition paths.

A secret-management consumer MAY mutate `secrets/` only when the host explicitly grants that role.

## 5. `bot.toml`

`bot.toml` is optional UTF-8 TOML.

It may contain these standard keys:

```toml
model = "gpt-5"
base_url = "https://api.openai.com/v1"

[[connection]]
id = "wax"
kind = "local"
command = "wax"

[[connection]]
id = "fsx"
kind = "local"
command = "fsx"
```

All keys are optional.

`model` selects the model name used by the consumer.

`base_url` selects an OpenAI-compatible API base URL.

A local connection's `command` is one executable name resolved through the consumer's `PATH`. It is a command name, not a shell command, and MUST NOT contain arguments. The legacy `tools` array is still accepted; consumers MUST preserve entry order and ignore duplicate entries after the first.

`[[connection]]` declares one integration as a unit — an id the model sees as a tool namespace, a kind, and how to reach it. Tools are discovered from the connection, so `bot.toml` does not list them; `capabilities` optionally narrows what a connection exposes. Ids are lowercase letters, digits, `_` and `-` (1–64 characters) and unique across the bot. `tools` and MCP server tables are legacy spellings of the same concept and keep their ids.

```toml
[[connection]]
id      = "web"
kind    = "local"        # local | mcp-stdio | mcp-http
command = "wax"
trust   = "auto"         # auto | read
```

A missing `bot.toml` or missing key means that the consumer applies its own default.

Consumers MUST ignore unknown keys for forward compatibility.

This specification does not define the tool provider protocol.

## 6. `bot.md`

`bot.md` is optional UTF-8 Markdown.

It contains free-form bot instructions. The specification does not define how a consumer applies them.

Consumers MUST treat its content literally. This version defines no includes, variables, interpolation, or additional standard prompt files.

## 7. `skills/`

`skills/` is optional.

It contains skills using the external Agent Skills format. This specification does not redefine that format.

A host MAY assemble `skills/` from several sources. It MUST reject ambiguous skill-name conflicts rather than silently select one.

Skill content is not inherently trusted.

## 8. `resources/`

`resources/` is optional.

It contains read-only files supplied as part of the bot definition that have no other standard meaning. It MAY contain text or binary data and has no prescribed internal layout.

Consumers MUST NOT automatically add all resources to model context.

`resources/` is definition input. `workspace/` is mutable instance data.

## 9. `state/`

`state/` is optional private, durable, machine-managed runtime data.

It may contain sessions, indexes, checkpoints, transport cursors, and other consumer state.

`state/` resolves relative to `BOT_DATA` when set, otherwise relative to the bot root.

Each consumer runtime MUST write inside its own lowercase namespace:

```text
state/
  ax/
  slaxi/
```

A consumer runtime exclusively owns and interprets its namespace. Other consumers MUST treat it as opaque. Several processes MAY share a namespace only when they implement the same state protocol.

Consumers MUST make state writes safe against interruption. File replacement SHOULD be atomic. Append-only formats SHOULD remain valid after an interrupted append.

State MUST survive normal stops, restarts, and definition updates. A consumer or host MUST NOT erase it as part of normal operation. Resetting state MUST be explicit.

State MAY contain sensitive data and SHOULD be treated as private.

State format and migration belong to the consumer runtime that owns the namespace.

## 10. `workspace/`

`workspace/` is optional private, durable, user-visible instance data.

It contains files supplied, created, or modified as work input, working material, or output.

`workspace/` resolves relative to `BOT_DATA` when set, otherwise relative to the bot root.

The workspace has no standard internal layout or consumer namespaces. Users and consumers MAY read and write it concurrently.

Workspace data MUST survive normal stops, restarts, and definition updates. A consumer or host MUST NOT erase it as part of normal operation. Resetting workspace data MUST be explicit and separate from resetting state.

Workspace data MAY be sensitive and SHOULD be treated as private.

A consumer MAY start a child process in any readable bot directory appropriate for that command. The specification does not require workspace to be the child process working directory.

## 11. `secrets/`

`secrets/` is optional secret runtime input provided by the host or an explicitly authorized secret-management consumer.

A normal runtime consumer MUST treat it as read-only. It MUST NOT create, modify, rename, or delete its entries.

Each secret MUST be one regular file directly under `secrets/`. Nested secret directories are not supported by this version.

Secret names MUST use lowercase ASCII kebab-case. Content is an exact sequence of arbitrary bytes. The filesystem contract does not trim line endings or require UTF-8.

The standard model API key path is:

```text
secrets/api-key
```

Other consuming protocols MAY define additional explicit secret names.

A consumer MUST fail readiness when a secret required for its operation is absent. It MUST identify the missing secret without exposing secret values.

A consumer MUST NOT copy secret values into definition paths, state, workspace, run data, output, or logs.

A consumer MAY pass a required secret to a child process. It SHOULD limit access to the smallest required scope.

The host decides whether secrets come from protected files, environment variables, a vault, container secrets, or another source. The runtime secret view need not survive a restart, but the host MUST be able to provide it again.

Secrets are **volatile**: a host MUST NOT persist them in the runtime data root. The `secrets/` path, when present, is a non-persistent host view (a mount or injected files) or an environment-provided set. A runtime consumer MUST NOT write secrets anywhere.

Secret rotation during a run is not defined by this version.

## 12. `run/`

`run/` is optional private ephemeral data for one run.

`run/` resolves relative to `BOT_DATA` when set, otherwise relative to the bot root.

It may contain sockets, locks, process identifiers, short-lived caches, and shared intermediate files.

A host or first consumer MAY create it. It MUST be empty at the start of a run. A host MUST remove or replace stale `run/` data before another run.

Each consumer runtime MUST use its own lowercase namespace:

```text
run/
  ax/
  slaxi/
```

Consumers MUST NOT require `run/` data to survive a stop, crash, or restart.

Private scratch data that other bot processes do not need to discover MAY use the operating system's temporary-file facilities instead.

## 13. Startup and shutdown

A host starts a consumer by changing to the bot root and executing it using normal process semantics:

```sh
cd "$bot"
exec consumer
```

The caller chooses the consumer command and its arguments. They are not part of the bot definition.

Before accepting work, a consumer MUST validate the standard paths it uses and report missing required runtime inputs.

Consumers SHOULD use standard output for results or protocol output and standard error for diagnostics. The host SHOULD capture output when durable logs are needed. This specification defines no log directory or output protocol.

On POSIX systems, long-running consumers SHOULD handle `SIGTERM`, stop accepting work, make owned durable state consistent, and exit. Zero exit status means clean termination. Non-zero means failure.

Consumers MUST tolerate forced termination without leaving valid-looking partial durable files.

## 14. Concurrency

Several consumers MAY operate one bot at the same time.

The host coordinates process startup and instance-level locking. Consumer runtimes MAY use their `run/` namespaces for private coordination.

Consumers MUST NOT mutate another consumer's `state/` or `run/` namespace unless both implement one shared protocol.

Sharing state or workspace between bot instances is permitted. The user or host owns the resulting coordination and isolation concerns.

## 15. Validation and readiness

Structural validation MUST be local, deterministic, read-only, and free of external service calls.

It SHOULD check the syntax and path types of present standard paths. Errors are invalid structure or conditions that prevent operation. Implementations SHOULD avoid warnings that do not give the user a clear action.

Structural validation MUST NOT execute tools.

Readiness is a separate consumer or host operation. It MAY resolve tools, check required secrets, and contact configured services.

A validator returns zero for a structurally valid bot and non-zero for an invalid bot. This version defines no diagnostic output format.

## 16. Security

The bot directory is an interface, not a security boundary.

Hosts MAY enforce isolation with filesystem permissions, users, containers, sandboxes, mounts, and network policy.

Definition content, skill content, workspace data, and symbolic-link targets MUST NOT be assumed safe.

`state/`, `workspace/`, `secrets/`, and `run/` SHOULD be private by default.

On POSIX systems, hosts SHOULD restrict secret access to the bot owner or service account. Equivalent controls MAY be used on other systems.

Hosts and consumers SHOULD expose each tool only to the secrets and capabilities it requires.

## 17. Portability

Standard text files MUST use UTF-8. Consumers MUST accept LF and CRLF line endings. Writers SHOULD use LF.

Standard paths use exact lowercase names. Definition authors seeking broad portability SHOULD avoid custom names that differ only by case or use platform-reserved characters.

Runtime data SHOULD store paths relative to the runtime data root (`BOT_DATA` or the bot root) when practical. A bot instance may move between hosts and absolute paths outside that root may become invalid.

The specification defines required access behavior, not one permission model, filesystem, or storage implementation.

## 18. Lifecycle

A host MAY assemble standard paths from independent physical sources into one bot root. A host MAY keep the definition at the bot root and place the runtime paths at a separate `BOT_DATA` root, mounting one persistent volume there. In that layout, a host mounts `BOT_DATA` (not the bot root) so the definition stays immutable and secrets stay volatile.

One definition MAY back several instances. Each instance SHOULD use separate state, workspace, secrets, and run data. Sharing is permitted when explicitly chosen.

Copying definition paths creates a bot clone with no runtime data. Copying definition, state, and workspace creates a snapshot. Secrets SHOULD be copied separately through a protected mechanism.

Definition updates SHOULD occur between runs. Live updates and secret rotation are not defined by this version.

After a stop or crash, a host MUST preserve state and workspace data and MUST discard stale run data. Restart policy is outside this specification.

## 19. Compatibility

Consumers MUST ignore unknown paths and unknown `bot.toml` keys.

New standard paths MUST initially be optional.

The meaning of a standard path MUST NOT change within a major specification version.

Optional paths MUST NOT become required within a major version.

A consumer runtime owns compatibility and migration for its state namespace. A migration SHOULD be explicit, documented, and safe against interruption.

The written specification is versioned. A bot does not declare a specification version in this version of the contract.

## 20. Conformance

A conforming bot is a directory whose present standard paths follow this specification. An empty directory conforms.

A conforming consumer:

- treats initial CWD as the bot root
- respects standard path meanings and access rules
- resolves runtime paths relative to `BOT_DATA` when set, otherwise the bot root
- ignores unknown paths and config keys
- namespaces its state and run data
- preserves durable data
- does not write secrets to persisted storage

A conforming host:

- starts consumers from the bot root
- exports `BOT_DATA` for consumers, defaulting to the bot root
- preserves state and workspace data unless explicitly told to erase them
- provides required secret inputs without persisting them in the runtime data root
- removes stale run data before a new run

Partial consumers such as editors and validators conform when they respect the paths they access.

## 21. Version 1 acceptance

Version 1 should be frozen only after:

- three different real bots use the contract
- two independent consumers use the contract
- a shell host and a container host use the contract
- protected persistent secrets and materialized secrets are tested
- stop, forced termination, restart, missing-secret, partial-write, and definition-update cases are tested
- the same definition runs under two hosts without changes
- durable data survives restart and update
- no required deployment choice leaks into the portable definition
