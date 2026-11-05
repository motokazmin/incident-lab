#!/usr/bin/env bash
# Скачивает Firecracker и гостевое ядро в .data/fc. Обе контрольные суммы обязательны.
# Сумму ядра закрепили при первом скачивании 05.11.2026: у бакета CI нет своего файла сумм.
set -euo pipefail
cd "$(dirname "$0")/.."
V=v1.17.0
KERNEL=https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-6.1.155
BASE=https://github.com/firecracker-microvm/firecracker/releases/download/$V
mkdir -p .data/fc && cd .data/fc
curl -fsSLO "$BASE/firecracker-$V-x86_64.tgz"
curl -fsSLO "$BASE/firecracker-$V-x86_64.tgz.sha256.txt"
sha256sum -c "firecracker-$V-x86_64.tgz.sha256.txt"
tar -xzf "firecracker-$V-x86_64.tgz"
ln -sf "release-$V-x86_64/firecracker-$V-x86_64" firecracker
curl -fsSL -o vmlinux-6.1.155 "$KERNEL"
echo "e20e46d0c36c55c0d1014eb20576171b3f3d922260d9f792017aeff53af3d4f2  vmlinux-6.1.155" | sha256sum -c
ln -sf vmlinux-6.1.155 vmlinux
