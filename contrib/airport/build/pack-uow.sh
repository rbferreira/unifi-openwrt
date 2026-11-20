#!/bin/sh
# pack-uow.sh UOW_BINARY: makes out/uow.blob (.xz padded with 0xFF to 64 KB sectors) and
# out/uow.meta.new for ../flash-uow.sh. Build the binary with the patched Go toolchain
# (../go/README.md):
#   GOROOT=<patched> GOTOOLCHAIN=local GOOS=netbsd GOARCH=arm GOARM=7 CGO_ENABLED=0 \
#     go build -trimpath -ldflags='-s -w' -o uow-netbsd-arm ./cmd/uow
set -eu
HERE=$(cd "$(dirname "$0")" && pwd); mkdir -p "$HERE/out"
BIN=${1:?usage: pack-uow.sh UOW_BINARY}; SKIP=${SKIP:-144}
xz -9e --arm --lzma2=dict=8MiB -c "$BIN" > "$HERE/out/uow.xz"
SZ=$(stat -c %s "$HERE/out/uow.xz"); COUNT=$(( (SZ + 65535) / 65536 ))
{ cat "$HERE/out/uow.xz"; head -c $(( COUNT * 65536 - SZ )) /dev/zero | tr '\0' '\377'; } > "$HERE/out/uow.blob"
printf 'SKIP=%s\nCOUNT=%s\nSIZE=%s\nMD5=%s\n' "$SKIP" "$COUNT" "$(stat -c %s "$BIN")" "$(md5sum "$BIN" | cut -d' ' -f1)" > "$HERE/out/uow.meta.new"
cat "$HERE/out/uow.meta.new"
