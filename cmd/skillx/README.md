# Skillx

Skillx provides Agent Skills through the Unix tool convention.

```sh
skillx describe
printf '%s\n' '{}' | skillx run skills
printf '%s\n' '{"name":"example"}' | skillx run skill
```

Tools:

```text
skills
skill
```

Skillx reads `$BOT_ROOT/skills` when running inside a bot; otherwise it merges the current directory's `./skills/` (falling back to the legacy `./.agents/skills` when `skills/` is absent) and `$HOME/.agents/skills`. Project skills take precedence when both roots contain the same name. Missing default roots are ignored. Skillx uses a separate Go `os.Root` for each root and strict skill names to confine reads.

Run the local coding stack:

```sh
AX_TOOLS="fsx bashx skillx" ax
```

## Build

```sh
make bin/skillx
go test ./cmd/skillx
```

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- skillx
```
