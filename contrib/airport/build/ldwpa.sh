#!/bin/sh
# LDO wrapper for wpa_supplicant's Makefile: keeps the objects, links like cc.sh.
set -eu
. "$(dirname "$0")/cc.sh"
out=""; objs=""
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out=$2; shift 2 ;;
	*.o) objs="$objs $1"; shift ;;
	*) shift ;;
	esac
done
xld "$out" $objs
