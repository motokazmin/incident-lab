#!/usr/bin/env bash
# Превращает docker-образ в ext4-диск. Использование: build-rootfs.sh <образ> <файл.ext4> [размер]
# mkfs идёт в контейнере от root, иначе владельцы файлов в образе потеряются.
set -euo pipefail
image=$1; out=$(realpath -m "$2"); size=${3:-2G}
mkdir -p "$(dirname "$out")"
cid=$(docker create "$image" /bin/true)
trap 'docker rm -f "$cid" >/dev/null' EXIT
docker export "$cid" | docker run -i --rm -v "$(dirname "$out"):/out" alpine:3.22 sh -euc "
  apk add --no-cache -q e2fsprogs tar >/dev/null
  mkdir /r && tar -xpf - -C /r --numeric-owner
  mkfs.ext4 -q -F -L labroot -d /r /out/$(basename "$out").tmp $size
  chown $(id -u):$(id -g) /out/$(basename "$out").tmp
  mv /out/$(basename "$out").tmp /out/$(basename "$out")
"
ls -la "$out"
