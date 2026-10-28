# Sshx

Sshx is the filesystem and shell provider for a **remote** host. It speaks the
same `describe`/`run` contract as [fsx](../fsx/README.md) and
[bashx](../bashx/README.md), with the same tool names and the same text
handling, so a connection can move a bot's compute to another machine without
the model noticing.

```sh
sshx describe
printf '%s\n' '{"path":"main.go"}' | SSHX_HOST=me@host SSHX_KNOWN_HOSTS=... sshx run read
```

Tools:

```text
read    read a file (offset/limit, 16 KB pages)
write   write a file (atomic: temp + posix-rename)
edit    exact text replacements, unique match required
bash    run a command over an exec channel, 120 s default timeout
```

## Configuration

Configuration comes from the environment the connection sets, never from
arguments:

| Variable | Meaning |
| --- | --- |
| `SSHX_HOST` | `user@host` or `user@host:port` (required) |
| `SSHX_KNOWN_HOSTS` | file with the host keys to trust (required) |
| `SSHX_KEY_FILE` | private key file; otherwise the agent, then `~/.ssh/id_*` |
| `SSHX_ROOT` | remote directory every path is relative to |
| `SSHX_TIMEOUT` | connect timeout in seconds (default 30, max 300) |

There is no option to accept any host key: a connection that skips host
verification is a MITM with a shell, so `SSHX_KNOWN_HOSTS` is mandatory and the
key must be parseable without a passphrase (a provider process has no terminal).

With `SSHX_ROOT` set, relative paths resolve inside it and absolute paths are
accepted only when they are beneath it; `..` cannot escape.

## As an AX connection

`ax` writes that environment for you from `[[connection]]`:

```toml
[[connection]]
id          = "fs"
kind        = "ssh"
host        = "builder@10.0.0.5:22"
root        = "/srv/app"                 # a path on the remote host
key         = "secrets/id_ed25519"       # relative to the bot root
known_hosts = "secrets/known_hosts"
compute     = true
```

`compute = true` makes these tools the bot's core tools; the model sees
`fs.read`, `fs.write`, `fs.edit` and `fs.bash` exactly like the local set. A
`capabilities` list or `trust = "read"` narrows them further.

## Guarantees

| Guarantee | sshx |
| --- | --- |
| atomic rename | `posix-rename@openssh.com`, falling back to `rename` |
| fsync | `fsync@openssh.com` when the server has it; the rename still orders readers |
| large reads | 8 MB cap; larger files say to use `bash` |
| read-then-edit safety | the edit re-reads the file and demands one unique match |
| output | last 16 KB of a command's output, with a truncation marker |
