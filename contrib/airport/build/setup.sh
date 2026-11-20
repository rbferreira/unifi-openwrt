#!/bin/sh
# Fetches what the builds here link against, into ./dl and ./sysroot (no root needed):
# headers and static libraries from the NetBSD 7.0 evbarm-earm comp set, the
# wpa_supplicant 0.7.3 source and NetBSD 7's getifaddrs.c. All checksummed.
# The cross compiler comes from the distribution: apt install gcc-arm-linux-gnueabi
set -eu
cd "$(dirname "$0")"
mkdir -p dl sysroot
get() { [ -f "dl/$1" ] || curl -fL -o "dl/$1" "$2"; }
get comp.tgz https://archive.netbsd.org/pub/NetBSD-archive/NetBSD-7.0/evbarm-earm/binary/sets/comp.tgz
echo "2230ba2b5bc4d044b82d96e3604e31fe5180723853a7d2f244243df2985976271834742cbc8f9dcad7b810b6bd463b419b3a2f6cfa8d0529385a69e4245eff16  dl/comp.tgz" | sha512sum -c -
get wpa_supplicant-0.7.3.tar.gz https://w1.fi/releases/wpa_supplicant-0.7.3.tar.gz
echo "d0cd50caa85346ccc376dcda5ed3c258eef19a93b3cade39d25760118ad59443  dl/wpa_supplicant-0.7.3.tar.gz" | sha256sum -c -
get getifaddrs.c https://raw.githubusercontent.com/NetBSD/src/netbsd-7/lib/libc/net/getifaddrs.c
echo "83cc8fe295660b27bbaffd2f16582f3c735d7b69b289402689f599bdea0fa095  dl/getifaddrs.c" | sha256sum -c -
[ -f sysroot/usr/lib/libc.a ] || tar xzf dl/comp.tgz -C sysroot ./usr/include ./usr/lib
# <machine/...> is a directory of wrappers on a real system; the set ships arm/ and evbarm/.
if [ ! -d sysroot/usr/include/machine ] || [ -L sysroot/usr/include/machine ]; then
	[ -L sysroot/usr/include/machine ] && unlink sysroot/usr/include/machine
	mkdir sysroot/usr/include/machine
	for f in sysroot/usr/include/arm/*.h sysroot/usr/include/evbarm/*.h; do
		ln -sf "../$(basename "$(dirname "$f")")/$(basename "$f")" sysroot/usr/include/machine/
	done
fi
echo "sysroot ready"
