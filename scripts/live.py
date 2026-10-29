#!/usr/bin/env python3
import json
import os
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
BASE_URL = os.environ.get("AX_BASE_URL") or "https://api.deepseek.com"
MODEL = os.environ.get("AX_MODEL") or "deepseek-flash"
PROVIDERS = ("fsx", "bashx", "skillx", "memoryx", "sshx", "bqx", "pgx", "slackx", "attachx", "browserx", "wax")
LAYERS = (
    "ax_cli",
    "ax_events",
    "ax_tools",
    "ax_mcp_stdio",
    "ax_mcp_http",
    "ax_boti",
    "ax_webi",
    "ax_slaxi",
    "ax_routine",
    "prov_pgx",
    "prov_sshx",
    "prov_browserx",
)


def fail(message):
    raise AssertionError(message)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Stack:
    def __init__(self):
        self.root = Path(tempfile.mkdtemp(prefix="ax-live-"))
        self.home = self.root / "home"
        self.config = self.root / "config"
        self.bot = self.root / "bot"
        self.bin = self.root / "bin"
        for path in (self.home, self.config, self.bot, self.bin):
            path.mkdir(parents=True, exist_ok=True)
        self.processes = []
        self.cleanup = []

    def build(self):
        for program in ("ax",) + PROVIDERS + ("boti", "webi", "slaxi"):
            out = self.bin / program
            result = subprocess.run(
                ["go", "build", "-trimpath", "-o", str(out), "./cmd/" + program],
                cwd=ROOT,
                capture_output=True,
                text=True,
            )
            if result.returncode != 0:
                fail("build %s: %s" % (program, result.stderr[-400:]))

    def write_bot(self, extra=""):
        lines = ['model = "%s"' % MODEL, 'base_url = "%s"' % BASE_URL]
        for name in ("fsx", "bashx", "skillx", "memoryx"):
            lines += ['', '[[connection]]', 'id = "%s"' % name, 'kind = "local"', 'command = "%s"' % name]
        (self.bot / "bot.toml").write_text("\n".join(lines) + "\n" + extra)

    def env(self, **extra):
        e = {
            "HOME": str(self.home),
            "XDG_CONFIG_HOME": str(self.config),
            "PATH": str(self.bin) + os.pathsep + os.environ.get("PATH", ""),
            "BOT_ROOT": str(self.bot),
            "BOT_DATA": str(self.bot),
            "DEEPSEEK_API_KEY": os.environ["DEEPSEEK_API_KEY"],
            "LANG": "C.UTF-8",
        }
        e.update({k: str(v) for k, v in extra.items()})
        return e

    def spawn(self, args, env=None, cwd=None, stdin=None):
        proc = subprocess.Popen(
            args,
            env=env or self.env(),
            cwd=str(cwd or self.root),
            stdin=stdin,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=True,
        )
        self.processes.append(proc)
        return proc

    def stop_all(self):
        for hook in self.cleanup:
            try:
                hook()
            except Exception:
                pass
        for proc in self.processes:
            if proc is not None and proc.poll() is None:
                try:
                    os.killpg(proc.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
        for proc in self.processes:
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(proc.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
        shutil.rmtree(self.root, ignore_errors=True)


def run_ax(stack, prompt, args=(), timeout=240):
    cmd = [str(stack.bin / "ax"), "-C", str(stack.bot), "-model", MODEL, "-base", BASE_URL, *args, prompt]
    result = subprocess.run(cmd, env=stack.env(), capture_output=True, text=True, timeout=timeout)
    if result.returncode != 0:
        fail("ax exited %d: %s" % (result.returncode, result.stderr[-400:]))
    return result


def events_of(stdout):
    return [json.loads(line) for line in stdout.splitlines() if line.strip()]


def outcome(events):
    done = [e for e in events if e.get("type") == "done"]
    return done[-1].get("outcome") if done else None


def wait_http(url, timeout=30):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2) as response:
                if response.status == 200:
                    return
        except Exception:
            time.sleep(0.2)
    fail("timed out waiting for %s" % url)


def wait_tcp(port, timeout=20):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=1):
                return
        except OSError:
            time.sleep(0.2)
    fail("timed out waiting for port %d" % port)


def http_json(method, url, payload=None, timeout=120):
    data = json.dumps(payload).encode() if payload is not None else None
    request = urllib.request.Request(url, data=data, method=method, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=timeout) as response:
        body = response.read().decode()
    return json.loads(body) if body.strip() else {}


def check_ax_cli(stack):
    result = run_ax(stack, "Reply with the single word: pong")
    if "pong" not in result.stdout.lower():
        fail("ax_cli: no answer: %r" % result.stdout[:300])


def check_ax_events(stack):
    result = run_ax(stack, "Say hi and nothing else.", args=("--events",))
    events = events_of(result.stdout)
    if not events or events[0] != {"type": "protocol", "version": 1}:
        fail("ax_events: bad protocol event %s" % events[:1])
    if outcome(events) != "done":
        fail("ax_events: outcome %r" % outcome(events))
    if not any(e.get("type") == "usage" for e in events):
        fail("ax_events: no usage event")


def check_ax_tools(stack):
    (stack.bot / "workspace").mkdir(exist_ok=True)
    result = run_ax(
        stack,
        "Create evidence.txt in the workspace containing exactly LIVE-OK, then reply done.",
        args=("--events",),
    )
    events = events_of(result.stdout)
    if outcome(events) != "done":
        fail("ax_tools: outcome %r" % outcome(events))
    names = {e.get("name") for e in events if e.get("type") == "tool_start"}
    if not names:
        fail("ax_tools: the model ran no tool")
    evidence = stack.bot / "workspace" / "evidence.txt"
    if not evidence.is_file() or "LIVE-OK" not in evidence.read_text():
        fail("ax_tools: evidence.txt missing (tools=%s)" % sorted(n for n in names if n))


def mcp_initialize(proc):
    request = {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "smoke", "version": "1"}}}
    proc.stdin.write(json.dumps(request) + "\n")
    proc.stdin.flush()


def check_ax_mcp_stdio(stack):
    proc = stack.spawn([str(stack.bin / "ax"), "mcp", "serve", "--stdio"], cwd=stack.bot, stdin=subprocess.PIPE)
    request = {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "smoke", "version": "1"}}}
    proc.stdin.write(json.dumps(request) + "\n")
    proc.stdin.flush()
    line = proc.stdout.readline()
    if not line:
        fail("ax_mcp_stdio: no reply: %s" % proc.stderr.read()[-300:])
    reply = json.loads(line)
    if "result" not in reply:
        fail("ax_mcp_stdio: %r" % reply)
    tools = {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}
    proc.stdin.write(json.dumps(tools) + "\n")
    proc.stdin.flush()
    reply = json.loads(proc.stdout.readline())
    names = [t.get("name") for t in reply.get("result", {}).get("tools", [])]
    if not names:
        fail("ax_mcp_stdio: no tools")
    call = {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": names[0], "arguments": {}}}
    proc.stdin.write(json.dumps(call) + "\n")
    proc.stdin.flush()
    reply = json.loads(proc.stdout.readline())
    if "result" not in reply and "error" not in reply:
        fail("ax_mcp_stdio: call reply %r" % reply)


def mcp_session(url, calls):
    session_id = None
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    for payload in calls:
        data = json.dumps(payload).encode()
        h = dict(headers)
        if session_id:
            h["Mcp-Session-Id"] = session_id
        request = urllib.request.Request(url, data=data, method="POST", headers=h)
        try:
            with urllib.request.urlopen(request, timeout=60) as response:
                session_id = response.headers.get("Mcp-Session-Id") or session_id
                body = response.read().decode()
        except urllib.error.HTTPError as error:
            raise AssertionError("mcp http %d: %s" % (error.code, error.read()[:200]))
        if not body.strip():
            yield {}
            continue
        lines = [l[5:].strip() for l in body.splitlines() if l.startswith("data:")]
        raw = lines[-1] if lines else body
        reply = json.loads(raw)
        if "error" in reply:
            fail("mcp error: %s" % reply["error"])
        yield reply


def check_ax_mcp_http(stack):
    port = free_port()
    proc = stack.spawn([str(stack.bin / "ax"), "mcp", "serve", "--http", "127.0.0.1:%d" % port], cwd=stack.bot)
    url = "http://127.0.0.1:%d/mcp" % port
    wait_tcp(port, timeout=20)
    replies = list(
        mcp_session(
            url,
            [
                {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "smoke", "version": "1"}}},
                {"jsonrpc": "2.0", "method": "notifications/initialized"},
                {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}},
            ],
        )
    )
    names = [t.get("name") for t in replies[-1].get("result", {}).get("tools", [])]
    if not names:
        fail("ax_mcp_http: no tools (stderr=%s)" % (proc.stderr.read()[-200:] if proc.poll() is not None else ""))


def check_ax_boti(stack):
    repo = stack.root / "checkout"
    subprocess.run(["git", "clone", "--depth", "1", "-q", str(ROOT), str(repo)], check=True)
    port = free_port()
    serve = [
        "",
        "[boti.serve]",
        'command = ["webi", "-listen", "127.0.0.1:%d", "-open=false"]' % port,
        'health = "http://127.0.0.1:%d/healthz"' % port,
        'idle = "http://127.0.0.1:%d/readyz"' % port,
        'repo = "%s"' % repo,
    ]
    with (stack.bot / "bot.toml").open("a") as config:
        config.write("\n".join(serve) + "\n")
    stack.spawn([str(stack.bin / "boti"), "-C", str(stack.bot), "serve"], env=stack.env(BOTI_INSTALL_URL=""))
    wait_http("http://127.0.0.1:%d/healthz" % port, timeout=180)


def check_ax_webi(stack):
    port = free_port()
    token = "live-token-0123456789abcdef"
    stack.spawn(
        [str(stack.bin / "webi"), "-listen", "127.0.0.1:%d" % port, "-open=false", "-C", str(stack.bot)],
        env=stack.env(WEBI_TOKEN=token),
    )
    base = "http://127.0.0.1:%d" % port
    wait_http(base + "/healthz", timeout=30)
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor())
    opener.open(base + "/api/state?token=" + token, timeout=20).read()

    def call(method, path, payload=None):
        data = json.dumps(payload).encode() if payload is not None else None
        request = urllib.request.Request(base + path, data=data, method=method, headers={"Content-Type": "application/json"})
        with opener.open(request, timeout=120) as response:
            text = response.read().decode()
        return json.loads(text) if text.strip() else {}

    state = call("GET", "/api/state")
    if "channels" not in state:
        fail("ax_webi: unexpected state keys %s" % sorted(state)[:8])
    channels = state.get("channels") or []
    if channels:
        channel_id = channels[0].get("id")
    else:
        channel = call("POST", "/api/channels", {"name": "live"})
        channel_id = channel.get("id") or channel.get("channel", {}).get("id")
    if not channel_id:
        fail("ax_webi: no channel id")
    thread = call("POST", "/api/channels/%s/threads" % channel_id, {"title": "live"})
    thread_id = thread.get("id") or thread.get("thread", {}).get("id")
    if not thread_id:
        fail("ax_webi: no thread id: %r" % thread)
    call("POST", "/api/threads/%s/messages" % thread_id, {"text": "Reply with the single word: pong"})
    deadline = time.time() + 180
    seen = ""
    while time.time() < deadline:
        seen = json.dumps(call("GET", "/api/threads/%s" % thread_id)).lower()
        if "pong" in seen:
            return
        time.sleep(2)
    fail("ax_webi: no answer in the thread (%s)" % seen[:200])


def check_ax_slaxi(stack):
    doctor = subprocess.run(
        [str(stack.bin / "slaxi"), "doctor"], env=stack.env(SLACK_APP_TOKEN="x", SLACK_BOT_TOKEN="y"), capture_output=True, text=True, timeout=60
    )
    if doctor.returncode not in (0, 1):
        fail("ax_slaxi: doctor exited %d: %s" % (doctor.returncode, doctor.stdout[-300:]))
    if "bot root" not in doctor.stdout:
        fail("ax_slaxi: doctor printed no report: %s" % doctor.stdout[-300:])


def download_deb(name, dest):
    subprocess.run(["apt-get", "download", name], cwd=str(dest), check=True, capture_output=True)
    package = next(dest.glob(name + "_*.deb"))
    subprocess.run(["dpkg-deb", "-x", str(package), str(dest / "root")], check=True)
    package.unlink()


def start_postgres(stack):
    dest = stack.root / "pg"
    dest.mkdir(parents=True, exist_ok=True)
    for name in ("postgresql-17", "libpq5", "postgresql-common"):
        download_deb(name, dest)
    bindir = dest / "root" / "usr" / "lib" / "postgresql" / "17" / "bin"
    if not (bindir / "postgres").is_file():
        fail("postgres: no server binary after unpacking")
    libdir = dest / "root" / "usr" / "lib" / "x86_64-linux-gnu"
    env = stack.env(LD_LIBRARY_PATH=libdir)
    subprocess.run([str(bindir / "initdb"), "-D", str(dest / "data"), "-U", "postgres", "--auth=trust"], env=env, check=True, capture_output=True)
    port = free_port()
    subprocess.run(
        [str(bindir / "pg_ctl"), "-D", str(dest / "data"), "-l", str(dest / "pg.log"), "-o", "-p %d -c listen_addresses=127.0.0.1 -c unix_socket_directories=%s" % (port, dest), "start"],
        env=env,
        check=True,
        capture_output=True,
    )
    stack.cleanup.append(lambda: subprocess.run([str(bindir / "pg_ctl"), "-D", str(dest / "data"), "stop", "-m", "immediate"], env=env, capture_output=True))
    wait_tcp(port, timeout=30)
    return "postgres://postgres@127.0.0.1:%d/postgres?sslmode=disable" % port


def start_sshd(stack):
    dest = stack.root / "sshd"
    dest.mkdir(parents=True, exist_ok=True)
    subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(dest / "host_key")], check=True)
    subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(dest / "user_key")], check=True)
    (dest / "authorized_keys").write_bytes((dest / "user_key.pub").read_bytes())
    (dest / "authorized_keys").chmod(0o600)
    download_deb("openssh-server", dest)
    download_deb("openssh-sftp-server", dest)
    sshd = dest / "root" / "usr" / "sbin" / "sshd"
    sftp = dest / "root" / "usr" / "lib" / "openssh" / "sftp-server"
    if not sshd.is_file() or not sftp.is_file():
        fail("sshd: binaries missing after unpacking")
    port = free_port()
    remote = dest / "remote"
    remote.mkdir(exist_ok=True)
    (dest / "sshd_config").write_text(
        "\n".join(
            [
                "Port %d" % port,
                "ListenAddress 127.0.0.1",
                "HostKey %s" % (dest / "host_key"),
                "AuthorizedKeysFile %s" % (dest / "authorized_keys"),
                "PasswordAuthentication no",
                "PubkeyAuthentication yes",
                "StrictModes no",
                "UsePAM no",
                "PidFile %s" % (dest / "sshd.pid"),
                "AuthorizedKeysCommand none",
                "Subsystem sftp %s" % sftp,
            ]
        )
        + "\n"
    )
    subprocess.run([str(sshd), "-f", str(dest / "sshd_config"), "-E", str(dest / "sshd.log")], check=True)
    stack.cleanup.append(lambda: subprocess.run(["pkill", "-f", str(dest / "sshd_config")], capture_output=True))
    wait_tcp(port, timeout=30)
    known = subprocess.run(["ssh-keyscan", "-p", str(port), "127.0.0.1"], capture_output=True, text=True, check=True).stdout
    hosts = dest / "known_hosts"
    hosts.write_text(known)
    return {
        "SSHX_HOST": "127.0.0.1:%d" % port,
        "USER": os.environ.get("USER") or "root",
        "SSHX_KEY_FILE": str(dest / "user_key"),
        "SSHX_KNOWN_HOSTS": str(hosts),
        "SSHX_ROOT": str(remote),
    }


def start_chromium(stack):
    chromium = shutil.which("chromium") or shutil.which("chromium-browser") or shutil.which("google-chrome")
    if not chromium:
        fail("browserx: no chromium on PATH")
    dest = stack.root / "chrome"
    dest.mkdir(parents=True, exist_ok=True)
    port = free_port()
    log = (dest / "chrome.log").open("wb")
    proc = subprocess.Popen(
        [
            chromium,
            "--headless=new",
            "--no-sandbox",
            "--disable-gpu",
            "--disable-dev-shm-usage",
            "--remote-debugging-port=%d" % port,
            "--user-data-dir=%s" % (dest / "profile"),
            "about:blank",
        ],
        stdout=log,
        stderr=log,
        stdin=subprocess.DEVNULL,
        start_new_session=True,
    )
    stack.processes.append(proc)
    wait_tcp(port, timeout=30)
    return "http://127.0.0.1:%d" % port


def check_prov_pgx(stack):
    url = start_postgres(stack)
    result = subprocess.run(
        [str(stack.bin / "pgx"), "run", "postgres_query"],
        input=json.dumps({"sql": "select 1 as one, 2 as two"}),
        env=stack.env(DATABASE_URL=url),
        capture_output=True,
        text=True,
        timeout=120,
    )
    if result.returncode != 0:
        fail("prov_pgx: exit %d: %s" % (result.returncode, result.stderr[-300:]))
    rows = json.loads(result.stdout).get("rows") or []
    if not rows or rows[0].get("one") != 1:
        fail("prov_pgx: unexpected rows %r" % rows)
    write = subprocess.run(
        [str(stack.bin / "pgx"), "run", "postgres_query"],
        input=json.dumps({"sql": "insert into t values (1)"}),
        env=stack.env(DATABASE_URL=url),
        capture_output=True,
        text=True,
        timeout=120,
    )
    if write.returncode != 2:
        fail("prov_pgx: a write exited %d, want 2" % write.returncode)


def check_prov_sshx(stack):
    env = start_sshd(stack)
    remote = stack.bin.parent / ("remote-%d" % os.getpid())
    payloads = [
        ("bash", {"command": "echo SSH-REMOTE-OK"}, "SSH-REMOTE-OK"),
        ("write", {"path": "probe.txt", "content": "REMOTE-WRITE"}, "wrote"),
        ("read", {"path": "probe.txt"}, "REMOTE-WRITE"),
    ]
    for tool, payload, want in payloads:
        result = subprocess.run(
            [str(stack.bin / "sshx"), "run", tool],
            input=json.dumps(payload),
            env=stack.env(**env),
            capture_output=True,
            text=True,
            timeout=120,
        )
        if result.returncode != 0:
            fail("prov_sshx %s: exit %d: %s" % (tool, result.returncode, result.stderr[-300:]))
        if want not in result.stdout:
            fail("prov_sshx %s: output %r lacks %r" % (tool, result.stdout[:120], want))
    del remote


def check_prov_browserx(stack):
    cdp = start_chromium(stack)
    artifacts = stack.root / "shots"
    artifacts.mkdir(exist_ok=True)
    env = stack.env(BROWSERX_CDP_URL=cdp, BROWSERX_ARTIFACT_DIR=artifacts)
    target = os.environ.get("AX_LIVE_BROWSER_URL") or "https://example.com"
    steps = [
        ("browser_open", {"url": target}, "Opened"),
        ("browser_read", {}, "URL:"),
        ("browser_links", {}, "http"),
        ("browser_screenshot", {"name": "smoke"}, "smoke.png"),
    ]
    for tool, payload, want in steps:
        result = subprocess.run(
            [str(stack.bin / "browserx"), "run", tool],
            input=json.dumps(payload),
            env=env,
            capture_output=True,
            text=True,
            timeout=180,
        )
        if result.returncode != 0:
            fail("prov_browserx %s: exit %d: %s" % (tool, result.returncode, result.stderr[-300:]))
        if want not in result.stdout:
            fail("prov_browserx %s: output %r lacks %r" % (tool, result.stdout[:120], want))
    shot = artifacts / "smoke.png"
    if not shot.is_file() or shot.stat().st_size < 1000:
        fail("prov_browserx: screenshot too small")


def check_ax_routine(stack):
    routine = stack.bot / 'routines' / 'live'
    routine.mkdir(parents=True, exist_ok=True)
    manifest = 'description = ''"''live''"''\nentry = ''"''./run.sh''"''\n'
    (routine / 'routine.toml').write_text(manifest)
    script = routine / 'run.sh'
    script.write_text('#!/bin/sh\necho ran > ''"''$ROUTINE_STATE/out.txt''"''\n')
    script.chmod(0o755)
    result = subprocess.run(
        [str(stack.bin / 'ax'), '-C', str(stack.bot), 'routine', 'run', 'live'],
        env=stack.env(),
        capture_output=True,
        text=True,
        timeout=120,
    )
    if result.returncode != 0:
        fail('ax_routine: exit %d: %s' % (result.returncode, result.stderr[-300:]))
    state = stack.bot / 'state' / 'routines' / 'live'
    if not any(state.rglob('out.txt')):
        fail('ax_routine: the run wrote nothing under %s' % state)


def main():
    if not os.environ.get("DEEPSEEK_API_KEY"):
        print("live.py: DEEPSEEK_API_KEY is required", file=sys.stderr)
        return 2
    only = sys.argv[1:]
    selected = [name for name in LAYERS if not only or name in only]
    stack = Stack()
    failures = 0
    try:
        stack.build()
        stack.write_bot()
        for name in selected:
            started = time.time()
            try:
                globals()["check_" + name](stack)
                print("PASS %-16s %.1fs" % (name, time.time() - started), flush=True)
            except Exception as error:
                failures += 1
                print("FAIL %-16s %s" % (name, error), flush=True)
    finally:
        stack.stop_all()
    if failures:
        print("\n%d/%d live checks failed" % (failures, len(selected)))
        return 1
    print("\nall %d live checks passed" % len(selected))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
