#!/usr/bin/env python3
import json
import subprocess
import sys


def run(provider, arguments, data=b""):
    return subprocess.run(
        [provider, *arguments],
        input=data,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=10,
        check=False,
    )


def fail(provider, message):
    print(f"{provider}: {message}", file=sys.stderr)
    return False


def check(provider):
    valid = True
    result = run(provider, ["describe"])
    if result.returncode != 0:
        return fail(provider, f"describe exited with {result.returncode}")
    if len(result.stdout) > 65536:
        valid = fail(provider, "describe output exceeds 65536 bytes")

    names = set()
    for number, raw_line in enumerate(result.stdout.splitlines(), 1):
        if not raw_line.strip():
            continue
        try:
            descriptor = json.loads(raw_line)
        except Exception as error:
            valid = fail(provider, f"descriptor {number} is invalid JSON: {error}")
            continue
        if not isinstance(descriptor, dict):
            valid = fail(provider, f"descriptor {number} is not an object")
            continue
        name = descriptor.get("name")
        description = descriptor.get("description")
        parameters = descriptor.get("parameters")
        if not isinstance(name, str) or not name:
            valid = fail(provider, f"descriptor {number} has no name")
            continue
        if name in names:
            valid = fail(provider, f"duplicate tool name: {name}")
        names.add(name)
        if not isinstance(description, str) or not description.strip():
            valid = fail(provider, f"{name} has no description")
        if not isinstance(parameters, dict):
            valid = fail(provider, f"{name} parameters are not an object")

    for arguments in ([], ["invalid"], ["ax-tools"], ["ax-run"]):
        result = run(provider, arguments)
        if result.returncode != 2:
            valid = fail(
                provider,
                f"{' '.join(arguments) or 'no arguments'} exited with {result.returncode}, want 2",
            )

    result = run(provider, ["run", "ax-contract-missing"], b"{}")
    if result.returncode != 2:
        valid = fail(provider, f"unknown tool exited with {result.returncode}, want 2")

    for name in names:
        result = run(provider, ["run", name], b"{")
        if result.returncode != 2:
            valid = fail(provider, f"malformed JSON for {name} exited with {result.returncode}, want 2")

    if valid:
        print(f"{provider}: ok ({len(names)} tools)")
    return valid


def main():
    if len(sys.argv) < 2:
        print("usage: provider-contract.py PROVIDER...", file=sys.stderr)
        return 2
    valid = True
    for provider in sys.argv[1:]:
        if not check(provider):
            valid = False
    return 0 if valid else 1


if __name__ == "__main__":
    raise SystemExit(main())
