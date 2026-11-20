#!/bin/sh
# Builds ../relay.c into ./out/relay
set -eu
. "$(dirname "$0")/cc.sh"
mkdir -p "$HERE/out"
$XCC -Os -c "$HERE/../relay.c" -o "$HERE/out/relay.o"
xld "$HERE/out/relay" "$HERE/out/relay.o"
arm-linux-gnueabi-strip "$HERE/out/relay"
ls -la "$HERE/out/relay"
