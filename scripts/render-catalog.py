import json, sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CATALOG = ROOT / "catalog.json"
README = ROOT / "README.md"
BEGIN = "<!-- programs:begin -->"
END = "<!-- programs:end -->"

KIND_ORDER = ["engine", "host", "transport", "provider", "bridge"]
KIND_TITLE = {
    "engine": "Engine",
    "host": "Bot host",
    "transport": "Transports",
    "provider": "Tool providers",
    "bridge": "Protocol bridges",
}


def render(programs):
    groups = {}
    for program in programs:
        groups.setdefault(program["kind"], []).append(program)
    lines = []
    for kind in KIND_ORDER:
        entries = groups.get(kind, [])
        if not entries:
            continue
        lines.append(f"### {KIND_TITLE[kind]}")
        lines.append("")
        lines.append("| Program | Description |")
        lines.append("| --- | --- |")
        for program in entries:
            lines.append(f"| `{program['name']}` | {program['description']} |")
        lines.append("")
    return "\n".join(lines).rstrip() + "\n"


def main():
    data = json.loads(CATALOG.read_text())
    programs = data["programs"]
    errors = []
    names = set()
    for program in programs:
        name = program["name"]
        names.add(name)
        if not (ROOT / "cmd" / name).is_dir():
            errors.append(f"{name}: missing cmd/{name}")
    for cmd in sorted((ROOT / "cmd").iterdir()):
        if cmd.is_dir() and cmd.name not in names:
            errors.append(f"{cmd.name}: cmd/{cmd.name} is not in catalog.json")

    readme = README.read_text()
    if BEGIN not in readme or END not in readme:
        errors.append(f"README.md is missing {BEGIN} / {END}")
    if errors:
        for error in errors:
            print(f"catalog: {error}", file=sys.stderr)
        return 1

    head, rest = readme.split(BEGIN, 1)
    _, tail = rest.split(END, 1)
    rendered = f"{BEGIN}\n\n{render(programs)}\n{END}"
    updated = head + rendered + tail

    if "--check" in sys.argv[1:]:
        if updated != readme:
            print("catalog: README.md is out of date; run scripts/render-catalog.py", file=sys.stderr)
            return 1
        return 0

    if updated != readme:
        README.write_text(updated)
        print("catalog: README.md updated")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
