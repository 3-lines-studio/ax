# Contributing

## Quality gate

Run before finishing any change:

```bash
cargo fmt && cargo clippy --all-targets -- -D warnings && cargo test && cargo build --release
```

All four must pass. The last command catches linker/panic-abort issues that
tests miss.

Cheaper daily loop (autofixes without the gate):

```bash
cargo clippy --fix --allow-dirty && cargo fmt && cargo test
```

## Known exceptions

- `src/markdown.rs` is a byte-exact port of fx's markdown renderer. Clippy
  reports ~16 style nits there (`type_complexity`, redundant `as_bytes`).
  Leave them. Do not refactor the port's code or add allow-attributes unless
  explicitly asked.
- `src/curlffi.rs` dlopens libcurl at runtime. The dlsym→fn-pointer
  transmutes are deliberate and annotated with explicit types. Do not
  "simplify" them.

## Project rules

- Release binary stays under 1 MB (currently ~785 KB).
- Dependencies are exactly `serde`, `serde_json`, `libc`. No new deps
  without asking. No compile-time curl.
- Extensibility is files-over-code: skills, user commands, SYSTEM.md. No
  plugin system, no MCP, no permissions layer.
- Config lives at `~/.config/ax/`; skills at `~/.agents/skills/`.
- Never push to git. Never generate or run DB migrations (there is no DB).
- Match existing code style. Touch only what a change requires.
