# wax

Fetch a web page as Markdown, for `ax` and other LLM tools. HTTP first; when a page has little useful text, it renders with an installed Chromium and converts the final HTML to Markdown.

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

Enable it through `ax` tool discovery: `AX_TOOLS="wax" ax`.

## Build and test

```sh
make check
make bin/wax
```

On restricted CI runners where Chromium cannot create its sandbox, run with `WAX_NO_SANDBOX=1`. Do not disable the sandbox for untrusted pages unless the runner already isolates the process.
