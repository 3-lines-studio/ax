# Connections

A **connection** is one integration the bot has: a local provider, an MCP
server, or a remote host. The connection, not its tools, is the
unit of configuration. Tools are discovered from the connection and projected
into the catalog; `bot.toml` never lists them unless the bot wants to narrow an
allowlist.

This is a configuration model, not a new contract:

- The model still sees `search` and `execute` ([docs/tools.md](tools.md)).
- Providers still speak `describe`/`run` ([docs/tools.md](tools.md)).
- MCP servers are still MCP ([docs/mcp.md](mcp.md)).
- The session log still records the tool ids the model called
  ([docs/session.md](session.md)).

## Schema

```toml
[[connection]]
id           = "web"          # the tool prefix the model sees: web.search
kind         = "local"        # local | ssh | mcp-stdio | mcp-http
command      = "wax"          # kind = local, mcp-stdio
trust        = "auto"         # auto | read
capabilities = ["web.search"] # optional allowlist; empty means every tool
```

`id` is what the model calls (`<id>.<tool>`), so it is stable across
transports: pointing `fsx` at a remote host does not rename a single tool. Ids are lowercase letters, digits, `_` and `-`, 1–64 characters, and are
unique across the whole bot.

`kind` selects how the connection is reached:

| kind | fields | notes |
| --- | --- | --- |
| `local` | `command`, `root`, `env`, `pass_env` | An executable speaking `describe`/`run`. No arguments: wrap the command in a script. |
| `mcp-stdio` | `command`, `args`, `env`, `pass_env`, `timeout` | MCP over stdio. |
| `ssh` | `host`, `known_hosts`, `key`, `root` | Runs [sshx](../../cmd/sshx/README.md): files and commands on a remote host. |
| `mcp-http` | `url`, `headers`, `token_env`, `auth`, `client_id`, `client_secret_env`, `scopes` | Streamable HTTP, OAuth included. |

`capabilities` is a narrowing allowlist. Entries match either the qualified id
(`web.search`) or the bare tool name (`search`). A disabled tool does not appear
in `search` and `execute` rejects it with the list of enabled ones. With no
list, every tool the connection declares is available — the common case.

`trust` is the coarse version of the same idea:

| trust | exposes |
| --- | --- |
| `auto` (default) | every tool the connection declares |
| `read` | only tools annotated read-only |

`read` is for integrations whose writes are not wanted: a repository on a shared
account, a production database, a provider whose destructive tools are one bad
argument away. A tool without the read-only annotation counts as a writer and
stays hidden, so a provider that forgets the annotation fails safe.

## Inspecting

```sh
ax conn list              # id, kind, trust, capabilities, auth, target
ax conn list --json       # the same, for scripts
ax conn doctor            # connect everything: "12 declared, 3 on" + what breaks
ax conn doctor --connection github
```

`doctor` always connects, bypassing the MCP tool cache, and exits non-zero when a
connection fails. It reports declared vs enabled, so a `trust = "read"` or a
narrow `capabilities` list is visible instead of looking like a broken
integration.

## Legacy spellings

Both older forms compile to a connection and keep the ids they had, so catalog
ids and stored sessions stay valid:

```toml
tools = ["wax"]                 # connection{id: "wax", kind: "local", command: "wax"}
AX_TOOLS="wax browserx"         # same, from the environment; replaces `tools` only
[[mcp]] name = "github" ...     # connection{id: "github", kind: "mcp-*"}
```

`AX_TOOLS` replaces the `tools` list and does not touch explicit
`[[connection]]` tables: a connection is a bot-level declaration, `AX_TOOLS` is
a host-level convenience for local providers.

## Compute: local and remote

A connection marked `compute = true` defines the bot's compute: its tools are
the ones the prompt suggests calling directly (the core tools), and its `root`
is the directory every source runs in.

```toml
[[connection]]
id      = "fs"            # the model calls fs.read, fs.write, fs.edit
kind    = "local"
command = "fsx"
compute = true

[[connection]]
id      = "shell"         # and shell.bash
kind    = "local"
command = "bashx"
compute = true
root    = "vol"           # relative to the bot root; must exist
```

- Every compute connection must agree on `root`; two different roots are an
  error, not a silent winner.
- `root` makes the workspace a property of the bot instead of the process: the
  prompt's working directory, every provider's `cmd.Dir` and the MCP servers all
  move together.
- Without `root` the Botdir workspace is used, which is what a bot that never
  mentions compute gets. Without `compute` the core tools stay the historical
  `fsx.read`, `fsx.write`, `fsx.edit`, `bashx.bash`.
- `core_tools` still wins when set: it is the escape hatch, not the normal way.
- A missing root fails the run with a clear error, while `ax conn list` and
  `ax conn doctor` still work and report it.

That is the whole seam for a remote workspace. `root` is a path *on the
backend*: making `fs` an SSH connection later changes `kind`, not the tools, the
prompt or the sessions.

Guarantees are part of the contract, and a backend that cannot provide one must
say so instead of pretending:

| Guarantee | local | ssh (SFTP) | object volume |
| --- | --- | --- | --- |
| atomic rename | yes | `posix-rename@openssh.com`, else temp + rename + warning | no |
| fsync | yes | `fsync@openssh.com`, else warning | no |
| mode/owner | yes | `setstat` (owner needs root) | no |
| symlinks | yes | yes, except Windows servers | no |
| large files | streamed | streamed | multipart |
| read-then-edit safety | mtime + hash | mtime rounds to seconds, so hash | etag |
| glob/grep | local walk | remote readdir or remote `exec` | paginated list |

Two rules follow from the table:

1. Tool calls and the session log use **workspace-relative paths**. An absolute
   path in a stored log cannot survive a change of backend.
2. The remote backend ships as its own provider binary, so `ax` does not carry
   SSH in its size budget. The connection only changes which command it runs.

Order of implementation, cheapest first:

1. Mounting (`sshfs`, NFS, a bind-mounted container volume) keeps `kind` at
   `local`: no adapter, no new failure modes. Prefer it when the host is yours.
2. `kind = "ssh"` for hosts that cannot be mounted: `sshx` speaks the same
   tools over SFTP and an exec channel.

That second case is implemented:

```toml
[[connection]]
id          = "fs"
kind        = "ssh"
host        = "builder@10.0.0.5:22"
root        = "/srv/app"              # a path on the remote host
key         = "secrets/id_ed25519"    # relative to the bot root
known_hosts = "secrets/known_hosts"
compute     = true
```

`ax` does not speak SSH: it runs `sshx` (or `command`, if you point at a
different build) with an environment built from the connection —
`SSHX_HOST`, `SSHX_ROOT`, `SSHX_KEY_FILE`, `SSHX_KNOWN_HOSTS` — and the rest of
the model (ids, capabilities, trust, compute, catalog, sessions) is exactly the
same as a local connection. `known_hosts` is required: a connection that
accepts any host key is a MITM with a shell, and `sshx` has no option for it.
The key must be readable without a passphrase, because a provider process has
no terminal.

`root` on a local connection is the directory its provider runs in; on an ssh
connection it is the remote directory every path is relative to. For remote
compute the local workspace is left alone, so local MCP servers keep working:
`root` is passed to the connection, not stat'ed on this machine.

`env` and `pass_env` exist for providers that need configuration. A connection
that sets either gets exactly that environment instead of inheriting the host's,
so a remote workspace cannot read secrets it never asked for.

3. Object volumes get `push`/`pull` semantics, not a fake POSIX `fs`.
