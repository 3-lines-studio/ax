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

## Vault

The UI's vault panel works: secrets live in `state/webi/vault.json`, one
AES-256-GCM blob whose key is derived from a PIN with scrypt (N=32768). The
file never holds plaintext and the decrypted map only exists in memory while
the vault is unlocked.

- Scopes: `global` plus one per channel (`vault_scope`, default `global`). A
  channel sees `global` first and its own scope on top, so a shared token can be
  overridden per channel.
- A run gets those values as environment variables for the `ax` process, which
  is the only way a provider reads a secret.
- `@file:KUBECONFIG` (or any `@file:NAME`) materializes the value as a private
  temporary file (0600) for the length of the run and passes the path, so a PEM
  or a kubeconfig never sits in an environment variable.
- `POST /api/vault/import` merges a pasted `.env`: `KEY=value`, `export`,
  quotes and comments; keys the text does not mention stay.
- The key counts: if the vault holds `DEEPSEEK_API_KEY`, `has_key` is true and
  the UI stops asking for one.

Revealing a value asks for the PIN again on every request: anything running as
the same user can reach loopback, so an unlocked session is not a license to
print secrets.

## Not here yet

Slack-specific formatting and pixi's supervisor. Threads compact themselves in
ax (`compact_at`), and a compaction shows up as a divider in the UI.
