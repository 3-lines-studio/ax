# Memoryx

Memoryx exposes the bot's personal memory store as catalog tools.

```sh
memoryx describe
printf '%s\n' '{"query":"timezone"}' | memoryx run search
printf '%s\n' '{"subject":"Zona horaria","summary":"Don vive en Argentina (UTC-3).","dedupe_key":"profile:timezone"}' | memoryx run upsert
```

Tools:

```text
search   rank saved memories
get      read one memory
verify   see what already exists before writing
upsert   create or update a fact
delete   soft-delete, or remove with force
```

The store is one JSON file per memory under `state/memory/` in the runtime data
root (`BOT_DATA` when a host provides one, otherwise the bot root). Files are
0600 and the directory is 0700. Memories are durable personal facts: names,
ids, preferences, relationships and project context. They are not for secrets
(the vault), routine state (`state/routines/`), procedures (`skills/`) or
project documentation (`bot.md`/`AGENTS.md`).

Search is lexical and local; there is no embedding model and no network call.
`dedupe_key` makes an upsert update the same fact instead of duplicating it.
`delete` is a soft delete; `force` removes the file.

Declare it as a local connection in the bot's `bot.toml`:

```toml
[[connection]]
id = "memoryx"
kind = "local"
command = "memoryx"
```

## Build

```sh
make bin/memoryx
go test ./cmd/memoryx ./internal/memory
```

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- memoryx
```
