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

- `src/markdown.rs` is a byte-exact port of fx's markdown renderer. It has
  one targeted `#[allow(clippy::needless_update)]` on the `PROFILES` table:
  the `p!` macro legitimately fills partial field sets from `Profile::empty()`.
  Do not remove it or refactor the port's code beyond lint fixes.
- `src/curlffi.rs` dlopens libcurl at runtime. The dlsym→fn-pointer
  transmutes are deliberate and annotated with explicit types. Do not
  "simplify" them.

## Project rules

- Release binary stays under 1 MB (currently ~920 KB).
- Dependencies are exactly `serde`, `serde_json`, `libc`. No new deps
  without asking. No compile-time curl.
- Extensibility is files-over-code: skills, user commands, SYSTEM.md. No
  plugin system, no MCP, no permissions layer.
- Config lives at `~/.config/ax/`; skills at `~/.agents/skills/`.
