# slaxi

Slack interface for `ax`. Each Slack thread maps to one persistent `ax` session.

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- slaxi
```

## Requirements

- slaxi and ax in `PATH`
- a Slack app with Socket Mode
- scopes: `app_mentions:read`, `im:history`, `chat:write`, `files:write`
- subscriptions: `app_mention` and `message.im`

## Run

slaxi is a Botdir consumer. Start it from the bot root; it runs `ax` as a subprocess:

```sh
cd /path/to/bot
slaxi
```

It reads `model`, `base_url`, and the `[slack]` table (`mention_only`) from `bot.toml`, and Slack secrets from the environment or `secrets/` files. It fails readiness when a required Slack or model secret is absent. Sessions live under `state/ax/sessions/`; transport state under `state/slack/`; artifacts under `workspace/slack/artifacts/`; ephemeral data under `run/slack/` (created on start). These runtime paths resolve under `BOT_DATA` (default `BOT_ROOT`), while `bot.toml` and `secrets/` stay on `BOT_ROOT`.

## Variables

```text
BOT_ROOT            bot definition root (default: current directory)
BOT_DATA            bot runtime data root (default: BOT_ROOT)
SLAXI_AX_PATH
SLAXI_BASE_URL      overrides bot.toml base_url
SLAXI_MODEL         overrides bot.toml model
```

`SLACK_APP_TOKEN`, `SLACK_BOT_TOKEN`, and `OPENAI_API_KEY` are read from the environment first, then `secrets/` files.

## Diagnose

Validate a bot root before starting — it reports exactly which secrets, the `ax` binary, or `bot.toml` tools are missing:

```sh
slaxi doctor
```

## Test

```sh
go test ./cmd/slaxi
```
