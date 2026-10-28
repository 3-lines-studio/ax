# wax

Web search and fetch for `ax` and other LLM tools. `web_search` uses DeepSeek's
Search API; `web_fetch` gets a page as Markdown, HTTP first and Chromium when a
page needs rendering.

## Install

```sh
curl -fsSL https://github.com/3-lines-studio/ax/releases/latest/download/install.sh | sh -s -- wax
```

Linux x86-64/ARM64, macOS ARM64. Chromium (or Chrome) is required only when a page needs rendering.

## Use

```sh
wax https://example.com
```

Markdown goes to stdout, errors to stderr. It also implements the `ax` external-tool protocol:

```sh
wax describe
printf '%s' '{"url":"https://example.com"}' | wax run web_fetch
```

Declare it as a local connection:

```toml
[[connection]]
id = "wax"
kind = "local"
command = "wax"
```


## Search

Search uses the same key as the model, so a bot that already talks to DeepSeek
needs nothing new. `wax` resolves it like `ax` does, first hit wins:

1. `DEEPSEEK_API_KEY`
2. `OPENAI_API_KEY`
3. `secrets/api-key` in the bot root (`BOT_ROOT`, else the current directory)
4. `api_key` in `~/.config/ax/config`

`web_search` is advertised by `describe` only when one of those resolves:
DeepSeek bills the Search API per request ($0.75 per 1,000), so wax does not
offer a tool that cannot run.

```sh
wax search "deepseek v4 release" -count 5      # Markdown, for humans
wax search "rust async" -content               # include page text
wax search "site:github.com tokio" -json       # structured results
printf '%s' '{"query":"deepseek","count":3}' | wax run web_search
wax describe                                    # now lists web_search too
```

With a `wax` connection the model sees `wax.web_search` and `wax.web_fetch`; no
extra environment variable is needed beyond the key the bot already uses.

The query supports DeepSeek's operators (`site:`, `-site:`, `lang:`, `after:`,
`before:`, `"exact phrase"`, `-term`). `DEEPSEEK_BASE_URL` overrides the
endpoint (a trailing `/v1` is fine). The tool returns titles, links, site,
date, authority and the long summary for each result, and retries the statuses
DeepSeek documents as retryable (408, 429, 5xx).

`web_fetch` stays local: it renders JavaScript with Chromium, which the
DeepSeek Fetch API does not do.

## Build and test

```sh
make check
make bin/wax
```

On restricted CI runners where Chromium cannot create its sandbox, run with `WAX_NO_SANDBOX=1`. Do not disable the sandbox for untrusted pages unless the runner already isolates the process.
