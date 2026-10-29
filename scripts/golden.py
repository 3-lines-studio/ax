#!/usr/bin/env python3
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
GOLDEN = ROOT / "scripts" / "golden"
BASE_URL = os.environ.get("AX_BASE_URL") or "https://api.deepseek.com"
MODEL = os.environ.get("AX_MODEL") or "deepseek-flash"
PROMPT = os.environ.get("GOLDEN_PROMPT") or (
    "In the workspace create notes.txt with exactly GOLDEN-OK, then reply with the word stored."
)
EVENT_KINDS = (
    "protocol",
    "message",
    "tool_start",
    "tool_end",
    "assistant_delta",
    "reasoning_delta",
    "usage",
    "result",
    "error",
    "done",
)


NOISE = {
    "reasoning_delta",
    "assistant_delta",
    "usage",
    "assistant_done",
    "message",
    "tool_done",
    "tool_result",
}


def spine(kinds):
    out = []
    for kind in kinds:
        if kind in NOISE:
            continue
        if out and kind == "tool_start" and out[-1] == "tool_start":
            continue
        out.append(kind)
    return out


def record(stack):
    bot = stack / "bot"
    workspace = bot / "workspace"
    workspace.mkdir(parents=True, exist_ok=True)
    lines = ['model = "%s"' % MODEL, 'base_url = "%s"' % BASE_URL]
    for name in ("fsx", "bashx"):
        lines += ['', '[[connection]]', 'id = "%s"' % name, 'kind = "local"', 'command = "%s"' % name]
    (bot / "bot.toml").write_text("\n".join(lines) + "\n")
    env = {
        "HOME": str(stack / "home"),
        "XDG_CONFIG_HOME": str(stack / "config"),
        "BOT_ROOT": str(bot),
        "BOT_DATA": str(bot),
        "DEEPSEEK_API_KEY": os.environ["DEEPSEEK_API_KEY"],
        "LANG": "C.UTF-8",
        "PATH": os.environ.get("PATH", ""),
    }
    result = subprocess.run(
        [sys.argv[1] if len(sys.argv) > 1 else "ax", "-C", str(bot), "--events", PROMPT],
        env=env,
        capture_output=True,
        text=True,
        timeout=300,
    )
    events = [json.loads(line) for line in result.stdout.splitlines() if line.strip()]
    kinds = spine([e.get("type") for e in events])
    tools = sorted({e.get("name") for e in events if e.get("type") == "tool_start" and e.get("name") != "search"})
    outcome = [e.get("outcome") for e in events if e.get("type") == "done"]
    return {
        "model": MODEL,
        "kinds": kinds,
        "tools": tools,
        "outcome": outcome[-1] if outcome else None,
        "created_file": (workspace / "notes.txt").is_file(),
        "exit": result.returncode,
    }


def compare(expected, actual):
    problems = []
    for key in ("kinds", "tools", "outcome", "created_file", "exit"):
        if expected.get(key) != actual.get(key):
            problems.append("%s: got %r want %r" % (key, actual.get(key), expected.get(key)))
    return problems


def main():
    golden_file = GOLDEN / "oneshot.json"
    stack = Path(tempfile.mkdtemp(prefix="ax-golden-"))
    try:
        actual = record(stack)
    finally:
        shutil.rmtree(stack, ignore_errors=True)
    if actual["outcome"] != "done":
        print("golden: the run did not finish: %r" % actual, file=sys.stderr)
        return 1
    if "--update" in sys.argv[1:] or not golden_file.is_file():
        GOLDEN.mkdir(parents=True, exist_ok=True)
        golden_file.write_text(json.dumps(actual, indent=2) + "\n")
        print("golden: %s written" % golden_file.relative_to(ROOT))
        return 0
    expected = json.loads(golden_file.read_text())
    problems = compare(expected, actual)
    if problems:
        for problem in problems:
            print("golden: " + problem, file=sys.stderr)
        return 1
    print("golden: shape matches (kinds=%d, tools=%s)" % (len(actual["kinds"]), actual["tools"]))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
