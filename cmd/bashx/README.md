# bashx

Run non-interactive Bash commands through the `ax` Unix tool convention.

```sh
bashx describe
printf '%s\n' '{"command":"printf hello"}' | bashx run bash
```

Commands run with host filesystem and network access and inherit the host environment (the container or VM is the sandbox). They do not load Bash profiles or accept interactive input.

Commands time out after 120 seconds by default (max 600 seconds); the timeout kills the command process group. Combined stdout/stderr is capped at the last 16 KiB.

bashx uses `$BOT_DATA/workspace` when run inside a bot (falling back to `$BOT_ROOT`), `$BOT_ROOT/workspace` when only `BOT_ROOT` is set, then the current directory (or a `workspace/` directory present there) when standalone.

Add it to `bot.toml`:

```toml
[[connection]]
id = "bashx"
kind = "local"
command = "bashx"
```

The working directory is not a filesystem boundary — commands can read and change files elsewhere when OS permissions allow. Isolation is the caller's job: run bashx inside a container or VM when you need it, with minimal filesystem and network access.

## Build and install

```sh
make bin/bashx
go test ./cmd/bashx
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- bashx
```
