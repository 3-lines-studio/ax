# Memory

Memory is durable personal state for a bot: facts and preferences that should
survive a session but do not belong in the bot definition.

## Where it lives

Memory is runtime data, not definition. It lives under the bot's runtime state:

```text
$BOT_DATA/state/memory/       # or <bot>/state/memory/ without BOT_DATA
  20261028T174029-0f137b1f.json
```

One JSON file per memory, mode 0600, directory 0700. Readers may read the files
directly; the format is the export. A malformed file is a hard error, so the
user can fix it instead of silently losing facts.

### Per channel

A channel is a bot, and a host runs each channel with `BOT_DATA` set to the
channel's own workdir. Memory therefore follows the channel:

```text
/channels/work/state/memory/       # channel "work"
/channels/home/state/memory/       # channel "home"
```

Switching channel switches facts: a preference saved in `work` is not visible in
`home`, and `dedupe_key` only collides inside one channel. Copying
`state/memory/` between channels is the way to share something on purpose.

```json
{
  "id": "20261028T174029-0f137b1f",
  "subject": "Zona horaria de Don",
  "summary": "Don vive en Argentina (UTC-3).",
  "body": "Usar America/Argentina/Buenos_Aires para horarios.",
  "category": "profile",
  "source_uris": ["https://example.com/timezone"],
  "dedupe_key": "profile:timezone",
  "created_at": "2026-10-28T17:40:29Z",
  "updated_at": "2026-10-28T17:40:29Z",
  "deleted_at": null
}
```

`id` is generated. `subject` and `summary` are required; `body`, `category`,
`source_uris` and `dedupe_key` are optional. `dedupe_key` is unique among
memories: an upsert with the same key updates the existing fact and restores it
if it was soft-deleted. `deleted_at` marks a soft delete; a delete with
`force: true` removes the file.

## What belongs here

Durable personal facts: who the user is, identifiers and handles, people and
relationships that give a task context, project nicknames and background facts
not recorded elsewhere.

Use the right home for everything else:

| State | Home |
| --- | --- |
| Durable personal fact | memory |
| Repeatable procedure | `skills/` |
| Credential, token, password | the vault / `secrets/` |
| Routine cursor or checkpoint | `state/routines/<name>/` |
| Project documentation | `bot.md`, `AGENTS.md` |
| Session transcript | the append-only session log |

## Tools

Declare the provider as a local connection:

```toml
[[connection]]
id = "memoryx"
kind = "local"
command = "memoryx"
```

The `memoryx` provider exposes five catalog tools:

- `memoryx.search` — rank active memories for a query.
- `memoryx.get` — read one memory by id.
- `memoryx.verify` — find memories already saved about a topic.
- `memoryx.upsert` — create or update a fact.
- `memoryx.delete` — soft-delete, or remove with `force`.

The tool descriptions carry the policy: verify before writing, keep one fact per
memory, say what was saved, and never store secrets, routine state or
procedures. Search is lexical and local; no model and no network call are
involved. The provider inherits the channel's `BOT_ROOT` and `BOT_DATA` from the
`ax` process that starts it, which is what keeps the store per channel.

## Why there is no automatic surfacing

Kody-style automatic surfacing — retrieving one or two memories and putting them
next to a search/execute result — is deliberately not implemented. The model
already has `memoryx.search` and a prompt rule to check memory when a task
depends on personal facts; injecting extra text into every tool result would
spend context on guesses. Add it only if an eval shows the model missing facts
it could have found.
