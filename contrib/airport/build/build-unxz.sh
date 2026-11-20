#!/bin/sh
# Builds ../unxz.c (liblzma from the sysroot) into ./out/unxz
set -eu
. "$(dirname "$0")/cc.sh"
mkdir -p "$HERE/out"
$XCC -Os -ffunction-sections -fdata-sections -c "$HERE/../unxz.c" -o "$HERE/out/unxz.o"
arm-linux-gnueabi-ld -static --gc-sections --build-id=none -o "$HERE/out/unxz" \
	"$SR/usr/lib/crt0.o" "$SR/usr/lib/crti.o" "$SR/usr/lib/crtbegin.o" "$HERE/out/unxz.o" \
	-L"$SR/usr/lib" --start-group -llzma -lc -lgcc --end-group "$SR/usr/lib/crtend.o" "$SR/usr/lib/crtn.o"
arm-linux-gnueabi-strip "$HERE/out/unxz"
ls -la "$HERE/out/unxz"
