#!/usr/bin/env python3
"""PreToolUse: запреты проекта из .claude/guard.txt.
Строки: `bash: <regex>  # причина` или `path: <regex>  # причина`. Код 2 — отказ, причина уходит Claude."""
import json, os, re, sys

ev = json.load(sys.stdin)
inp = ev.get("tool_input") or {}
root = os.environ.get("CLAUDE_PROJECT_DIR", ".")
rules = os.path.join(root, ".claude", "guard.txt")
if not os.path.exists(rules):
    sys.exit(0)
target = {"bash": inp.get("command", ""), "path": inp.get("file_path", "")}
for line in open(rules, encoding="utf-8"):
    line = line.strip()
    if not line or line.startswith("#") or ":" not in line:
        continue
    kind, rest = line.split(":", 1)
    pattern, _, why = rest.partition("  #")
    kind, pattern = kind.strip(), pattern.strip()
    if kind in target and target[kind] and re.search(pattern, target[kind]):
        print(f"Запрещено правилом проекта ({kind}: {pattern}): {why.strip() or 'см. .claude/guard.txt'}. "
              "Нужно всё-таки сделать — спроси пользователя.", file=sys.stderr)
        sys.exit(2)
