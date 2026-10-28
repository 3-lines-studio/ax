# webi

The browser transport for `ax`, the sibling of `slaxi`: one `ax` process per
message, the same sessions and the same MCP catalog. A channel is a bot
directory (`bot.toml`, `workspace/`, `skills/`); a thread is an ax session log.

```sh
ax mcp add sentry             # whatever the bot needs
webi -C ~/bots/team
```

If `~/bots/team` does not exist, webi creates it with a starter `bot.toml`.
In a terminal it prints a token URL (`?token=...`) and opens it; the visit
sets a cookie and redirects the token out of the URL. On another device use
the "Other devices" URL, or `?token=` from `<state>/token` (created on first
run, 0600). Flags: `-ax` (binary), `-state` (default
`<BOT_DATA>/state/webi`), `-base`/`-model` overrides, `-open=false` to skip
the browser.

## Run it as a service

```sh
webi -C ~/bots/me -listen 0.0.0.0:8377 setup
```

`setup` writes `~/.config/systemd/user/webi.service` with the same `-C`,
`-listen` and `-state`, enables and starts it, and prints the token URL. It
does not restart a running unit when the flags change; it prints the
`systemctl --user restart webi` command instead. Without systemd, run webi in
a terminal or add it to your login items.

A restart asks for the vault PIN again. `boti serve` (see
[docs/deploy.md](../../docs/deploy.md)) keeps the key across restarts at the
cost of a supervisor, which is worth it only if you also want builds and
rollback.

## Access

Every request needs the token except `/healthz` and `/readyz`: a browser gets
it from the `?token=` link, scripts send `Authorization: Bearer <token>`.
`WEBI_TOKEN` pins the token from the environment instead of the file. The
check does not trust loopback, because a page in the same browser can be
pointed at `127.0.0.1` by DNS rebinding.

Wrong PINs on unlock, check and reveal are counted per client: after five the
client waits 30 s, doubling to 15 minutes. Responses carry a strict CSP (`self`
only, no inline script), `nosniff` and no referrer.

## How it works

- `internal/axrun` starts `ax --events --session <thread>/session.jsonl -C
  <channel workdir>` and forwards the NDJSON events. The run gets
  `BOT_DATA=<channel workdir>`, so a channel is an independent bot: its tools
  work there and its `workspace/`, `skills/` and `secrets/` are its own. The
  host's `BOT_DATA` only holds webi's state (channels, threads, vault).
- The UI (the embedded static app) gets `delta`, `message`, `run`, `step`, `compact`
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
- Runs survive the process. On startup and every 30 s, webi looks for threads
  whose ax run never wrote its `run_end` and calls `ax --resume` on them, so a
  crash or a deploy picks the work up instead of stranding it. The session cache
  is keyed by size+mtime, so an untouched log costs one `stat` per sweep, and
  the resume turn is shown as a "run resumed" divider rather than a message the
  user typed.
- A tool that needs a secret mid-run asks for it (`{"ask":{"name":...}}`, see
  the [provider convention](../../docs/tools.md)). webi shows the request in
  `state.pending_secret`, the UI banner asks for the value, and
  `POST /api/vault/secret` stores it in the channel's vault scope **and** hands
  it to the live ax so the call retries without restarting the thread. `skip`
  tells ax to fail the call instead.
- The first finished run names the thread: one small model call with the first
  exchange, best effort. The name is stored once (`title_generated`) and a
  later run never renames the thread; without a key the title stays the one
  derived from the first message.

## Vault

The API key is a vault secret: create the vault first, then add
`DEEPSEEK_API_KEY` in the `global` scope (the "Add key" banner opens that form
with the name already filled). The Settings dialog does not store a key; there
is exactly one place for secrets.

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
- The PIN survives a restart of webi: unlocking (or creating the vault) hands
  the derived key to the supervisor over the control socket, and the next
  process adopts it from fd 3 (`AX_VAULT_FD`) without asking. Locking tells the
  supervisor to forget it; restarting the supervisor itself does too.
- Five wrong PINs wait 30 seconds, then double up to 15 minutes. The counter is
  per client and resets on a good PIN.

Revealing a value asks for the PIN again on every request: anything running as
the same user can reach loopback, so an unlocked session is not a license to
print secrets.

## Health, build and selftest

`GET /healthz` answers `{"ok":"1","build":"<id>","uptime":<s>}`; `GET
/readyz` adds `running` and stays open so a host can wait for idle without the
token. The build id
comes from `AX_BUILD`, which the host that supervises a build sets (`boti
serve` does); a plain checkout reports `dev`. `/api/state` carries the same id
plus `running`, the number of threads with a live ax process.

`webi -selftest` checks the binary against a bot without binding a port or
calling the model: the embedded UI, the state directory, the vault header and
`ax selftest` with the engine this webi would spawn. Exit code 1 means the
build should not be activated.

## Not here yet

Slack-specific formatting and ax's supervisor. Threads compact themselves in
ax (`compact_at`), and a compaction shows up as a divider in the UI.
