#!/bin/sh
# Flags for a static NetBSD ARM EABI binary that runs on the A1470's NetBSD 6.0 kernel.
# Source this: it sets XCC (compile) and defines xld (link: xld OUT OBJ...).
HERE=$(cd "$(dirname "$0")" && pwd)
SR=$HERE/sysroot
CC=arm-linux-gnueabi-gcc
XCC="$CC -march=armv5te -mfloat-abi=soft -marm -fno-pic -fno-pie -fno-stack-protector -nostdinc \
 -isystem $($CC -print-file-name=include) -isystem $SR/usr/include \
 -U__linux__ -U__gnu_linux__ -U__linux -Ulinux -Uunix -D__NetBSD__ -D__ELF__"
xld() {
	out=$1; shift
	arm-linux-gnueabi-ld -static --gc-sections --build-id=none -o "$out" \
	  "$SR/usr/lib/crt0.o" "$SR/usr/lib/crti.o" "$SR/usr/lib/crtbegin.o" "$@" \
	  -L"$SR/usr/lib" --start-group -lc -lgcc --end-group "$SR/usr/lib/crtend.o" "$SR/usr/lib/crtn.o"
}
