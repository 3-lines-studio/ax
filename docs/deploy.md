# Deploying AX

AX is a set of processes, not a service: something has to keep them running.
This document is that "something", in three shapes. None of them is built into
`ax` — the engine only knows how to run one conversation.

## What a host needs

- the binaries (`install.sh`, or `boti install` for a bot's tool list),
- a bot directory (`bot.toml`, `workspace/`, optional `skills/`, `secrets/`),
- a way to run a conversation: `ax` for one-shots, `webi` or `slaxi` for a
  live interface,
- a model key: `DEEPSEEK_API_KEY`, `secrets/api-key` in the bot, or the vault.

Check the whole thing without spending tokens:

```sh
ax -C ~/bots/example selftest
boti -C ~/bots/example selftest     # also checks every tool in bot.toml
```

`selftest` parses the bot, resolves the connections and loads the catalog. A
dead integration is a warning; a broken compute root or an unparseable
connection is a failure, because every run would hit it.

## systemd (a VM or a small server)

One unit per bot, running the transport you use. See
[`packaging/ax-webi.service`](../packaging/ax-webi.service):

```sh
mkdir -p ~/.config/systemd/user
cp packaging/ax-webi.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now ax-webi
```

`webi` binds `127.0.0.1` on purpose. To reach it from outside, put something in
front (next section); do not open the port.

## Container

[`packaging/Dockerfile`](../packaging/Dockerfile) builds an image with every
program and no shell:

```sh
docker build -f packaging/Dockerfile -t ax .
docker run --rm -it -v "$PWD/bot:/bot" -e DEEPSEEK_API_KEY ax ax -C /bot "summarize this repo"
```

Mount the bot at a stable path and pass the key through the environment or the
bot's `secrets/`. The image is the same binaries the release publishes, so a tag
is a pin: `ax:0.5.0` is reproducible.

## In front: TLS and a tailnet

The transport speaks plain HTTP on loopback. Whatever terminates TLS is the
user's call:

- `tailscale serve --bg http://127.0.0.1:8377` puts webi on the tailnet with a
  certificate and Tailscale's own identity check, with no code in AX.
- Caddy or nginx with basic auth or mTLS, if the host is public.
- `ssh -L 8377:127.0.0.1:8377 host` when it is just you.

## Updating

`boti update` is the safe path: it backs up the installed binaries, installs the
current release and runs `boti selftest`. If the new engine does not survive it,
the previous binaries come back and the command fails, so a bad release cannot
leave a host that will not start.

```sh
boti -C ~/bots/example update
```

A run that is in flight when a binary is replaced keeps running: the old file
stays alive until the process exits, and the next run uses the new one. The host
is what decides when to restart; `webi` picks up interrupted runs on its own
(see [../cmd/webi/README.md](../cmd/webi/README.md)).
