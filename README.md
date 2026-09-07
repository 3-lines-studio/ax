# ax

A minimal headless LLM coding-agent harness for scripts and Unix-style clients. ~7 MB single binary; stdlib HTTP, no libcurl.

## Install

```sh
curl -fsSL https://ax.3lines.studio/install.sh | sh
```

Installs to `~/.local/bin` (override with `AX_PREFIX`). Linux x86-64/ARM64 and macOS ARM64, all SHA-256 verified.

## Run

```sh
OPENAI_API_KEY=... ax "explain main.go"
```

`ax` is a [Botdir](https://github.com/3-lines-studio/ax-ecosystem) consumer. The bot root is the current directory (or `-C DIR`); it reads `bot.toml` there:

```toml
model = "gpt-4.1-mini"
base_url = "https://api.openai.com/v1"
tools = ["fsx", "bashx", "skillx"]
```

Any OpenAI-compatible endpoint works (`-base https://...`: OpenRouter, DeepSeek, Ollama, vLLM). The model key comes from `OPENAI_API_KEY` or `secrets/api-key`. Precedence is **flags > `bot.toml` > `~/.config/ax/config` > defaults**; `OPENAI_API_KEY` and `AX_TOOLS` come from the environment. When the bot root has `workspace/`, the model and tools run there.

## Tools

`ax` has no built-in tools — register every capability explicitly:

```sh
AX_TOOLS="fsx bashx skillx" ax
```

At startup `ax` runs `<command> describe` (one JSON spec per line: name, description, JSON-Schema parameters). For a call it runs `<command> run <name>`, writes the JSON arguments to stdin, reads the result from stdout, and treats a non-zero exit as a tool error. See the [tool contract](docs/tools.md).

## Session

`ax` emits a JSONL event stream on stdout: assistant deltas, tool events, usage, errors, and one final `done`. Send `{"type":"cancel"}` on stdin to stop. Persist a run with `--session FILE` (JSONL, append-only; a Botdir host routes them under `state/ax/`).

## Build and test

```sh
go build -trimpath -ldflags='-s -w' -o ax .
go test ./...
make harness     # black-box end-to-end harness (scripts/harness.py)

# real-provider botdir eval (scripts/eval.py):
#   set OPENAI_API_KEY, AX_BASE_URL, and AX_MODEL, then:
make eval
```

`eval` drives the real `ax` binary against a real provider through a real Botdir bot root and the real tool providers, asserting on observable artifacts. It skips cleanly until `OPENAI_API_KEY`/`AX_BASE_URL`/`AX_MODEL` are set. Put those in a gitignored `.env` file next to this README (or export them) to run it.

See [CONTRIBUTING.md](CONTRIBUTING.md) for internals.
