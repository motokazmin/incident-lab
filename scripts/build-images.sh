#!/usr/bin/env bash
# Собирает агент, базовый образ и диски сценариев в .data/images.
set -euo pipefail
cd "$(dirname "$0")/.."
ctx=$(mktemp -d); trap 'rm -rf "$ctx"' EXIT
CGO_ENABLED=0 go build -o "$ctx/labagent" ./platform/cmd/labagent
cp platform/guest/lab-init platform/guest/base.Dockerfile "$ctx/"
docker build -q -t lab-base -f "$ctx/base.Dockerfile" "$ctx" >/dev/null
(cd scenarios/poolexhausted/service && [ -d vendor ] || (cd scenarios/poolexhausted/service && go mod tidy && go mod vendor))
docker build -q -t lab-pool-exhausted -f scenarios/poolexhausted/image/Dockerfile scenarios/poolexhausted >/dev/null
platform/guest/build-rootfs.sh lab-pool-exhausted .data/images/pool-exhausted.ext4 2G
