#!/bin/sh
# wpa_supplicant 0.7.3 (WPA2-PSK only) for the AirPort Time Capsule A1470: ./out/wpa_supplicant
# Stock source, unedited; compat/ carries the kernel ABI differences (see ../README.md).
# Recipe ported from TDSOJohn/airport-express-tools (A1392, MIPS, NetBSD 4), see NOTICE.
set -eu
. "$(dirname "$0")/cc.sh"
SRC=$HERE/wpa_supplicant-0.7.3
[ -d "$SRC" ] || tar xzf "$HERE/dl/wpa_supplicant-0.7.3.tar.gz" -C "$HERE"
cat > "$SRC/wpa_supplicant/.config" <<'CFG'
CONFIG_DRIVER_BSD=y
CONFIG_L2_PACKET=bpf
CONFIG_TLS=internal
CONFIG_INTERNAL_LIBTOMMATH=y
CONFIG_CTRL_IFACE=y
CONFIG_BACKEND=file
CFG
W="$XCC -std=gnu89 -fcommon -I$HERE/compat -DCOMPAT_FREEBSD_NET80211 \
 -Wno-implicit-function-declaration -Wno-int-conversion -Wno-incompatible-pointer-types"
mkdir -p "$HERE/out"
$W -O2 -I"$HERE/compat/libc" -c -o "$HERE/out/getifaddrs.o" "$HERE/dl/getifaddrs.c"
$W -Os -c -o "$HERE/out/getgrnam_stub.o" "$HERE/compat/libc/getgrnam_stub.c"
cp "$HERE/compat/l2_packet/l2_packet_bpf.c" "$SRC/src/l2_packet/"
cd "$SRC/wpa_supplicant"
make clean >/dev/null 2>&1 || true
CFLAGS="-MMD -Os -ffunction-sections -fdata-sections" \
	make -j"$(nproc)" wpa_supplicant CC="$W" LDO="$HERE/ldwpa.sh $HERE/out/getifaddrs.o $HERE/out/getgrnam_stub.o"
arm-linux-gnueabi-strip wpa_supplicant
cp wpa_supplicant "$HERE/out/wpa_supplicant"
ls -la "$HERE/out/wpa_supplicant"
