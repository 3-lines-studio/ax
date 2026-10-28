# Connections

A **connection** is one integration the bot has: a local provider, an MCP
server, and later a remote workspace. The connection, not its tools, is the
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
kind         = "local"        # local | mcp-stdio | mcp-http
command      = "wax"          # kind = local, mcp-stdio
capabilities = ["web.search"] # optional allowlist; empty means every tool
```

`id` is what the model calls (`<id>.<tool>`), so it is stable across
transports: pointing `fsx` at a remote workspace later must not rename a single
tool. Ids are lowercase letters, digits, `_` and `-`, 1–64 characters, and are
unique across the whole bot.

`kind` selects how the connection is reached:

| kind | fields | notes |
| --- | --- | --- |
| `local` | `command` | An executable speaking `describe`/`run`. No arguments: wrap the command in a script. |
| `mcp-stdio` | `command`, `args`, `env`, `pass_env`, `timeout` | MCP over stdio. |
| `mcp-http` | `url`, `headers`, `token_env`, `auth`, `client_id`, `client_secret_env`, `scopes` | Streamable HTTP, OAuth included. |

`capabilities` is a narrowing allowlist. Entries match either the qualified id
(`web.search`) or the bare tool name (`search`). A disabled tool does not appear
in `search` and `execute` rejects it with the list of enabled ones. With no
list, every tool the connection declares is available — the common case.

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

`read`, `write`, `edit` and `bash` are today the tools of an implicit local
connection (`fsx` and `bashx` in `core_tools`). Naming that connection is the
next phase; the point of doing it is to let the same tools reach a different
workspace without changing the model surface.

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
2. `kind = "ssh"` for hosts that cannot be mounted: SFTP for `fs.*`, an exec
   channel for `bash`, host key pinned in the connection (never
   `InsecureIgnoreHostKey`), key from the vault, `secrets/` never forwarded.
3. Object volumes get `push`/`pull` semantics, not a fake POSIX `fs`.
