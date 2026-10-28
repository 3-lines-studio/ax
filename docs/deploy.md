# Deploying AX

AX is a set of processes, not a service: something has to keep them running.
`ax` only knows how to run one conversation; `webi` and `slaxi` are the
long-lived transports in front of it. `boti serve` is the host that ships with
the ecosystem: it starts the transport, checks it, restarts it and rolls back a
build that crash-loops. This document also covers the cases where the host is
something else.

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

## systemd: the app

For one bot and no supervisor, let webi write its own unit:

```sh
webi -C ~/bots/me -listen 0.0.0.0:8377 setup
```

It writes `~/.config/systemd/user/webi.service`, enables and starts it, and
prints the token URL. `systemctl --user status webi` and `journalctl --user
-u webi` are the familiar tools. A restart asks for the vault PIN. On a host
where the user is not always logged in, `loginctl enable-linger <user>` keeps
the unit running at boot.

## systemd: supervised (boti serve)

One unit per bot, running `boti serve`. See
[`packaging/ax-webi.service`](../packaging/ax-webi.service):

```sh
mkdir -p ~/.config/systemd/user
cp packaging/ax-webi.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now ax-webi
```

`boti serve` reads `[boti.serve]` from the bot, starts the transport, polls its
health URL and restarts it when it exits. It also keeps the vault key in memory
after an unlock and gives it to each new process, so a restart or a promotion
does not ask for the PIN; restarting the supervisor itself does. Three exits in a minute switch
`builds/current` back to the previous build and record the reason, which is
the rollback systemd alone cannot do.

```sh
boti -C ~/bots/example status    # running?, build, child, last error
boti -C ~/bots/example logs -n 50
boti -C ~/bots/example restart
```

`webi` binds `127.0.0.1` by default. Every request except `/healthz` and
`/readyz` needs the API token, so a public listener is not an open door, but
still put TLS in front (next section). The first visit is
`http://host:8377/?token=$(cat <data>/state/webi/token)`; it sets a cookie.
Set `WEBI_TOKEN` to pin the token from a unit file instead. If the bot uses
`[boti.serve].idle`, point it at `/readyz`, which needs no token.

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

There are two paths, and both are transactional.

From a checkout, when the host is `boti serve`:

```sh
boti -C ~/bots/example promote        # build, test, activate when idle
boti -C ~/bots/example rollback       # back to the previous build
```

`promote` builds the bot's programs with `scripts/package.sh`, runs `go vet`,
`go test`, `ax selftest` and the transport's own selftest, and then asks the
supervisor to switch. Activation waits for `[boti.serve].idle` to report no
live runs, so the promotion does not interrupt the conversation that asked for
it. A failure at any step leaves the running host untouched. The build id is
the short commit (`-dirty` for an uncommitted tree), and the previous build
stays for `boti rollback` and for the crash-loop fallback.

From a release, on any host:

```sh
boti -C ~/bots/example update
```

`update` backs up the installed binaries, installs the current release and runs
`boti selftest`. If the new engine does not survive it, the previous binaries
come back and the command fails, so a bad release cannot leave a host that will
not start.

A run that is in flight when a binary is replaced keeps running: the old file
stays alive until the process exits, and the next run uses the new one. `webi`
picks up interrupted runs on its own (see
[../cmd/webi/README.md](../cmd/webi/README.md)).
