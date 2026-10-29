#!/usr/bin/env python3
import json
import os
import sys
import urllib.error
import urllib.request

BASE = os.environ.get("AX_CANARY_URL") or "http://127.0.0.1:8377"
TOKEN = os.environ.get("AX_CANARY_TOKEN") or ""
TIMEOUT = int(os.environ.get("AX_CANARY_TIMEOUT") or "240")
PROMPT = os.environ.get("AX_CANARY_PROMPT") or "Reply with the single word: pong"


def call(opener, method, path, payload=None):
    data = json.dumps(payload).encode() if payload is not None else None
    request = urllib.request.Request(
        BASE + path, data=data, method=method, headers={"Content-Type": "application/json"}
    )
    with opener.open(request, timeout=60) as response:
        text = response.read().decode()
    return json.loads(text) if text.strip() else {}


def main():
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor())
    try:
        opener.open(BASE + "/healthz", timeout=10).read()
    except Exception as error:
        print("canary: %s is not healthy: %s" % (BASE, error), file=sys.stderr)
        return 1
    if TOKEN:
        opener.open(BASE + "/api/state?token=" + TOKEN, timeout=20).read()
    state = call(opener, "GET", "/api/state")
    channels = state.get("channels") or []
    if not channels:
        print("canary: the instance has no channels", file=sys.stderr)
        return 1
    channel_id = channels[0].get("id")
    thread = call(opener, "POST", "/api/channels/%s/threads" % channel_id, {"title": "canary"})
    thread_id = thread.get("id") or thread.get("thread", {}).get("id")
    if not thread_id:
        print("canary: could not open a thread: %r" % thread, file=sys.stderr)
        return 1
    call(opener, "POST", "/api/threads/%s/messages" % thread_id, {"text": PROMPT})
    import time
    deadline = time.time() + TIMEOUT
    seen = ""
    while time.time() < deadline:
        seen = json.dumps(call(opener, "GET", "/api/threads/%s" % thread_id)).lower()
        if "pong" in seen:
            print("canary: answered in %.1fs" % (TIMEOUT - (deadline - time.time())))
            return 0
        time.sleep(2)
    print("canary: no answer within %ds (%s)" % (TIMEOUT, seen[:200]), file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
