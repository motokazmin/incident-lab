#!/usr/bin/env bash
# Запускает полигон локально: готовит недостающее и стартует сервер на http://127.0.0.1:8090.
# Использование: run.sh [--build]. С --build пересобирает образы и веб, даже если они уже есть.
# Прежний сервер этого проекта останавливается вместе со всеми стендами.
# Остановка: Ctrl+C, виртуалки стендов умирают вместе с сервером.
set -euo pipefail
cd "$(dirname "$0")/.."

build=
case "${1:-}" in
  "") ;;
  --build) build=1 ;;
  *) echo "использование: $0 [--build]" >&2; exit 2 ;;
esac

if [ ! -r /dev/kvm ] || [ ! -w /dev/kvm ]; then
  echo "нет доступа к /dev/kvm: проверь виртуализацию и членство в группе kvm" >&2
  exit 1
fi

# Прежний сервер останавливаем: второй очистил бы .data/run и удалил диски стендов первого.
# Его виртуалки умирают вместе с ним. Чужой процесс на порту не трогаем.
if ss -ltnH 'sport = :8090' | grep -q .; then
  pid=$(ss -ltnpH 'sport = :8090' | grep -o 'pid=[0-9]*' | head -n 1 | cut -d= -f2 || true)
  if [ -z "$pid" ] || [ "$(cat "/proc/$pid/comm" 2>/dev/null)" != server ] ||
     [ "$(readlink "/proc/$pid/cwd" 2>/dev/null)" != "$(pwd -P)" ]; then
    echo "порт 8090 занят другим процессом${pid:+ (pid $pid)}, останови его сам" >&2
    exit 1
  fi
  echo "останавливаю прежний сервер, pid $pid: его стенды будут уничтожены"
  kill "$pid"
  for _ in $(seq 1 50); do
    ss -ltnH 'sport = :8090' | grep -q . || break
    sleep 0.1
  done
  if ss -ltnH 'sport = :8090' | grep -q .; then
    echo "прежний сервер не остановился за 5 секунд, pid $pid" >&2
    exit 1
  fi
fi

[ -x .data/fc/firecracker ] && [ -f .data/fc/vmlinux ] || scripts/setup-firecracker.sh

missing=
for dir in scenarios/*/; do
  [ -f ".data/images/$(basename "$dir").ext4" ] || missing=1
done
[ -z "$build$missing" ] || scripts/build-images.sh

if [ -n "$build" ] || [ ! -f web/dist/index.html ]; then
  (cd web && npm ci && npm run build)
fi

exec go run ./platform/cmd/server
