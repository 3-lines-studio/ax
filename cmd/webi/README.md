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
- A message sent while the thread is running steers the run: webi writes
  `{"type":"steer","text":...}` to the same ax process, which applies it at the
  next turn boundary and records it in the session. The composer stays usable
  during a run for exactly this. Images cannot be steered (`409`): they need a
  new prompt, not a user turn.
- The first finished run names the thread: one small model call with the first
  exchange, best effort. The name is stored once (`title_generated`) and a
  later run never renames the thread; without a key the title stays the one
  derived from the first message.

## Not here yet

The vault (secrets stay in the bot environment; the UI's vault panel answers
"not available"), Slack-specific formatting and pixi's supervisor. Threads compact themselves in ax (`compact_at`), and a
compaction shows up as a divider in the UI.
