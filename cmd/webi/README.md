# webi

The browser transport for `ax`, the sibling of `slaxi`: one `ax` process per
message, the same sessions and the same MCP catalog. A channel is a workspace;
the host's `bot.toml`, `bot.md` and `skills/` are shared by every channel, and a
thread is an ax session log. A folder with its own `bot.toml` is a standalone
bot and overrides the host config; you never need one per channel. A fresh bot
gets a `bot.toml` that declares every installed provider among `fsx`, `bashx`,
`skillx` and `memoryx`, so the first chat can use them.

```sh
ax mcp add sentry             # whatever the bot needs
webi setup
```

`setup` keeps the UI in your per-user data directory: `~/.local/share/ax` on
Linux (or `$XDG_DATA_HOME/ax`), `~/Library/Application Support/ax` on macOS.
No folder to choose. For a bot in a project directory, pass `-C ~/project`;
the app directory is the default. In a terminal, webi prints a token URL
(`?token=...`) and opens it; the visit sets a cookie and redirects the token
out of the URL. `-listen` pins the address (setup asks whether other devices
may connect), `-ax` points at the binary, `-state` moves the state (default
`<BOT_DATA>/state/webi`), `-base`/`-model` override the model, and
`-open=false` skips the browser.

## Run it as a service

```sh
webi setup
```

`setup` asks whether the UI should be reachable from other devices, then
writes a systemd user unit (`~/.config/systemd/user/webi.service`) on Linux or
a LaunchAgent (`~/Library/LaunchAgents/studio.3-lines.ax.webi.plist`) on
macOS, starts it and prints the token URL. It uses the same `-C`, `-listen`
and `-state` as the command. It does not restart a running unit when the
settings change; it prints the restart command instead. Without systemd or
launchd, run webi in a terminal or add it to your login items.

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
  <host bot root>` and forwards the NDJSON events. The run gets
  `BOT_DATA=<channel workdir>`, so tools and state follow the channel while
  `bot.toml`, `bot.md` and `skills/` come from the host. A channel folder with
  its own `bot.toml` is a standalone bot and keeps using it; otherwise the host
  config is the default. The channel workdir is the data root: ax works in its
  `workspace/` when that exists, otherwise in the folder itself, and its
  `state/` holds memory and routines. The run also gets `AX_WEB_CHANNEL`,
  `AX_WEB_THREAD`, their names and titles, the user's name and `AX_WEB_PROMPT`
  (channel instructions); ax folds them into the runtime section of its system
  prompt. Edit channel changes the folder (it must be absolute and exist).
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
is exactly one place for secrets. It does store the model, effort, compaction,
context window and the name shown next to your messages.

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

## Routines

webi is also the routine scheduler. Every 30 seconds it reads
`routines/*/routine.toml` under the bot root and under every channel workdir,
and runs enabled cron schedules with no model in the loop. `ax routine list`
shows them, `ax routine run NAME` tests one manually, and `ax routine runs|log`
reads the history under `state/routines/<name>/runs/<run-id>/`.
The sidebar's **Routines** view lists them with their channel, **Run now**,
**History** and **Log**; a finished run also arrives over the SSE stream, so the
view stays current and the PWA can notify on failures. See
[docs/routines.md](../../docs/routines.md).

## Memory and skills

The sidebar's **Memory** view reads the current channel's store
(`state/memory/`): search, open one, or forget it (a soft delete; the file stays
until you remove it). The **Skills** view lists the directories skillx would
give the agent — the bot's `skills/`, or the workspace and user fallbacks —
shows each `SKILL.md`, and flags what the agent ignores: a missing file, no
description, or a directory whose name is not lowercase-dashes. Both use the
same token as the rest of the API:

| Endpoint | Purpose |
| --- | --- |
| `GET /api/memory?channel=<id>&q=&limit=` | active memories, or search |
| `GET /api/memory/<id>?channel=<id>` | one memory |
| `DELETE /api/memory/<id>?channel=<id>&force=1` | soft delete, or remove |
| `GET /api/skills?channel=<id>` | skills with their source and errors |
| `GET /api/skills/<name>?channel=<id>` | one skill's body |

## Not here yet

Slack-specific formatting and ax's supervisor. Threads compact themselves in
ax (`compact_at`), and a compaction shows up as a divider in the UI.
