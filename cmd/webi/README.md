# webi

The browser transport for `ax`, the sibling of `slaxi`: one `ax` process per
message, the same sessions and the same MCP catalog. A channel is a bot
directory (`bot.toml`, `workspace/`, `skills/`); a thread is an ax session log.

```sh
ax mcp add sentry             # whatever the bot needs
webi -C ~/bots/team -listen 127.0.0.1:8377
```

Open `http://127.0.0.1:8377`. Flags: `-ax` (binary), `-state` (default
`<BOT_DATA>/state/webi`), `-base`/`-model` overrides.

## How it works

- `internal/axrun` starts `ax --events --session <thread>/session.jsonl -C
  <channel workdir>` and forwards the NDJSON events.
- The UI (pixi's static app) gets `delta`, `message`, `run`, `step`, `compact`
  and `state` events over `/api/stream`.
- History is read straight from the ax session log, so a thread survives a
  restart and `slaxi` can read the same conversation format.
- `POST /api/threads/{id}/stop` writes the `cancel` control to ax's stdin;
  `POST /api/threads/{id}/compact` runs `ax compact --session ...`.

## Not here yet

The vault (secrets stay in the bot environment), Slack-specific formatting and
pixi's supervisor. Threads compact themselves in ax (`compact_at`), and a
compaction shows up as a divider in the UI.
