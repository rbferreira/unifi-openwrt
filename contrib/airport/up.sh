#!/bin/sh
# /mnt/Flash/mesh/up.sh - an AirPort Time Capsule A1470 as a UniFi mesh AP.
# Started by /mnt/Flash/rc.local. Unpacks and runs the uow daemon, creates a client VAP on
# the 5 GHz radio (bwl0) with our own wpa_supplicant (the firmware's is broken), puts the
# uplink in the LAN bridge and leaves the WLANs to the daemon. Then it watches:
#  - a cable with link on the WAN port = wired uplink: the wireless uplink goes (no loop);
#  - no WAN link for 20 s: back to the wireless uplink;
#  - supplicant dead or not associated for 60 s: start over;
#  - the parent changed channel: the 5 GHz WLANs start again on it;
#  - the daemon died: start it again.
# Log: /mnt/Memory/mesh.log. Nothing here changes the base station's stored configuration.
PATH=/sbin:/usr/sbin:/bin:/usr/bin
D=/mnt/Flash/mesh; M=/mnt/Memory; IF=wlan9; BR=bridge0
exec >> $M/mesh.log 2>&1 < /dev/null
say() { echo "$(date '+%H:%M:%S') $*"; }
pids() { ps ax | while read pid rest; do case "$rest" in *$1*) echo $pid;; esac; done; }
assoc() { case "$(ifconfig $IF 2>&1)" in *'status: associated'*) return 0;; esac; return 1; }
inbr() { case "$(brconfig $BR 2>&1)" in *"$IF flags"*) return 0;; esac; return 1; }
led() { /usr/bin/acp -q LEDc=$1; }
# WAN port = PHY index 0 in acpd.system.show
wan() {
	/usr/bin/acp rpc acpd.system.show 2>/dev/null | {
		hit=0; r=1
		while read a b c d e f g; do
			case "$a $c $d" in "PHY index 0") hit=1; continue;; esac
			if [ $hit = 1 ]; then
				case "$a $b $c $d" in "Link State - Current") case "$e $f $g" in *Down*) r=1;; *) r=0;; esac; break;; esac
			fi
		done
		exit $r
	}
}
# The WLANs are the daemon's: with -apply it runs one hostapd per WLAN the controller asks
# for, on VAPs wlan10..17 (2.4 GHz, bwl1) and wlan20..27 (5 GHz, bwl0), and keeps its files in
# $M/uowap. Both radios are off in the stored configuration, so the firmware runs none.
# The 5 GHz ones share the radio with the uplink: they are taken down here before the uplink
# is created again or after it changed channel, and the daemon brings them back.
upch() { ifconfig $IF 2>/dev/null | while read a b c d e; do case "$a" in ssid) echo $d;; esac; done; }
vaps5_stop() {
	for f in $M/uowap/wlan2[0-7].pid; do
		[ -f "$f" ] || continue
		v=${f##*/}; v=${v%.pid}
		p=$(cat $f); [ -n "$p" ] && kill $p 2>/dev/null
		case "$(brconfig $BR 2>&1)" in *"$v flags"*) brconfig $BR delete $v;; esac
		ifconfig $v destroy 2>/dev/null
		rm -f $f $M/uowap/$v.conf
	done
	upc=
}
# The uow daemon (unifi-openwrt, -ap-platform airport) runs on the base station itself.
# The binary is too big for /mnt/Flash, so it lives xz-compressed in the erased tail of the
# reserve firmware bank (flash1, see uow.meta) and is unpacked into the RAM disk at boot.
# Its state directory is in RAM too; state.json, the adoption SSH keys and the controller's
# last configuration are kept on flash, so the WLANs come up at boot without the controller
# (the config history it writes would fill the flash in time).
UOW=$M/uow; ST=$M/state; PST=$D/state
uow_unpack() {
	[ -s $D/uow.meta ] && [ -x $D/unxz ] || { say "uow: no uow.meta or unxz, not started"; return 1; }
	. $D/uow.meta
	dd if=/dev/rflash1c bs=65536 skip=$SKIP count=$COUNT 2>/dev/null | $D/unxz > $UOW || { say "uow: unpack failed"; return 1; }
	set -- $(ls -l $UOW); [ "$5" = "$SIZE" ] || { say "uow: unpacked $5 bytes, expected $SIZE"; return 1; }
	chmod 755 $UOW
	mkdir -p $ST $PST
	for f in $PST/*; do [ -f "$f" ] && cp -p "$f" $ST/; done
	# regulatory ids of this unit, for the daemon's hostapd configs: saved the first time,
	# while the firmware still runs a WLAN of its own
	[ -s $D/hostap.seed ] || { [ -f /etc/hostap_wlan0.conf ] && sed -n -e '/^sku=/p' -e '/^region=/p' /etc/hostap_wlan0.conf > $D/hostap.seed; }
	return 0
}
uow_alive() { p=$(cat $M/uow.pid 2>/dev/null); [ -n "$p" ] && kill -0 $p 2>/dev/null; }
uow_start() {
	[ -x $UOW ] && [ -s $D/uow.conf ] || return
	case "$(ifconfig $BR)" in *"inet 1"[0-9]*|*"inet 19"[0-9]*|*"inet 2"*) ;; *) return ;; esac   # wait for an address
	. $D/uow.conf    # URL= MODEL= NAME=
	mac=$(ifconfig $BR | while read a b c; do case "$a" in ether) echo $b;; esac; done)
	$UOW -ap local -ap-platform airport -mac $mac -url "$URL" -model "$MODEL" -profile-caps -hostname "$NAME" \
		-apply -state $ST -ssh-listen :2222 -health-every 0 -stop-on-reset=false >> $M/uow.log 2>&1 < /dev/null &
	echo $! > $M/uow.pid
	say "uow started (pid $(cat $M/uow.pid))"
}
uow_keep() {
	uow_alive || uow_start
	# persist the adoption state when it changes (small files only)
	for f in state.json system_cfg.txt ssh_host_ed25519.pem ssh_host_ed25519_rsa.pem; do
		[ -f $ST/$f ] || continue
		[ "$(cat $ST/$f)" = "$(cat $PST/$f 2>/dev/null)" ] || { cp -p $ST/$f $PST/$f; say "uow: saved $f"; }
	done
	set -- $(ls -l $M/uow.log 2>/dev/null); [ "${5:-0}" -gt 400000 ] && : > $M/uow.log
}
# VLAN interfaces the daemon put on the uplink (WLANs on a VLAN) go before the uplink VAP is
# destroyed; the daemon notices and builds them again on the new one.
vlans_stop() {
	for v in $(ifconfig -l); do
		case $v in vlan[0-9]*) ;; *) continue;; esac
		case "$(ifconfig $v 2>&1)" in *"parent interface: $IF"*) ifconfig $v destroy 2>/dev/null;; esac
	done
}
alive() { p=$(cat $M/wpa.pid 2>/dev/null); [ -n "$p" ] && kill -0 $p 2>/dev/null; }
leave() {
	vaps5_stop
	vlans_stop
	inbr && brconfig $BR delete $IF
	p=$(cat $M/wpa.pid 2>/dev/null); [ -n "$p" ] && kill $p 2>/dev/null
	rm -f $M/wpa.pid
	ifconfig $IF down 2>/dev/null
	say "$1: wireless uplink removed"
}
# Which wpa config a join uses. The daemon keeps wpa.conf in step with the controller and
# leaves the file it replaced as wpa.conf.prev. A join that does not associate makes the next one
# use the other file, and so on, so a wrong key pushed by the controller cannot strand the unit
# while the old one still works. Nothing is deleted. Without a previous file there is only
# wpa.conf. After WN failed joins in a row with wpa.conf, while wpa.conf.prev did associate, the
# joins start with the file that last worked. Memory of the script's life only: wl = the file
# that last associated, wfail = joins in a row that wpa.conf failed, wnext = the file to try
# after a failure, wsig = what wpa.conf said at the last join (a new text starts over).
WC=wpa.conf; WP=wpa.conf.prev; WN=3; wl=; wfail=0; wnext=; wsig=; wf=$WC; wquick=
wpick() {
	cur=$(cat $D/$WC 2>/dev/null)
	[ "$cur" = "$wsig" ] || { [ -n "$wsig" ] && say "uplink: $WC changed"; wsig=$cur; wl=; wfail=0; wnext=; }
	wf=$WC
	[ "$wl" = $WP ] && [ $wfail -ge $WN ] && wf=$WP
	[ -n "$wnext" ] && wf=$wnext
	if [ ! -f $D/$wf ]; then
		[ $wf = $WC ] && o=$WP || o=$WC
		[ -f $D/$o ] && wf=$o
	fi
}
# the join failed with $wf: the other file, if there is one, is next
wfailed() {
	[ $wf = $WC ] && { wfail=$((wfail + 1)); o=$WP; } || o=$WC
	wnext=
	[ -f $D/$o ] && { wnext=$o; wquick=1; }
}
join() {
	wpick
	say "uplink: joining with $wf"
	vaps5_stop
	vlans_stop
	A=$(cat /var/run/ACPd.pid)
	kill -STOP $A
	ifconfig $IF destroy 2>/dev/null
	ifconfig $IF create wlandev bwl0 wlanmode sta || say "create failed"
	ifconfig $IF media autoselect
	ifconfig $IF dwds
	ifconfig $IF up; ifconfig $IF down
	$M/wpa_supplicant -t -D bsd -i $IF -c $D/$wf > $M/wpa.log 2>&1 &
	echo $! > $M/wpa.pid
	sleep 3
	kill -CONT $A
	n=0
	until assoc; do n=$((n + 1)); [ $n -gt 45 ] && break; sleep 2; done
	assoc && sleep 3   # the WPA handshake ends after "associated"; a wrong key is dropped in it
	if assoc; then
		wl=$wf; wnext=; [ $wf = $WC ] && wfail=0
		brconfig $BR add $IF up && say "uplink associated and in $BR" || say "brconfig failed"
		led 2
		upc=$(upch)
	else
		say "uplink did not associate in 90 s with $wf"; led 1
		wfailed
	fi
}

say "up.sh start"
# Safety: in router mode the base station runs a DHCP server (10.0.1.x) on the LAN bridge.
# Bridging the uplink into it would leak that server onto the network. Bridge mode only.
if [ -n "$(pids /usr/sbin/dhcpd)" ]; then say "dhcpd running (router mode): uplink NOT started"; led 1; exit 0; fi
cp $D/wpa_supplicant $M/wpa_supplicant; chmod 755 $M/wpa_supplicant
uow_unpack && HAVE_UOW=1 || HAVE_UOW=0

lost=20; meshing=0; bad=0; upc=
while :; do
	if wan; then
		lost=0
		[ $meshing = 1 ] && { leave "cable on the WAN port"; meshing=0; led 2; }
	else
		lost=$((lost + 5))
		if [ $meshing = 0 ] && [ $lost -ge 20 ]; then wquick=; join; meshing=1; bad=0; [ -n "$wquick" ] && bad=60; fi
		if [ $meshing = 1 ]; then
			if alive && assoc; then
				bad=0
				# the parent changed channel: the 5 GHz WLANs start again on it
				c=$(upch); [ -n "$upc" ] && [ -n "$c" ] && [ "$c" != "$upc" ] && { say "uplink moved to channel $c"; vaps5_stop; upc=$c; }
			else
				bad=$((bad + 5))
			fi
			if [ $bad -ge 60 ]; then leave "not associated for 60 s"; meshing=0; lost=20; bad=0; led 1; fi
		fi
	fi
	[ $HAVE_UOW = 1 ] && uow_keep
	sleep 5
done
