# boti

The [Botdir](../../docs/botdir/BOTDIR.md) host loader for the AX ecosystem. Given a bot root, it reads `bot.toml`, installs the consumer, engine, transport and every local provider, wires the standard Botdir environment, runs the bot's `[[pre_run]]` hooks, then execs the declared consumer (default `slaxi`).

```sh
boti install   # read bot.toml, install any missing binaries (build-time)
boti           # wire the bot root, run [[pre_run]] hooks, exec the consumer
boti serve     # keep a transport alive, restart it, roll back a bad build
boti status    # what is running, which build, last error
boti logs      # tail the transport's log
boti restart   # restart the child without stopping the host
boti promote   # build this checkout and activate it when the bot is idle
boti rollback  # switch back to the previous build
```

`boti run` replaces itself with the consumer (a container entrypoint).
`boti serve` stays in front: it starts the transport from `[boti.serve]`, polls its
health URL, restarts it when it exits and, after three failures in a minute,
switches back to the previous build. A promotion never has to stop the host to
prove a build is good; that is what `boti promote` will do.

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- boti
```

The `boti install` command below is different: it installs the binaries a bot
declares, not boti itself.

## Use

A bot container entrypoint is `boti`:

```dockerfile
COPY bot.toml bot.md /bot/
COPY skills /bot/skills
RUN boti install
CMD ["boti"]
```

`boti selftest` checks that every program in `bot.toml` (the engine, the
consumer, the `[boti.serve]` command, every local connection and every legacy
tool) is on `PATH` and that
the engine can parse the bot, resolve its connections and load its catalog —
no model call. `boti update` backs up the installed binaries, installs the
current release and runs that selftest; if the new engine fails it, the previous
binaries come back and the command exits non-zero.

`boti install` installs every missing tool through `https://github.com/3-lines-studio/ax/releases/latest/download/install.sh` in one call — the release is one archive per platform, so it is one download — honoring `AX_PREFIX` and `VERSION`. `boti` exports `BOT_ROOT` (the definition root) and `BOT_DATA` (the runtime root, defaulting to `BOT_ROOT`); consumers and tools derive definition paths (`bot.md`, `skills/`) from `BOT_ROOT` and runtime paths (`workspace/`, `state/`, `run/`) from `BOT_DATA`, with secrets host-provided and volatile.

## `bot.toml`

```toml
consumer = "slaxi"

[[connection]]
id = "fs"
kind = "local"
command = "fsx"
compute = true

[[connection]]
id = "web"
kind = "local"
command = "wax"

[env]
WAX_NO_SANDBOX = "1"

[[pre_run]]
command = "refresh-skill --name metrics"

[boti.serve]
command = ["webi", "-listen", "127.0.0.1:8377"]
health = "http://127.0.0.1:8377/healthz"
idle = "http://127.0.0.1:8377/readyz"
selftest = ["webi", "-selftest"]
repo = "/srv/ax"
```

- `consumer` — executable resolved through `PATH`; the loader execs it.
- `[[connection]]` — the integration unit. A `kind = "local"` (the default)
  command is one program the host installs and checks; `tools` is the legacy
  spelling of the same thing and still works.
- `[env]` — literal environment variables exported; host env wins.
  `[boti.serve]` commands get the same environment plus `BOT_ROOT`, `BOT_DATA`
  and `AX_BUILD`.
- `[[pre_run]]` — commands run in order before the consumer; a non-zero exit
  aborts startup. `boti serve` runs them once, before the first start.
- `[boti.serve].command` — what `boti serve` runs. Default:
  `webi -listen 127.0.0.1:8377`.
- `[boti.serve].health` — a URL that answers 200 when the transport is useful. It
  defaults to `/healthz` on the default listen address; set it to empty to
  supervise by process liveness only (a transport without a port).
- `[boti.serve].repo` — the ax checkout `boti promote` builds from.
- `[boti.serve].idle` — a URL (`/readyz` on webi) whose `running` field is the number of live
  runs. Activation waits for zero, so a promotion asked for inside a
  conversation does not interrupt that conversation; if the bot never goes
  idle, the host activates after 30 minutes. Without `idle`, promotion
  activates at once.
- `[boti.serve].selftest` — an extra command to run on the new build, with the
  build first on `PATH`. `webi -selftest` checks the UI and the state; `ax
  selftest` is always part of the transaction.
- `[boti.serve]` also makes `boti install`/`selftest`/`update` include the
  transport binary, so a webi bot installs webi.

## Serve

`boti serve` keeps its state in `$BOT_DATA/state/host/`:

```
state.json    pid, build, previous, child, restarts, last error
log           the child's stdout and stderr (rotated at 4 MiB)
sock          control socket for `boti status|restart`, mode 0600
builds/       one directory per build, `current` is a symlink to the active one
              `builds/<id>/bin` holds the programs, `meta.json` the commit
```

When `builds/current` exists, the host prepends its `bin/` to `PATH` and sets
`AX_BUILD` to the build id, so `webi`, `ax` and every tool come from the same
build. Without one it reports `dev` and uses the host's `PATH`.

The vault key webi derives from its PIN is kept in memory, never on disk, and
handed to each child on fd 3 (`AX_VAULT_FD`), so a promotion or a restart does
not ask for the PIN again. Locking the vault, or restarting the supervisor,
forgets it.

A child that exits is restarted; three exits in a minute with a `previous`
build recorded switch `current` back to it and record why. A build that is
healthy for one start clears the failure counter, so a later promotion can
still try again. `SIGTERM` to `boti serve` stops the child and exits cleanly.

## Promote

`boti promote` builds the bot's programs from `[boti.serve].repo` and asks the
running `boti serve` to activate them:

```sh
boti -C ~/bots/example promote          # activate when no run is in flight
boti -C ~/bots/example promote --now    # activate immediately
boti -C ~/bots/example rollback
```

The build id is the short commit, plus `-dirty` when the tree has uncommitted
changes; promoting the same tree again gets `-2`, `-3`. The transaction is `go
vet`, `go test`, `scripts/package.sh --dest`, `ax selftest`, the optional
`[boti.serve].selftest`, and then activation. A failed step leaves the build
in `builds/<id>` for inspection and changes nothing that is running.

The build contains the bot's package list (`ax`, the consumer, the transport
and every tool the checkout can build); a tool from another repository stays on
the host `PATH`. Binaries that are byte-identical to one in the active build
become hard links, so promoting one changed program does not copy the rest, and
the host keeps the active build, the rollback target and the newest one.

`boti rollback` switches back to the previous build and restarts. It is the
same switch a crash loop performs, available when the crash is semantic
instead of a process exit.

## Development

```sh
make bin/boti
go test ./cmd/boti
```
