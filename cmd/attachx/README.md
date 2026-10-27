# Attachx

Conversation attachment tool for AX.

The host sets `BOT_ROOT` (and optionally `BOT_DATA`) for each run; attachx reads the workspace at `$BOT_DATA/workspace` when `BOT_DATA` is set, otherwise `$BOT_ROOT/workspace`. Slaxi sets `AX_ARTIFACT_DIR` to a per-thread artifact directory. Enable Attachx in a bot's tool list:

```text
attachx
```

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- attachx
```

## Protocol

```sh
attachx describe
printf '{"path":"/workspace/report.pdf"}' | attachx run attach_file
```

Attachx accepts non-empty regular files inside `$BOT_DATA/workspace` (or `$BOT_ROOT/workspace` when `BOT_DATA` is unset), limits them to 25 MB, and copies them into `AX_ARTIFACT_DIR`. Interfaces decide how users receive the artifact.

## Test

```sh
go test ./cmd/attachx
```
