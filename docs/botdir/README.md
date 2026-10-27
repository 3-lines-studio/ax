# Botdir

A portable directory interface for bots.

A **bot** is a directory. **Consumers** (agent programs) start with the bot
directory as their working directory and derive their configuration, inputs,
outputs, and state from it by convention. **Hosts** prepare a bot and start
consumers.

`docs/botdir/` is the canonical, versioned home of the Botdir specification:

- [BOTDIR.md](BOTDIR.md) — the normative spec (terms, standard paths,
  definition, runtime, process contract, conformance).
- [BOTDIR-GUIDE.md](BOTDIR-GUIDE.md) — the implementation guide.

## The idea

Rather than each agent project inventing its own config, session, and
workspace layout, a bot is a plain directory the whole ecosystem agrees on:

```text
.
├── bot.toml       # configuration
├── bot.md         # instructions
├── skills/        # Agent Skills
├── resources/     # other read-only input
├── state/         # durable machine-managed data
├── workspace/     # durable user-visible work
├── secrets/       # secret runtime input
└── run/           # ephemeral per-run data
```

No new process protocol, no supervisor, and no bot-specific environment
variable is required. A host simply `cd`s into the bot and execs the consumer.

A host MAY provide a separate **runtime data root** via the optional
`BOT_DATA` environment variable. When set, `state/`, `workspace/`, and `run/`
resolve there (so a single persistent volume can be mounted without shadowing
the definition); the definition (`bot.toml`, `bot.md`, `skills/`,
`resources/`) stays at the bot root and secrets stay host-provided and
volatile.

## Conformance

- A **bot** conforms when its present standard paths follow the spec. An empty
  directory conforms.
- A **consumer** conforms when it uses its initial working directory as the
  bot root, respects standard path meanings and access rules, ignores unknown
  paths and `bot.toml` keys, namespaces its `state/` and `run/` data, and
  preserves durable data.
- A **host** conforms when it starts consumers from the bot root, exports
  `BOT_DATA` for consumers (defaulting to the bot root), provides required
  secrets without persisting them in the runtime data root, preserves `state/`
  and `workspace/` data, and removes stale `run/` data before a new run.

## Ecosystem

Every program in this repository is a Botdir consumer or host.
