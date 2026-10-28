# boti

The [Botdir](../../docs/botdir/BOTDIR.md) host loader for the AX ecosystem. Given a bot root, it reads `bot.toml`, installs the consumer, engine, and every declared tool binary, wires the standard Botdir environment, runs the bot's `[[pre_run]]` hooks, then execs the declared consumer (default `slaxi`).

```sh
boti install   # read bot.toml, install any missing binaries (build-time)
boti           # wire the bot root, run [[pre_run]] hooks, exec the consumer
boti serve     # keep a transport alive, restart it, roll back a bad build
boti status    # what is running, which build, last error
boti logs      # tail the transport's log
boti restart   # restart the child without stopping the host
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
consumer, the `[boti.serve]` command and every tool) is on `PATH` and that
the engine can parse the bot, resolve its connections and load its catalog —
no model call. `boti update` backs up the installed binaries, installs the
current release and runs that selftest; if the new engine fails it, the previous
binaries come back and the command exits non-zero.

`boti install` installs every missing tool through `https://github.com/3-lines-studio/ax/releases/latest/download/install.sh` in one call — the release is one archive per platform, so it is one download — honoring `AX_PREFIX` and `VERSION`. `boti` exports `BOT_ROOT` (the definition root) and `BOT_DATA` (the runtime root, defaulting to `BOT_ROOT`); consumers and tools derive definition paths (`bot.md`, `skills/`) from `BOT_ROOT` and runtime paths (`workspace/`, `state/`, `run/`) from `BOT_DATA`, with secrets host-provided and volatile.

## `bot.toml`

```toml
consumer = "slaxi"

[env]
WAX_NO_SANDBOX = "1"

[[pre_run]]
command = "refresh-skill --name metrics"

[boti.serve]
command = ["webi", "-listen", "127.0.0.1:8377"]
health = "http://127.0.0.1:8377/healthz"
```

- `consumer` — executable resolved through `PATH`; the loader execs it.
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
- `[boti.serve]` also makes `boti install`/`selftest`/`update` include the
  transport binary, so a webi bot installs webi.

## Serve

`boti serve` keeps its state in `$BOT_DATA/state/host/`:

```
state.json    pid, build, previous, child, restarts, last error
log           the child's stdout and stderr (rotated at 4 MiB)
sock          control socket for `boti status|restart`, mode 0600
builds/       one directory per build, `current` is a symlink to the active one
```

When `builds/current` exists, the host prepends its `bin/` to `PATH` and sets
`AX_BUILD` to the build id, so `webi`, `ax` and every tool come from the same
build. Without one it reports `dev` and uses the host's `PATH`.

A child that exits is restarted; three exits in a minute with a `previous`
build recorded switch `current` back to it and record why. A build that is
healthy for one start clears the failure counter, so a later promotion can
still try again. `SIGTERM` to `boti serve` stops the child and exits cleanly.

## Development

```sh
make bin/boti
go test ./cmd/boti
```
