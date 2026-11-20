#!/bin/sh
# /mnt/Flash/mesh/up.sh - an AirPort Time Capsule A1470 as a UniFi mesh AP.
# Started by /mnt/Flash/rc.local. Unpacks and runs the uow daemon, creates a client VAP on
# the 5 GHz radio (bwl0) with our own wpa_supplicant (the firmware's is broken), puts the
# uplink in the LAN bridge and starts a 5 GHz AP next to it. Then it watches:
#  - a cable with link on the WAN port = wired uplink: the wireless uplink goes (no loop);
#  - no WAN link for 20 s: back to the wireless uplink;
#  - supplicant dead or not associated for 60 s: start over;
#  - the parent changed channel: the 5 GHz AP follows;
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
# 5 GHz AP on the uplink's radio (bwl0). The firmware's hostapd only runs the 2.4 GHz WLAN
# (5 GHz is off in the stored configuration), so this one is started from its config: same
# SSID and key, on the channel the uplink associated on. hw_mode=a and channel= are both
# needed: with hw_mode=b it comes up on channel 36 and takes the uplink down.
AP5=wlan8
upch() { ifconfig $IF 2>/dev/null | while read a b c d e; do case "$a" in ssid) echo $d;; esac; done; }
ap5_stop() {
	p=$(cat $M/hostapd_$AP5.pid 2>/dev/null); [ -n "$p" ] && kill $p 2>/dev/null
	rm -f $M/hostapd_$AP5.pid
	case "$(brconfig $BR 2>&1)" in *"$AP5 flags"*) brconfig $BR delete $AP5;; esac
	ifconfig $AP5 destroy 2>/dev/null
	ap5ch=
}
ap5_start() {
	[ -f /etc/hostap_wlan0.conf ] || { say "no /etc/hostap_wlan0.conf (2.4 GHz off?): no 5 GHz AP"; return; }
	ch=$(upch); [ -n "$ch" ] || return
	sed -e "s/wlan0/$AP5/g" -e 's/^phy_mode=.*/phy_mode=10/' -e 's/^hw_mode=.*/hw_mode=a/' -e 's/^mcast_rate=.*/mcast_rate=6/' 	    -e 's/^ifparent=.*/ifparent=bwl0/' -e 's/^allow_ht40=.*/allow_ht40=1/' -e 's/^allow_vht80=.*/allow_vht80=1/' 	    -e 's/^acs=.*/acs=0/' -e '/^wps_/d' -e "s#/etc/hostap_$AP5#$M/hostap_$AP5#g" -e "s#/tmp/hostapd_$AP5#$M/hostapd_$AP5#" 	    /etc/hostap_wlan0.conf > $M/hostap_$AP5.conf
	echo "channel=$ch" >> $M/hostap_$AP5.conf
	for f in wpa_psk eap_user time_acc; do [ -f /etc/hostap_wlan0.$f ] && sed "s/wlan0/$AP5/g" /etc/hostap_wlan0.$f > $M/hostap_$AP5.$f; done
	ifconfig $AP5 create wlandev bwl0 wlanmode hostap || { say "5 GHz AP: create failed"; return; }
	hostapd -t -F $M/hostap_$AP5.log $M/hostap_$AP5.conf > $M/hostapd_$AP5.out 2>&1 &
	echo $! > $M/hostapd_$AP5.pid
	sleep 6
	brconfig $BR add $AP5 && say "5 GHz AP on channel $ch, in the bridge" || say "5 GHz AP: brconfig failed"
	ap5ch=$ch
}
# The uow daemon (unifi-openwrt, -ap-platform airport) runs on the base station itself.
# The binary is too big for /mnt/Flash, so it lives xz-compressed in the erased tail of the
# reserve firmware bank (flash1, see uow.meta) and is unpacked into the RAM disk at boot.
# Its state directory is in RAM too; only state.json and the adoption SSH keys are kept
# on flash (the config history it writes would fill the flash in time).
UOW=$M/uow; ST=$M/state; PST=$D/state
uow_unpack() {
	[ -s $D/uow.meta ] && [ -x $D/unxz ] || { say "uow: no uow.meta or unxz, not started"; return 1; }
	. $D/uow.meta
	dd if=/dev/rflash1c bs=65536 skip=$SKIP count=$COUNT 2>/dev/null | $D/unxz > $UOW || { say "uow: unpack failed"; return 1; }
	set -- $(ls -l $UOW); [ "$5" = "$SIZE" ] || { say "uow: unpacked $5 bytes, expected $SIZE"; return 1; }
	chmod 755 $UOW
	mkdir -p $ST $PST
	for f in $PST/*; do [ -f "$f" ] && cp -p "$f" $ST/; done
	# the config text is not kept across boots: ask the controller for it again
	[ -f $ST/state.json ] && sed 's/"cfgversion": "[^"]*"/"cfgversion": "0"/' $ST/state.json > $ST/state.tmp && mv $ST/state.tmp $ST/state.json
	return 0
}
uow_alive() { p=$(cat $M/uow.pid 2>/dev/null); [ -n "$p" ] && kill -0 $p 2>/dev/null; }
uow_start() {
	[ -x $UOW ] && [ -s $D/uow.conf ] || return
	case "$(ifconfig $BR)" in *"inet 1"[0-9]*|*"inet 19"[0-9]*|*"inet 2"*) ;; *) return ;; esac   # wait for an address
	. $D/uow.conf    # URL= MODEL= NAME=
	mac=$(ifconfig $BR | while read a b c; do case "$a" in ether) echo $b;; esac; done)
	$UOW -ap local -ap-platform airport -mac $mac -url "$URL" -model "$MODEL" -profile-caps -hostname "$NAME" \
		-state $ST -ssh-listen :2222 -health-every 0 -stop-on-reset=false >> $M/uow.log 2>&1 < /dev/null &
	echo $! > $M/uow.pid
	say "uow started (pid $(cat $M/uow.pid))"
}
uow_keep() {
	uow_alive || uow_start
	# persist the adoption state when it changes (small files only)
	for f in state.json ssh_host_ed25519.pem ssh_host_ed25519_rsa.pem; do
		[ -f $ST/$f ] || continue
		[ "$(cat $ST/$f)" = "$(cat $PST/$f 2>/dev/null)" ] || { cp -p $ST/$f $PST/$f; say "uow: saved $f"; }
	done
	set -- $(ls -l $M/uow.log 2>/dev/null); [ "${5:-0}" -gt 400000 ] && : > $M/uow.log
}
alive() { p=$(cat $M/wpa.pid 2>/dev/null); [ -n "$p" ] && kill -0 $p 2>/dev/null; }
leave() {
	ap5_stop
	inbr && brconfig $BR delete $IF
	p=$(cat $M/wpa.pid 2>/dev/null); [ -n "$p" ] && kill $p 2>/dev/null
	rm -f $M/wpa.pid
	ifconfig $IF down 2>/dev/null
	say "$1: wireless uplink removed"
}
join() {
	A=$(cat /var/run/ACPd.pid)
	kill -STOP $A
	ifconfig $IF destroy 2>/dev/null
	ifconfig $IF create wlandev bwl0 wlanmode sta || say "create failed"
	ifconfig $IF media autoselect
	ifconfig $IF dwds
	ifconfig $IF up; ifconfig $IF down
	$M/wpa_supplicant -t -D bsd -i $IF -c $D/wpa.conf > $M/wpa.log 2>&1 &
	echo $! > $M/wpa.pid
	sleep 3
	kill -CONT $A
	n=0
	until assoc; do n=$((n + 1)); [ $n -gt 45 ] && break; sleep 2; done
	if assoc; then
		sleep 3   # the WPA handshake ends after "associated"
		brconfig $BR add $IF up && say "uplink associated and in $BR" || say "brconfig failed"
		led 2
		ap5_start
	else
		say "uplink did not associate in 90 s"; led 1
	fi
}

say "up.sh start"
# Safety: in router mode the base station runs a DHCP server (10.0.1.x) on the LAN bridge.
# Bridging the uplink into it would leak that server onto the network. Bridge mode only.
if [ -n "$(pids /usr/sbin/dhcpd)" ]; then say "dhcpd running (router mode): uplink NOT started"; led 1; exit 0; fi
cp $D/wpa_supplicant $M/wpa_supplicant; chmod 755 $M/wpa_supplicant
uow_unpack && HAVE_UOW=1 || HAVE_UOW=0

lost=20; meshing=0; bad=0; ap5ch=
while :; do
	if wan; then
		lost=0
		[ $meshing = 1 ] && { leave "cable on the WAN port"; meshing=0; led 2; }
	else
		lost=$((lost + 5))
		if [ $meshing = 0 ] && [ $lost -ge 20 ]; then join; meshing=1; bad=0; fi
		if [ $meshing = 1 ]; then
			if alive && assoc; then
				bad=0
				# the parent changed channel: the 5 GHz AP follows
				c=$(upch); [ -n "$ap5ch" ] && [ -n "$c" ] && [ "$c" != "$ap5ch" ] && { say "uplink moved to channel $c"; ap5_stop; ap5_start; }
			else
				bad=$((bad + 5))
			fi
			if [ $bad -ge 60 ]; then leave "not associated for 60 s"; meshing=0; lost=20; bad=0; led 1; fi
		fi
	fi
	[ $HAVE_UOW = 1 ] && uow_keep
	sleep 5
done
