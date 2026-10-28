#!/usr/bin/env python3
"""Black-box end-to-end harness for ax.

Drives the real binary the way a user would: a hermetic HOME and
XDG_CONFIG_HOME, a scripted mock OpenAI-compatible endpoint, one-shot CLI
runs, and a PTY session for the interactive TUI. Stdlib only.

Usage:
    python3 scripts/harness.py [--bin bin/ax] [--filter name] [--list]

Every case gets a fresh temp HOME. Failures keep their temp dir and print
its path. The mock server answers /chat/completions from a scenario: the
Nth request gets scenario[N], the rest repeat the last entry. Each entry is
one of:

    {"status": 429, "body": "..."}                 error response
    {"stream": [chunk, ...]}                       SSE (when stream:true)
    {"body": {...}}                                plain JSON response

A chunk is a raw OpenAI SSE payload dict. Helpers: content_chunk, tool_chunk,
usage_chunk. The /models endpoint returns two fake models.
"""
import fcntl
import http.server
import json
import os
import pty
import re
import select
import shutil
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time
from contextlib import contextmanager
from pathlib import Path

ANSI_RE = re.compile(
    r"\x1b\[[0-9;:<=>?]*[ -/]*[@-~]"
    r"|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)"
    r"|\x1b[=>]"
    r"|\x1b"
)


def strip_ansi(b):
    if isinstance(b, bytes):
        b = b.decode("utf-8", "replace")
    return ANSI_RE.sub("", b)


def check(cond, msg):
    if not cond:
        raise AssertionError(msg)


# --- mock OpenAI-compatible endpoint -------------------------------------

def content_chunk(text, finish=None):
    return {"choices": [{"delta": {"content": text}, "finish_reason": finish}]}


def tool_chunk(name, args, cid="call_1", finish="tool_calls"):
    return {
        "choices": [
            {
                "delta": {
                    "tool_calls": [
                        {
                            "index": 0,
                            "id": cid,
                            "type": "function",
                            "function": {"name": name, "arguments": json.dumps(args)},
                        }
                    ]
                },
                "finish_reason": finish,
            }
        ]
    }


def usage_chunk(inp, out):
    return {"usage": {"prompt_tokens": inp, "completion_tokens": out}}


ANSWER_SCENARIO = [
    {"stream": [content_chunk("# Done\n\nThe **plan** worked.\n", "stop"), usage_chunk(20, 8)]}
]

TOOL_SCENARIO = [
    {
        "stream": [
            content_chunk("Let me check. "),
            tool_chunk("execute", {"tool": "bashprovider.bash", "arguments": {"command": "echo hello"}}),
            usage_chunk(10, 5),
        ]
    },
    {"stream": [content_chunk("# Done\n\nThe **plan** worked.\n", "stop"), usage_chunk(20, 8)]},
]

WRITE_FILE_SCENARIO = [
    {"stream": [tool_chunk("execute", {"tool": "bashprovider.bash", "arguments": {"command": "printf hi > out.txt"}}), usage_chunk(10, 5)]},
    {"stream": [content_chunk("file written.", "stop"), usage_chunk(20, 8)]},
]

SEARCH_SCENARIO = [
    {"stream": [tool_chunk("search", {"query": "run a shell command"}), usage_chunk(10, 5)]},
    {"stream": [tool_chunk("execute", {"tool": "bashprovider.bash", "arguments": {"command": "echo searched"}}), usage_chunk(10, 5)]},
    {"stream": [content_chunk("found it.", "stop"), usage_chunk(20, 8)]},
]

CONNECTION_SCENARIO = [
    {"stream": [tool_chunk("search", {"query": "run a shell command"}), usage_chunk(10, 5)]},
    {"stream": [tool_chunk("execute", {"tool": "shell.bash", "arguments": {"command": "echo via-connection > conn.txt"}}), usage_chunk(10, 5)]},
    {"stream": [content_chunk("done.", "stop"), usage_chunk(20, 8)]},
]

DIRECT_SCENARIO = [
    {"stream": [tool_chunk("bash", {"command": "printf direct > direct.txt"}), usage_chunk(10, 5)]},
    {"stream": [content_chunk("direct done.", "stop"), usage_chunk(20, 8)]},
]

REWIND_SCENARIO = [
    {"stream": [content_chunk("FIRST ANSWER", "stop"), usage_chunk(5, 3)]},
    {"stream": [content_chunk("SECOND ANSWER", "stop"), usage_chunk(5, 3)]},
]


class MockServer:
    def __init__(self, scenario, chunk_delay=0.01):
        self.scenario = scenario
        self.chunk_delay = chunk_delay
        self.requests = []
        self.lock = threading.Lock()
        self.httpd = None
        self.thread = None

    def start(self):
        self.httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), self._handler())
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()
        return self

    @property
    def base_url(self):
        return "http://127.0.0.1:%d" % self.httpd.server_address[1]

    def stop(self):
        self.httpd.shutdown()
        self.httpd.server_close()
        self.thread.join(timeout=5)

    def _handler(self):
        srv = self

        class H(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.0"

            def log_message(self, *a):
                pass

            def do_GET(self):
                if self.path == "/models":
                    body = json.dumps({"data": [{"id": "model-a"}, {"id": "model-b"}]}).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                else:
                    self.send_response(404)
                    self.end_headers()

            def do_POST(self):
                length = int(self.headers.get("Content-Length", 0))
                raw = self.rfile.read(length) if length else b""
                with srv.lock:
                    idx = len(srv.requests)
                    srv.requests.append(
                        {
                            "method": "POST",
                            "path": self.path,
                            "headers": dict(self.headers),
                            "raw": raw,
                            "json": None,
                        }
                    )
                try:
                    srv.requests[idx]["json"] = json.loads(raw or b"{}")
                except ValueError:
                    pass
                spec = srv.scenario[min(idx, len(srv.scenario) - 1)] if srv.scenario else {}
                status = spec.get("status", 200)
                if status != 200:
                    payload = spec.get("body", '{"error":{"message":"mock error"}}')
                    if isinstance(payload, str):
                        payload = payload.encode()
                    self.send_response(status)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(payload)))
                    self.end_headers()
                    self.wfile.write(payload)
                    return
                req = srv.requests[idx]["json"] or {}
                if req.get("stream") and "stream" in spec:
                    chunks = [b"data: " + json.dumps(c).encode() + b"\n\n" for c in spec["stream"]]
                    chunks.append(b"data: [DONE]\n\n")
                    payload = b"".join(chunks)
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.send_header("Content-Length", str(len(payload)))
                    self.end_headers()
                    for c in chunks:
                        self.wfile.write(c)
                        self.wfile.flush()
                        if srv.chunk_delay:
                            time.sleep(srv.chunk_delay)
                else:
                    body = spec.get("body")
                    if isinstance(body, str):
                        body = json.loads(body)
                    if body is None:
                        body = {
                            "choices": [
                                {"message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}
                            ],
                            "usage": {"prompt_tokens": 1, "completion_tokens": 1},
                        }
                    payload = json.dumps(body).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(payload)))
                    self.end_headers()
                    self.wfile.write(payload)

        return H


# --- hermetic environment -------------------------------------------------

class Harness:
    def __init__(self, root):
        self.root = Path(root)
        self.home = self.root / "home"
        self.config = self.root / "config"
        self.work = self.root / "work"
        for d in (self.home, self.config, self.work, self.ax_root()):
            d.mkdir(parents=True, exist_ok=True)

    def ax_root(self):
        return self.config / "ax"

    def session_root(self):
        value = 0xcbf29ce484222325
        for byte in os.fsencode(self.work.resolve()):
            value ^= byte
            value = value * 0x100000001b3 & 0xffffffffffffffff
        return self.ax_root() / "projects" / f"{value:016x}"

    def write_config(self, text):
        (self.work / "bot.toml").write_text(text)

    def write_secret(self, name, value):
        d = self.work / "secrets"
        d.mkdir(parents=True, exist_ok=True)
        (d / name).write_text(value)

    def write_command(self, name, text):
        d = self.ax_root() / "commands"
        d.mkdir(parents=True, exist_ok=True)
        (d / (name + ".md")).write_text(text)

    def seed_session(self, sid, title, content):
        self.seed_session_msgs(sid, title, [{"Role": "user", "Content": content}])

    def seed_session_msgs(self, sid, title, msgs):
        d = self.session_root() / "sessions"
        d.mkdir(parents=True, exist_ok=True)
        out = "".join(json.dumps({"type": "message", "message": m}) + "\n" for m in msgs)
        (d / (sid + ".jsonl")).write_text(out)
        if title:
            (d / (sid + ".title")).write_text(title)

    def env(self, api_key="sk-test", extra=None):
        e = dict(os.environ)
        e.update(
            {
                "HOME": str(self.home),
                "XDG_CONFIG_HOME": str(self.config),
                "TERM": "xterm-256color",
                "LANG": "C.UTF-8",
                "LC_ALL": "C.UTF-8",
            }
        )
        e.pop("TMUX", None)
        e.pop("OPENAI_API_KEY", None)
        if api_key is not None:
            e["OPENAI_API_KEY"] = api_key
        if extra:
            e.update(extra)
        return e

    @contextmanager
    def mock(self, scenario, **kw):
        srv = MockServer(scenario, **kw).start()
        try:
            yield srv
        finally:
            srv.stop()

    def oneshot(self, ax, args, stdin=None, base=None, api_key="sk-test", extra_env=None):
        if base:
            args = ["-base", base] + args
        r = subprocess.run(
            [ax] + args,
            input=stdin,
            capture_output=True,
            env=self.env(api_key=api_key, extra=extra_env),
            timeout=120,
            cwd=str(self.work),
        )
        r.stdout = r.stdout.decode("utf-8", "replace")
        r.stderr = r.stderr.decode("utf-8", "replace")
        return r

    def tui(self, ax, args=None, base=None, api_key="sk-test", extra_env=None, cols=100, rows=30):
        if base:
            args = ["-base", base] + (args or [])
        return Tui(ax, self.env(api_key=api_key, extra=extra_env), args or [], cwd=str(self.work), cols=cols, rows=rows)


# --- PTY driver for the TUI ----------------------------------------------

KEYS = {
    "enter": b"\r",
    "esc": b"\x1b",
    "tab": b"\t",
    "backspace": b"\x7f",
    "up": b"\x1b[A",
    "down": b"\x1b[B",
    "ctrl_c": b"\x03",
    "shift_enter": b"\x1b[13;2u",
    "eof": b"\x04",
}


class Tui:
    def __init__(self, ax, env, args, cwd, cols=100, rows=30):
        self.master, slave = pty.openpty()
        fcntl.ioctl(self.master, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        self.proc = subprocess.Popen(
            [ax] + args, stdin=slave, stdout=slave, stderr=slave, env=env, cwd=cwd
        )
        os.close(slave)
        self.buf = b""
        self._lock = threading.Lock()
        self._stop = threading.Event()
        self._thread = threading.Thread(target=self._pump, daemon=True)
        self._thread.start()

    def _pump(self):
        # The TUI repaints constantly; if the master is not drained the PTY
        # output buffer fills and the TUI blocks on write, never reading keys.
        while not self._stop.is_set():
            r, _, _ = select.select([self.master], [], [], 0.05)
            if not r:
                continue
            try:
                data = os.read(self.master, 65536)
            except OSError:
                return
            if not data:
                return
            with self._lock:
                self.buf += data

    def send(self, text):
        try:
            os.write(self.master, text.encode() if isinstance(text, str) else text)
        except OSError:
            pass

    def type(self, text, delay=0.01):
        for ch in text:
            self.send(ch)
            time.sleep(delay)

    def key(self, name):
        self.send(KEYS[name])

    def output(self):
        with self._lock:
            return strip_ansi(self.buf)

    def expect(self, text, timeout=20):
        deadline = time.time() + timeout
        while time.time() < deadline:
            if text in self.output():
                return
            if self.proc.poll() is not None:
                time.sleep(0.1)
                if text in self.output():
                    return
                raise AssertionError(
                    "tui exited (code %s) before %r appeared\n--- output ---\n%s"
                    % (self.proc.poll(), text, self.output()[-4000:])
                )
            time.sleep(0.05)
        raise AssertionError(
            "timeout waiting for %r\n--- output ---\n%s" % (text, self.output()[-4000:])
        )

    def wait_exit(self, timeout=20):
        deadline = time.time() + timeout
        while time.time() < deadline:
            code = self.proc.poll()
            if code is not None:
                time.sleep(0.1)
                return code
            time.sleep(0.05)
        self.proc.kill()
        raise AssertionError("tui did not exit\n--- output ---\n%s" % self.output()[-4000:])

    def close(self):
        self._stop.set()
        if self.proc.poll() is None:
            self.proc.kill()
            self.proc.wait(timeout=5)
        self._thread.join(timeout=2)
        try:
            os.close(self.master)
        except OSError:
            pass


# --- test cases ----------------------------------------------------------

CASES = []


def case(fn):
    CASES.append(fn)
    return fn


def body_msg(server, i, role):
    return [m for m in server.requests[i]["json"]["messages"] if m["role"] == role]


def read_events(p):
    return [json.loads(line) for line in p.stdout.splitlines()]


def last_result(events):
    return [e for e in events if e.get("type") == "result"][-1]


def bash_provider(h):
    sh = h.root / "bashprovider"
    sh.write_text(
        """#!/usr/bin/python3
import json, subprocess, sys
if len(sys.argv) > 1 and sys.argv[1] == "describe":
    print(json.dumps({"name": "bash", "description": "Run a shell command", "parameters": {"type": "object", "properties": {"command": {"type": "string"}}, "required": ["command"]}}))
    sys.exit(0)
if len(sys.argv) > 2 and sys.argv[1] == "run" and sys.argv[2] == "bash":
    args = json.load(sys.stdin)
    r = subprocess.run(args["command"], shell=True, capture_output=True, text=True)
    sys.stdout.write(r.stdout)
    sys.exit(0)
sys.exit(1)
"""
    )
    sh.chmod(0o755)
    return sh


@case
def oneshot_prints_answer(h, ax):
    with h.mock(ANSWER_SCENARIO) as srv:
        p = h.oneshot(ax, ["--events", "hello"], base=srv.base_url)
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        events = read_events(p)
        check(events[0] == {"type": "protocol", "version": 1}, "bad protocol event")
        check(events[-1] == {"type": "done", "outcome": "done"}, "bad final event")
        content = [m["Content"] for m in last_result(events)["messages"] if m["Role"] == "assistant"]
        check(any("Done" in c for c in content), "assistant result missing: %s" % content)
        check(len(srv.requests) == 1, "expected 1 request, got %d" % len(srv.requests))
        check(body_msg(srv, 0, "user")[-1]["content"] == "hello", "prompt not sent")
        check(
            srv.requests[0]["headers"].get("Authorization") == "Bearer sk-test",
            "auth header wrong: %s" % srv.requests[0]["headers"],
        )


@case
def oneshot_stdin(h, ax):
    with h.mock(ANSWER_SCENARIO) as srv:
        p = h.oneshot(ax, [], stdin=b"hello from stdin", base=srv.base_url)
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        check("Done" in p.stdout, "stdout: %s" % p.stdout[-500:])
        check(
            body_msg(srv, 0, "user")[-1]["content"] == "hello from stdin",
            "stdin prompt not sent",
        )


@case
def oneshot_events(h, ax):
    with h.mock(ANSWER_SCENARIO) as srv:
        p = h.oneshot(ax, ["--events", "hello"], stdin=b"", base=srv.base_url)
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        events = [json.loads(line) for line in p.stdout.splitlines()]
        check(events[0] == {"type": "protocol", "version": 1}, "bad protocol event")
        check(any(e.get("type") == "assistant_delta" for e in events), "no assistant delta")
        check(any(e.get("type") == "usage" for e in events), "no usage event")
        check(events[-1] == {"type": "done", "outcome": "done"}, "bad final event")



@case
def oneshot_explicit_session(h, ax):
    session = h.root / "thread.jsonl"
    with h.mock(ANSWER_SCENARIO) as srv:
        first = h.oneshot(ax, ["--session", str(session), "first"], base=srv.base_url)
        second = h.oneshot(ax, ["--session", str(session), "second"], base=srv.base_url)
        check(first.returncode == 0 and second.returncode == 0, "session call failed")
        check(session.is_file(), "session was not written")
        users = body_msg(srv, 1, "user")
        check([m["content"] for m in users] == ["first", "second"], "session context lost")


@case
def oneshot_tool_loop(h, ax):
    with h.mock(TOOL_SCENARIO) as srv:
        p = h.oneshot(
            ax,
            ["--events", "use tools"],
            base=srv.base_url,
            extra_env={"AX_TOOLS": str(bash_provider(h))},
        )
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        events = read_events(p)
        check(
            any(e.get("type") == "tool_start" and e.get("name") == "execute" for e in events),
            "no tool_start event",
        )
        content = [m["Content"] for m in last_result(events)["messages"] if m["Role"] == "assistant"]
        check(any("Done" in c for c in content), "assistant result missing: %s" % content)
        check(len(srv.requests) == 2, "expected 2 requests, got %d" % len(srv.requests))
        tool_msgs = body_msg(srv, 1, "tool")
        check(
            any("hello" in m.get("content", "") for m in tool_msgs),
            "tool result not fed back: %s" % tool_msgs,
        )


@case
def oneshot_catalog_search(h, ax):
    with h.mock(SEARCH_SCENARIO) as srv:
        p = h.oneshot(
            ax,
            ["--events", "find a tool"],
            base=srv.base_url,
            extra_env={"AX_TOOLS": str(bash_provider(h))},
        )
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        events = read_events(p)
        check(
            any(e.get("type") == "tool_start" and e.get("name") == "search" for e in events),
            "no search tool_start",
        )
        check(
            any(
                e.get("type") == "tool_start"
                and e.get("name") == "execute"
                and e.get("target") == "bashprovider.bash"
                for e in events
            ),
            "no execute target event",
        )
        tool_msgs = body_msg(srv, 1, "tool")
        check(
            any("bashprovider.bash" in m.get("content", "") for m in tool_msgs),
            "search result not fed back: %s" % tool_msgs,
        )


@case
def oneshot_direct_tools(h, ax):
    with h.mock(DIRECT_SCENARIO) as srv:
        p = h.oneshot(
            ax,
            ["-tool-mode", "direct", "write directly"],
            base=srv.base_url,
            extra_env={"AX_TOOLS": str(bash_provider(h))},
        )
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        check((h.work / "direct.txt").read_text() == "direct", "direct tool did not run")


@case
def mcp_add_from_registry(h, ax):
    p = h.oneshot(ax, ["mcp", "add", "sentry"])
    check(p.returncode == 0, "add exit %s: %s" % (p.returncode, p.stderr[-500:]))
    toml = (h.work / "bot.toml").read_text()
    check("[[connection]]" in toml and 'id = "sentry"' in toml, "bot.toml missing the connection: %s" % toml)
    check('kind = "mcp-http"' in toml, "connection is not an MCP kind: %s" % toml)
    check("mcp.sentry.dev" in toml, "bot.toml missing url: %s" % toml)
    listed = h.oneshot(ax, ["mcp", "list", "--json"])
    check(listed.returncode == 0, "list exit %s: %s" % (listed.returncode, listed.stderr[-500:]))
    servers = json.loads(listed.stdout)
    check(len(servers) == 1 and servers[0]["name"] == "sentry", "listed: %s" % servers)
    check(servers[0]["auth"] == "oauth", "auth kind wrong: %s" % servers)
    check(servers[0]["status"] == "login required", "status wrong: %s" % servers)


@case
def mcp_import_document(h, ax):
    document = {
        "mcpServers": {
            "linear": {"type": "http", "url": "https://mcp.linear.app/mcp"},
            "files": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-memory"]},
            "gdocs": {"url": "https://api.cursor.com/rest-mcp/google-docs/mcp"},
        }
    }
    path = h.work / "mcp.json"
    path.write_text(json.dumps(document))
    p = h.oneshot(ax, ["mcp", "import", str(path)])
    check(p.returncode == 0, "import exit %s: %s" % (p.returncode, p.stderr[-500:]))
    check("Cursor-hosted" in p.stderr, "no skip reason: %s" % p.stderr[-500:])
    toml = (h.work / "bot.toml").read_text()
    check('id = "linear"' in toml and 'id = "files"' in toml, "bot.toml: %s" % toml)
    check("cursor.com" not in toml, "imported a Cursor-only endpoint: %s" % toml)
    again = h.oneshot(ax, ["mcp", "import", str(path)])
    check("already configured" in again.stderr, "second import: %s" % again.stderr[-500:])


COMPACT_SCENARIO = [
    # First turn: a tool call and a prompt that is already over half the window.
    {
        "stream": [
            tool_chunk("execute", {"tool": "bashprovider.bash", "arguments": {"command": "echo busy"}}),
            usage_chunk(8000, 5),
        ]
    },
    # The summarizer's own request.
    {"stream": [content_chunk("SUMMARY: the user asked for work; a tool ran.", "stop"), usage_chunk(50, 12)]},
    # The run continues with the summary in the system prompt.
    {"stream": [content_chunk("# Done\n\nCompacted and finished.\n", "stop"), usage_chunk(60, 8)]},
]


@case
def connections_list_and_doctor(h, ax):
    provider = bash_provider(h)
    h.write_config(
        "[[connection]]\n"
        'id = "shell"\n'
        'kind = "local"\n'
        'command = "%s"\n'
        'capabilities = ["shell.bash"]\n' % provider
    )
    p = h.oneshot(ax, ["conn", "list", "--json"], stdin=b"")
    check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
    listed = json.loads(p.stdout)
    check(len(listed) == 1, "list = %s" % listed)
    check(listed[0]["id"] == "shell", "id = %s" % listed[0])
    check(listed[0]["kind"] == "local", "kind = %s" % listed[0])
    check(listed[0]["capabilities"] == "1", "capabilities = %s" % listed[0])

    p = h.oneshot(ax, ["conn", "doctor"], stdin=b"")
    check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
    check("1 declared, 1 on" in p.stdout, "doctor = %s" % p.stdout)
    check("ready" in p.stdout, "doctor = %s" % p.stdout)


@case
def oneshot_connection_id_reaches_the_model(h, ax):
    provider = bash_provider(h)
    h.write_config(
        "[[connection]]\n"
        'id = "shell"\n'
        'kind = "local"\n'
        'command = "%s"\n' % provider
    )
    with h.mock(CONNECTION_SCENARIO) as srv:
        p = h.oneshot(ax, ["--events", "run something"], base=srv.base_url)
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        body = json.dumps(srv.requests[0]["json"]["messages"])
        check("Catalog servers: shell (1)" in body, "connection missing from the prompt: %s" % body[:1000])
        tool_msgs = body_msg(srv, 1, "tool")
        check(
            any("shell.bash" in m.get("content", "") for m in tool_msgs),
            "search result has no connection id: %s" % tool_msgs,
        )
        check(
            not any("bashprovider" in m.get("content", "") for m in tool_msgs),
            "the binary name leaked into the catalog: %s" % tool_msgs,
        )
        events = read_events(p)
        check(
            any(
                e.get("type") == "tool_start"
                and e.get("name") == "execute"
                and e.get("target") == "shell.bash"
                for e in events
            ),
            "execute did not route through the connection id",
        )
        check((h.work / "workspace" / "conn.txt").read_text().strip() == "via-connection", "the tool did not run")


@case
def oneshot_compute_connection_sets_the_workspace(h, ax):
    provider = bash_provider(h)
    ws = h.work / "vol"
    ws.mkdir()
    h.write_config(
        "[[connection]]\n"
        'id = "shell"\n'
        'kind = "local"\n'
        'compute = true\n'
        'root = "vol"\n'
        'command = "%s"\n' % provider
    )
    with h.mock(CONNECTION_SCENARIO) as srv:
        p = h.oneshot(ax, ["--events", "run something"], base=srv.base_url)
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        system = body_msg(srv, 0, "system")[0]["content"]
        check("Current working directory: %s" % ws in system, "workspace not applied: %s" % system[-800:])
        check("shell.bash" in system, "compute tools missing from the prompt: %s" % system[-800:])
        check("fsx.read" not in system, "default core tools leaked into the prompt")
        check((ws / "conn.txt").read_text().strip() == "via-connection", "tool did not run in the compute root")
        check(not (h.work / "workspace" / "conn.txt").exists(), "tool ran in the default workspace")


@case
def oneshot_compute_root_must_exist(h, ax):
    h.write_config(
        "[[connection]]\n"
        'id = "shell"\n'
        'compute = true\n'
        'root = "missing"\n'
        'command = "%s"\n' % bash_provider(h)
    )
    p = h.oneshot(ax, ["hello"], stdin=b"")
    check(p.returncode != 0, "a missing compute root must fail")
    check("root" in p.stderr and "missing" in p.stderr, "stderr = %s" % p.stderr)


@case
def oneshot_auto_compaction(h, ax):
    session = h.root / "compact.jsonl"
    h.write_config("compact_at = 0.5\ncontext_window = 10000\n")
    with h.mock(COMPACT_SCENARIO) as srv:
        p = h.oneshot(
            ax,
            ["--events", "--session", str(session), "keep working"],
            base=srv.base_url,
            extra_env={"AX_TOOLS": str(bash_provider(h))},
        )
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        events = read_events(p)
        check(
            any(e.get("type") == "compaction" and e.get("summary") for e in events),
            "no compaction event: %s" % events[:5],
        )
        check(events[-1] == {"type": "done", "outcome": "done"}, "bad final event")
        systems = [
            m["content"]
            for m in srv.requests[-1]["json"]["messages"]
            if m["role"] == "system"
        ]
        check(
            any("Summary of the earlier conversation" in s for s in systems),
            "summary missing from the system prompt",
        )
        check(len(srv.requests) == 3, "expected 3 requests, got %d" % len(srv.requests))
    entries = [json.loads(line) for line in session.read_text().splitlines() if line.strip()]
    check(
        any(e.get("type") == "compaction" and e.get("upto") for e in entries),
        "session log has no compaction entry",
    )


@case
def oneshot_tool_workdir(h, ax):
    with h.mock(WRITE_FILE_SCENARIO) as srv:
        p = h.oneshot(
            ax,
            ["-C", str(h.work), "write a file"],
            base=srv.base_url,
            extra_env={"AX_TOOLS": str(bash_provider(h))},
        )
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        check((h.work / "out.txt").read_text() == "hi", "tool ran in wrong dir")


@case
def oneshot_flags_and_config(h, ax):
    h.write_config('base_url = "http://127.0.0.1:9"\nmodel = "cfg-model"\n')
    with h.mock(ANSWER_SCENARIO) as srv:
        p = h.oneshot(ax, ["-model", "flag-model", "-system", "You are Taco.", "hi"], base=srv.base_url)
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        req = srv.requests[0]["json"]
        check(req["model"] == "flag-model", "flag model lost: %s" % req["model"])
        sys_msgs = [m for m in req["messages"] if m["role"] == "system"]
        check(any("Taco" in m.get("content", "") for m in sys_msgs), "system flag lost")
    with h.mock(ANSWER_SCENARIO) as srv:
        p = h.oneshot(ax, ["hi"], base=srv.base_url)
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        check(srv.requests[0]["json"]["model"] == "cfg-model", "config model ignored")


@case
def oneshot_config_api_key(h, ax):
    h.write_secret("api-key", "cfg-key")
    with h.mock(ANSWER_SCENARIO) as srv:
        p = h.oneshot(ax, ["hi"], base=srv.base_url, api_key=None)
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        check(
            srv.requests[0]["headers"].get("Authorization") == "Bearer cfg-key",
            "config key not used",
        )


@case
def oneshot_env_beats_config(h, ax):
    h.write_secret("api-key", "cfg-key")
    with h.mock(ANSWER_SCENARIO) as srv:
        p = h.oneshot(ax, ["hi"], base=srv.base_url, api_key="env-key")
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        check(
            srv.requests[0]["headers"].get("Authorization") == "Bearer env-key",
            "env key should win over config",
        )


@case
def oneshot_retry(h, ax):
    scenario = [
        {"status": 429, "body": '{"error":{"message":"rate limited"}}'},
        {"status": 500, "body": "boom"},
        ANSWER_SCENARIO[0],
    ]
    with h.mock(scenario) as srv:
        p = h.oneshot(ax, ["hi"], base=srv.base_url)
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        check(len(srv.requests) == 3, "expected 3 requests, got %d" % len(srv.requests))
        statuses = [r["headers"].get("Content-Length") for r in srv.requests]
        check(statuses, "requests recorded without bodies")


@case
def oneshot_retry_exhausted(h, ax):
    scenario = [{"status": 429, "body": '{"error":{"message":"rate limited"}}'}] * 3
    with h.mock(scenario) as srv:
        p = h.oneshot(ax, ["--events", "hi"], base=srv.base_url)
        check(p.returncode == 1, "expected exit 1, got %s" % p.returncode)
        events = read_events(p)
        err = [e for e in events if e.get("type") == "error"][-1]
        check("429" in err["message"] and "rate limited" in err["message"], "error event: %s" % err)
        check(len(srv.requests) == 3, "expected 3 attempts, got %d" % len(srv.requests))


@case
def oneshot_provider_down(h, ax):
    p = h.oneshot(ax, ["--events", "hi"], base="http://127.0.0.1:1")
    check(p.returncode == 1, "expected exit 1, got %s" % p.returncode)
    events = read_events(p)
    check(any(e.get("type") == "error" for e in events), "no error event: %s" % p.stdout[-500:])
    check(events[-1] == {"type": "done", "outcome": "failed"}, "bad final event: %s" % events[-1])


@case
def oneshot_bad_flag(h, ax):
    p = h.oneshot(ax, ["-nope"])
    check(p.returncode == 2, "expected exit 2, got %s" % p.returncode)
    check("flag provided but not defined" in p.stderr, "stderr: %s" % p.stderr[-500:])


@case
def oneshot_help(h, ax):
    p = h.oneshot(ax, ["--help"])
    check(p.returncode == 0, "expected exit 0, got %s" % p.returncode)
    check("Usage: ax" in p.stderr, "stderr: %s" % p.stderr[-500:])


@case
def oneshot_user_command(h, ax):
    h.write_command("commit", "stage everything now")
    with h.mock(ANSWER_SCENARIO) as srv:
        p = h.oneshot(ax, ["/commit"], base=srv.base_url)
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        check(
            body_msg(srv, 0, "user")[-1]["content"] == "stage everything now",
            "user command not expanded",
        )




@case
def oneshot_bot_config_and_prompt(h, ax):
    h.write_config('model = "bot-model"\n')
    (h.work / "bot.md").write_text("You are the bot instructions.")
    with h.mock(ANSWER_SCENARIO) as srv:
        p = h.oneshot(ax, ["hi"], base=srv.base_url)
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        req = srv.requests[0]["json"]
        check(req["model"] == "bot-model", "bot.toml model ignored: %s" % req["model"])
        sys_msgs = [m for m in req["messages"] if m["role"] == "system"]
        check(any("bot instructions" in m.get("content", "") for m in sys_msgs), "bot.md not in system")


@case
def oneshot_bot_unknown_keys(h, ax):
    h.write_config('model = "bot-model"\n\n[slack]\nmention_only = true\nnewkey = "x"\n')
    with h.mock(ANSWER_SCENARIO) as srv:
        p = h.oneshot(ax, ["hi"], base=srv.base_url)
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        check(srv.requests[0]["json"]["model"] == "bot-model", "model lost with unknown keys")


@case
def oneshot_bot_workspace(h, ax):
    h.write_config('model = "bot-model"\n')
    ws = h.work / "workspace"
    ws.mkdir()
    with h.mock(WRITE_FILE_SCENARIO) as srv:
        p = h.oneshot(
            ax,
            ["write a file"],
            base=srv.base_url,
            extra_env={"AX_TOOLS": str(bash_provider(h))},
        )
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        check((ws / "out.txt").read_text() == "hi", "tool did not run in workspace")


@case
def oneshot_bot_workspace_split(h, ax):
    h.write_config('model = "bot-model"\n')
    data = h.root / "data"
    ws = data / "workspace"
    ws.mkdir(parents=True)
    with h.mock(WRITE_FILE_SCENARIO) as srv:
        p = h.oneshot(
            ax,
            ["write a file"],
            base=srv.base_url,
            extra_env={"AX_TOOLS": str(bash_provider(h)), "BOT_DATA": str(data)},
        )
        check(p.returncode == 0, "exit %s: %s" % (p.returncode, p.stderr[-500:]))
        check((ws / "out.txt").read_text() == "hi", "tool did not run in BOT_DATA workspace")
        check(not (h.work / "workspace" / "out.txt").exists(), "tool wrote to bot root workspace instead of BOT_DATA")


def main(argv):
    ax = "bin/ax"
    filt = None
    i = 0
    while i < len(argv):
        a = argv[i]
        if a == "--bin":
            i += 1
            if i >= len(argv):
                usage()
            ax = argv[i]
        elif a == "--filter":
            i += 1
            if i >= len(argv):
                usage()
            filt = argv[i]
        elif a == "--list":
            for fn in CASES:
                print(fn.__name__)
            return 0
        elif a in ("-h", "--help"):
            usage()
        else:
            usage()
        i += 1
    ax = os.path.abspath(ax)
    if not os.path.exists(ax):
        print("binary not found: %s (build first: make harness)" % ax)
        return 2

    failed = []
    for fn in CASES:
        if filt and filt not in fn.__name__:
            continue
        tmp = tempfile.mkdtemp(prefix="ax-harness-")
        h = Harness(tmp)
        t0 = time.time()
        try:
            fn(h, ax)
            shutil.rmtree(tmp, ignore_errors=True)
            print("PASS %-28s %5.1fs" % (fn.__name__, time.time() - t0))
        except Exception as e:
            failed.append(fn.__name__)
            print("FAIL %-28s %s" % (fn.__name__, e))
            print("     artifacts kept in: %s" % tmp)
    print()
    if failed:
        print("%d/%d cases failed: %s" % (len(failed), len(CASES), ", ".join(failed)))
        return 1
    print("all %d cases passed" % len(CASES))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
