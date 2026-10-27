# boti

The [Botdir](../../docs/botdir/BOTDIR.md) host loader for the AX ecosystem. Given a bot root, it reads `bot.toml`, installs the consumer, engine, and every declared tool binary, wires the standard Botdir environment, runs the bot's `[[pre_run]]` hooks, then execs the declared consumer (default `slaxi`).

```sh
boti install   # read bot.toml, install any missing binaries (build-time)
boti           # wire the bot root, run [[pre_run]] hooks, exec the consumer
```

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

`boti install` installs missing tools through `https://github.com/3-lines-studio/ax/releases/latest/download/install.sh`, honoring `AX_PREFIX` and `VERSION` (all programs; `AX_VERSION` still overrides `ax` alone). `boti` exports `BOT_ROOT` (the definition root) and `BOT_DATA` (the runtime root, defaulting to `BOT_ROOT`); consumers and tools derive definition paths (`bot.md`, `skills/`) from `BOT_ROOT` and runtime paths (`workspace/`, `state/`, `run/`) from `BOT_DATA`, with secrets host-provided and volatile.

## `bot.toml`

```toml
consumer = "slaxi"

[env]
WAX_NO_SANDBOX = "1"

[[pre_run]]
command = "refresh-skill --name metrics"
```

- `consumer` — executable resolved through `PATH`; the loader execs it.
- `[env]` — literal environment variables exported; host env wins.
- `[[pre_run]]` — commands run in order before the consumer; a non-zero exit aborts startup.

## Development

```sh
make bin/boti
go test ./cmd/boti
```
