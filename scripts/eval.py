#!/usr/bin/env python3
"""Real-provider, botdir-centric end-to-end eval for ax.

Runs the real `ax` binary against a real OpenAI-compatible endpoint through a
real Botdir bot root (bot.toml, bot.md, skills/, secrets/, workspace/) and the
real tool providers built from this ecosystem. Asserts on observable artifacts,
never on model wording. Skips cleanly (exit 0, prints SKIP) when the provider is
not configured, so CI stays green without credentials.

Configuration (env):
  OPENAI_API_KEY    provider API key (required to run)
  AX_BASE_URL       OpenAI-compatible base URL, e.g. https://api.deepseek.com/v1
  AX_MODEL          model name, e.g. deepseek-v4-flash
"""
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent          # ax repo root
ECOSYSTEM = ROOT.parent                                  # ax-ecosystem root
TOOLS = ("ax", "fsx", "bashx", "skillx")


def check(cond, msg):
    if not cond:
        raise AssertionError(msg)


def build_tools(dest):
    dest.mkdir(parents=True, exist_ok=True)
    for tool in TOOLS:
        out = dest / tool
        print("building", tool)
        subprocess.run(
            ["go", "build", "-trimpath", "-o", str(out), "./..."],
            cwd=ECOSYSTEM / tool,
            check=True,
        )


class Bot:
    def __init__(self, root):
        self.root = Path(root)
        for d in ("workspace", "skills", "secrets"):
            (self.root / d).mkdir(parents=True, exist_ok=True)

    def write(self, rel, text):
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def run(self, prompt, bin_dir, session=None, extra_env=None):
        env = os.environ.copy()
        env["OPENAI_API_KEY"] = os.environ["OPENAI_API_KEY"]
        env["BOT_ROOT"] = str(self.root)
        env["AX_TOOLS"] = "fsx bashx skillx"
        env["PATH"] = str(bin_dir) + os.pathsep + env["PATH"]
        for k, v in (extra_env or {}).items():
            env[k] = v
        cmd = [str(bin_dir / "ax"), "-C", str(self.root), "--events"]
        if session:
            cmd += ["--session", session]
        cmd += [prompt]
        for _ in range(3):
            p = subprocess.run(cmd, capture_output=True, text=True, env=env, timeout=120)
            if p.returncode == 0:
                break
        else:
            raise AssertionError("ax did not exit 0: %s" % p.stderr[-500:])
        return [json.loads(l) for l in p.stdout.splitlines() if l.strip()]


def assistant_text(events):
    out = []
    for e in events:
        if e.get("type") == "assistant_delta":
            out.append(e.get("text", ""))
        m = e.get("message")
        if e.get("type") == "message" and isinstance(m, dict) and m.get("role") == "assistant":
            out.append(m.get("content", ""))
    return "".join(out)


def tool_names(events):
    return [e.get("name") for e in events if e.get("type") == "tool_start"]


def outcome(events):
    d = [e for e in events if e.get("type") == "done"]
    return d[-1]["outcome"] if d else "no-done"


SCENARIOS = []


def scenario(fn):
    SCENARIOS.append(fn)
    return fn


@scenario
def read_file(bot, bin_dir):
    bot.write("workspace/data.txt", "alpha beta gamma delta")
    events = bot.run("Read workspace/data.txt with the fsx read tool and reply with its first word.", bin_dir)
    check(outcome(events) == "done", "outcome=%s" % outcome(events))
    check("alpha" in assistant_text(events).lower(), "answer missing 'alpha': %r" % assistant_text(events)[-200:])


@scenario
def write_file(bot, bin_dir):
    events = bot.run("Use the fsx write tool to create workspace/hello.txt containing exactly the word hello.", bin_dir)
    check(outcome(events) == "done", "outcome=%s" % outcome(events))
    check((bot.root / "workspace/hello.txt").read_text().strip() == "hello", "hello.txt wrong")


@scenario
def bash_echo(bot, bin_dir):
    events = bot.run("Use the bash tool to run: printf world > workspace/world.txt", bin_dir)
    check(outcome(events) == "done", "outcome=%s" % outcome(events))
    check((bot.root / "workspace/world.txt").read_text().strip() == "world", "world.txt wrong")


@scenario
def skill_use(bot, bin_dir):
    bot.write("skills/hello/SKILL.md", "---\ndescription: greet marker\n---\nUsing the fsx write tool, create workspace/skill-marker.txt containing the word marker.")
    events = bot.run("Use the hello skill.", bin_dir)
    check(outcome(events) == "done", "outcome=%s" % outcome(events))
    check((bot.root / "workspace/skill-marker.txt").read_text().strip() == "marker", "skill not followed")


@scenario
def session_persist(bot, bin_dir):
    sess = str(bot.root / "thread.jsonl")
    bot.run("Remember that the secret word is zephyr. Do not do anything else.", bin_dir, session=sess)
    events = bot.run("What is the secret word I asked you to remember?", bin_dir, session=sess)
    check(outcome(events) == "done", "outcome=%s" % outcome(events))
    check("zephyr" in assistant_text(events).lower(), "session context lost: %r" % assistant_text(events)[-200:])


def load_dotenv(path):
    if not path.exists():
        return
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        k = k.strip().lstrip("export ").strip()
        v = v.strip().strip('"').strip("'")
        if k:
            os.environ.setdefault(k, v)


def main(argv):
    load_dotenv(ROOT / ".env")
    if not os.environ.get("OPENAI_API_KEY"):
        print("SKIP: OPENAI_API_KEY not set — real-provider eval not run")
        return 0
    base_url = os.environ.get("AX_BASE_URL")
    model = os.environ.get("AX_MODEL")
    if not base_url or not model:
        print("SKIP: set AX_BASE_URL and AX_MODEL for the real-provider eval")
        return 0
    if len(argv) > 1:
        print("usage: python3 scripts/eval.py")
        return 1
    with tempfile.TemporaryDirectory(prefix="ax-eval-") as td:
        td = Path(td)
        bin_dir = td / "bin"
        build_tools(bin_dir)
        results = []
        for fn in SCENARIOS:
            name = fn.__name__
            with tempfile.TemporaryDirectory(prefix="bot-") as btd:
                bot = Bot(btd)
                try:
                    fn(bot, bin_dir)
                    results.append((name, True, ""))
                    print("PASS  %s" % name)
                except AssertionError as err:
                    results.append((name, False, str(err)))
                    print("FAIL  %s — %s" % (name, err))
        failed = [r for r in results if not r[1]]
        print("\n%d/%d passed" % (len(results) - len(failed), len(results)))
        return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
