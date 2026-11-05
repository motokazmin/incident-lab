#!/usr/bin/env python3
"""PostToolUse: проверка файла сразу после правки. Код 2 — ошибка уходит Claude на исправление."""
import json, os, shutil, subprocess, sys

ev = json.load(sys.stdin)
path = (ev.get("tool_input") or {}).get("file_path", "")
root = os.environ.get("CLAUDE_PROJECT_DIR", ".")
if not path or not os.path.exists(path):
    sys.exit(0)
d = os.path.dirname(path)

def tsc():
    cfg = os.path.join(root, "tsconfig.json")
    return ["npx", "--no-install", "tsc", "--noEmit", "-p", cfg] if os.path.exists(cfg) else None

def py():
    cmds = [[sys.executable, "-m", "py_compile", path]]
    if shutil.which("ruff"):
        cmds.append(["ruff", "check", "--quiet", path])
    return cmds

CHECKS = {  # расширение → команды; подправить под проект
    ".go": lambda: [["go", "vet", "."]],
    ".py": py,
    ".ts": lambda: [tsc()],
    ".tsx": lambda: [tsc()],
}
ext = os.path.splitext(path)[1]
cwd = d if ext == ".go" else root
for cmd in (CHECKS.get(ext, lambda: [])() or []):
    if not cmd:
        continue
    try:
        r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=120)
    except (FileNotFoundError, subprocess.TimeoutExpired):
        continue
    if r.returncode != 0:
        print(f"{' '.join(cmd)} после правки {path}:\n{(r.stdout + r.stderr)[-3000:]}", file=sys.stderr)
        sys.exit(2)
