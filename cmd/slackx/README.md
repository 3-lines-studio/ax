# slackx

Slack file-upload tool for AX processes launched by Slaxi.

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- slackx
```

## Configure

Slaxi supplies the message context. Set the bot token and enable the tool:

```sh
export SLACK_BOT_TOKEN=xoxb-...
```

```toml
[[connection]]
id = "slackx"
kind = "local"
command = "slackx"
```

## Protocol

```sh
slackx describe
printf '{"path":"chart.png"}' | \
  AX_SLACK_CHANNEL=C123 AX_SLACK_THREAD=123.456 \
  slackx run upload_to_slack
```
