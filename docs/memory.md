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

The `memoryx` provider exposes five catalog tools:

- `memoryx.search` — rank active memories for a query.
- `memoryx.get` — read one memory by id.
- `memoryx.verify` — find memories already saved about a topic.
- `memoryx.upsert` — create or update a fact.
- `memoryx.delete` — soft-delete, or remove with `force`.

The tool descriptions carry the policy: verify before writing, keep one fact per
memory, say what was saved, and never store secrets, routine state or
procedures. Search is lexical and local; no model and no network call are
involved.

## Not implemented yet

Kody-style automatic surfacing (retrieving one or two relevant memories and
putting them next to a search/execute result) is deliberately left for a later
pass. The data and the tool exist first; auto-surfacing can be added when the
evals show it is needed.
