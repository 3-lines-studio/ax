# Fsx

Fsx provides filesystem tools through the Unix tool convention.

```sh
fsx describe
printf '%s\n' '{"path":"README.md"}' | fsx run read
```

Tools:

```text
read
write
edit
```

Fsx uses `$BOT_DATA/workspace` when run inside a bot (falling back to `$BOT_ROOT`), `$BOT_ROOT/workspace` when only `BOT_ROOT` is set, then the current directory (or a `workspace/` directory present there) when run standalone. Go's `os.Root` rejects `..`, absolute-path, and symlink escapes. Absolute input paths are accepted only when they are beneath the workspace. Files inside the workspace remain writable and removable.

Add Fsx to the bot's `bot.toml`:

```toml
[[connection]]
id = "fsx"
kind = "local"
command = "fsx"
```

Use it with Bashx by adding a second connection:

```toml
[[connection]]
id = "bashx"
kind = "local"
command = "bashx"
```

## Build

```sh
make bin/fsx
go test ./cmd/fsx
```

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- fsx
```
