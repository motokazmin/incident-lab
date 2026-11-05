#!/usr/bin/env bash
# Собирает агент, базовый образ и диски сюжетов в .data/images.
# Использование: build-images.sh [id ...], без аргументов — все сюжеты.
# Диск пересобирается, только если образ изменился.
set -euo pipefail
cd "$(dirname "$0")/.."
ctx=$(mktemp -d); trap 'rm -rf "$ctx"' EXIT
# Без отметок о коммите и путях: иначе каждый коммит менял бы агент, а с ним все образы.
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o "$ctx/labagent" ./platform/cmd/labagent
cp platform/guest/lab-init platform/guest/base.Dockerfile "$ctx/"
docker build -q -t lab-base -f "$ctx/base.Dockerfile" "$ctx" >/dev/null

[ $# -gt 0 ] || set -- $(cd scenarios && ls -d */ | tr -d /)
for id in "$@"; do
  dir=scenarios/$id
  [ -f "$dir/image/Dockerfile" ] || { echo "нет сюжета $id" >&2; exit 1; }
  [ -d "$dir/service/vendor" ] || (cd "$dir/service" && go mod tidy && go mod vendor)
  docker build -q -t "lab-$id" -f "$dir/image/Dockerfile" "$dir" >/dev/null
  disk=.data/images/$id.ext4
  built=$(docker image inspect -f '{{.Id}}' "lab-$id")
  if [ -f "$disk" ] && [ "$(cat "$disk.image" 2>/dev/null)" = "$built" ]; then
    echo "$id: образ не менялся"
    continue
  fi
  platform/guest/build-rootfs.sh "lab-$id" "$disk" 2G
  echo "$built" > "$disk.image"
done
