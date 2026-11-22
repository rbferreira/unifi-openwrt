#!/bin/sh
# flash-uow.sh - run ON the base station: writes a new uow image into the erased tail of
# the reserve firmware bank (flash1) and makes the running watcher pick it up.
# Needs /mnt/Memory/uow.blob (the .xz padded to 64 KB sectors) and /mnt/Memory/uow.meta.new
# (SKIP COUNT SIZE MD5), both made by build/pack-uow.sh.
#
# Bank layout measured on an A1470 (7.9.1): flash0 = running firmware, flash1 = factory
# firmware; in both the image ends near 8.2 MB and everything from there to the trailer in
# the last 64 KB sector is erased. SKIP must stay at or above sector 127 and SKIP+COUNT at
# or below 223. An overwrite does not erase: every sector is erased first, by number.
# NEVER run "flashctl ... erase" without a sector number, and never run eraseflash: the
# first erases the whole bank, the second the base station's configuration.
PATH=/sbin:/usr/sbin:/bin:/usr/bin
M=/mnt/Memory; D=/mnt/Flash/mesh
. $M/uow.meta.new || exit 1
[ "$SKIP" -ge 127 ] && [ $((SKIP + COUNT)) -le 223 ] || { echo "SKIP/COUNT outside the free area"; exit 1; }
s=$SKIP; end=$((SKIP + COUNT))
while [ $s -lt $end ]; do flashctl /dev/rflash1.raw erase $s > /dev/null 2>&1 || { echo "erase $s failed"; exit 1; }; s=$((s + 1)); done
dd if=$M/uow.blob of=/dev/rflash1c bs=65536 seek=$SKIP count=$COUNT 2>/dev/null || exit 1
rm -f $M/uow.blob     # the RAM disk cannot hold the blob and two copies of the binary
got=$(dd if=/dev/rflash1c bs=65536 skip=$SKIP count=$COUNT 2>/dev/null | $D/unxz | openssl md5 2>/dev/null | sed "s/.*= //")
[ "$got" = "$MD5" ] || { echo "checksum mismatch: $got, want $MD5 (the old binary keeps running from RAM)"; exit 1; }
cp $M/uow.meta.new $D/uow.meta && rm -f $M/uow.meta.new
# The RAM disk holds one copy of the binary, not two: the old daemon must be gone, and its
# file with it, before the new one is unpacked. Unpacked under another name, so the watcher
# cannot start a half-written binary (both happened: the disk filled, the copy came out short
# and the space only came back with a reboot).
p=$(cat $M/uow.pid 2>/dev/null); rm -f $M/uow
if [ -n "$p" ]; then kill $p 2>/dev/null; n=0; while kill -0 $p 2>/dev/null && [ $n -lt 15 ]; do sleep 1; n=$((n + 1)); done; fi
dd if=/dev/rflash1c bs=65536 skip=$SKIP count=$COUNT 2>/dev/null | $D/unxz > $M/uow.new
set -- $(ls -l $M/uow.new)
if [ "$5" != "$SIZE" ]; then rm -f $M/uow.new; echo "unpacked $5 bytes, expected $SIZE: RAM disk full? The image on flash is good: reboot the base station"; exit 1; fi
chmod 755 $M/uow.new && mv $M/uow.new $M/uow
echo "installed; up.sh restarts the daemon within 5 s"
