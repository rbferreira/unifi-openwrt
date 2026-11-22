// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// Applying the controller's Wi-Fi to an AirPort: one hostapd per wanted WLAN
// per radio, each on its own VAP (net80211 wlanN on top of bwl0/bwl1). The
// base station has no uci; the firmware keeps both radios off and runs no
// hostapd of its own for them, so with -apply the daemon owns every VAP in the
// two name ranges below. The mesh uplink and the firmware's own VAPs are
// outside those ranges and never touched. Everything lives in RAM (apRunDir).

const (
	apRunDir   = "/mnt/Memory/uowap"
	apSeedFile = "/mnt/Flash/mesh/hostap.seed" // sku= and region= of this unit, saved at install time
	apMaxVAPs  = 8                             // per radio: wlan10..17 and wlan20..27
	apMaxSta   = 64                            // the driver's bss_maxassoc: hostapd's max_clients above it means nothing
	apDTIM     = 3                             // the firmware's own dtim
)

// apVAPName is what the applier may create, stop and destroy: nothing else.
var apVAPName = regexp.MustCompile(`^wlan(1[0-7]|2[0-7])$`)

// AirPortVAP is one hostapd to run.
type AirPortVAP struct {
	Name     string // wlan10..17 (2.4 GHz), wlan20..27 (5 GHz)
	Parent   string // bwl1 (2.4 GHz) or bwl0 (5 GHz)
	SSID     string // for the log
	Security string // "none" or "psk2"
	Channel  int    // 0 = the radio chooses (acs=1)
	// ChannelOf: with an automatic channel only the first VAP of a radio may
	// choose (acs=1); the others take the channel that VAP is on, read on the
	// device when they start. "" = this VAP has its own channel or chooses.
	ChannelOf string
	// Tune: put the radio on Channel. hostapd's channel= does not move this
	// driver's radio; "ifconfig VAP channel N" on the first VAP of an idle radio
	// does (measured). False on the radio of the mesh uplink, which the uplink
	// alone tunes.
	Tune bool
	// Spec is the channel as ifconfig takes it when Tune is set: "6" on
	// 2.4 GHz (that radio stays at 20 MHz whatever is asked), and with the
	// width on 5 GHz: "44:vht/80", "44:ht/40", "44:ht/20". The width is the
	// radio's, set with the channel; hostapd's allow_ht40/allow_vht80 change
	// nothing (all measured).
	Spec string
	// Head is the hostapd config up to and including ifparent=; Tail the rest.
	// The unit's own sku= and region= lines go between them, and only the
	// device knows them (apSeedFile).
	Head, Tail string

	// What is not in the hostapd config, so changing it never restarts a radio:
	// the driver ACL (airport_ctl.go: MACPolicy "disable", "allow" or "deny" with
	// MACs, lower case and sorted), client isolation (the driver's apbridge flag)
	// and the radio's transmit power (set on the radio's first VAP).
	MACPolicy string
	MACs      []string
	Isolate   bool
	// TxPower in dBm: 0 = the radio's own power. On both radios, the 5 GHz one
	// with the mesh uplink too (measured: it lowers the uplink's power with it
	// and the uplink stays associated).
	TxPower int
}

// Config is the whole hostapd config, with the unit's regulatory ids.
func (v AirPortVAP) Config(sku, region string) string {
	return v.Head + "sku=" + sku + "\nregion=" + region + "\n" + v.Tail
}

// String is the log line: no passphrase in it.
func (v AirPortVAP) String() string {
	ch := "auto channel"
	if v.Spec != "" {
		ch = "channel " + v.Spec
	} else if v.Channel > 0 {
		ch = fmt.Sprintf("channel %d", v.Channel)
	} else if v.ChannelOf != "" {
		ch = "channel of " + v.ChannelOf
	}
	return fmt.Sprintf("%s on %s: ssid %q, %s, %s", v.Name, v.Parent, v.SSID, v.Security, ch)
}

// AirPortVAPs turns what the controller wants into the VAPs to run. uplink5 is
// the channel of the mesh uplink on the 5 GHz radio (0 = no uplink there):
// that radio follows its uplink, so the VAPs stay on its channel. The notes
// say what was left out or approximated, once per kind; none has a passphrase.
func AirPortVAPs(w translate.Wanted, uplink5 int) ([]AirPortVAP, []string) {
	var notes []string
	radios := map[string]translate.WantedRadio{}
	for _, r := range w.Radios {
		radios[r.Name] = r
	}
	slots := map[string]int{}    // parent -> VAPs so far
	first := map[string]string{} // parent -> its first VAP
	hasVAP := map[string]bool{}
	var vaps []AirPortVAP
	pmf, badMACs, bigLimit := 0, 0, 0

	for _, l := range w.WLANs {
		skip := func(why string) {
			notes = append(notes, fmt.Sprintf("wireless.%s (%q on %s): no VAP: %s", l.N, l.SSID, l.Radio, why))
		}
		r, ok := radios[l.Radio]
		var parent, digit string
		switch {
		case !ok:
			skip("radio unknown")
			continue
		case r.Band == "2g":
			parent, digit = "bwl1", "1"
		case r.Band == "5g":
			parent, digit = "bwl0", "2"
		default:
			skip("this AP has no " + r.Band + " radio")
			continue
		}
		switch {
		case l.Disabled:
			skip("disabled")
		case r.Disabled:
			skip("radio disabled")
		case l.Adopt:
			skip("adoption SSID")
		case l.Guest:
			skip("guest network (it needs a bridge of its own)")
		case l.VLAN != 0:
			skip(fmt.Sprintf("VLAN %d (it needs a bridge of its own)", l.VLAN))
		case l.Security != "none" && l.Security != "psk2":
			skip("security " + l.Security + " is not supported by this hostapd")
		case strings.ContainsAny(l.SSID, "\r\n"):
			skip("SSID has a line break")
		case len(l.SSID) > 32:
			skip("SSID longer than 32 bytes")
		case l.Security == "psk2" && strings.ContainsAny(l.Passphrase, "\r\n"):
			skip("passphrase has a line break")
		case l.Security == "psk2" && (len(l.Passphrase) < 8 || len(l.Passphrase) > 63):
			skip("passphrase is not 8 to 63 characters long")
		case slots[parent] >= apMaxVAPs:
			skip(fmt.Sprintf("more than %d WLANs on this radio", apMaxVAPs))
		default:
			v := airPortVAP(digit+fmt.Sprint(slots[parent]), parent, r, l, uplink5, first[parent])
			if first[parent] == "" {
				first[parent] = v.Name
			}
			slots[parent]++
			hasVAP[r.Name] = true
			var bad int
			v.MACPolicy, v.MACs, bad = aclPolicy(l.MACPolicy, l.MACs)
			badMACs += bad
			if v.MACPolicy == "allow" && len(v.MACs) == 0 {
				notes = append(notes, fmt.Sprintf("wireless.%s (%q on %s): MAC filter allows nobody (empty allow list)", l.N, l.SSID, l.Radio))
			}
			if l.MaxClients > apMaxSta {
				bigLimit++
			}
			vaps = append(vaps, v)
			if l.PMF == 2 {
				pmf++
			}
		}
	}

	var width []string
	for _, r := range w.Radios {
		if !hasVAP[r.Name] {
			continue
		}
		// The width goes with the channel on a 5 GHz radio the daemon tunes
		// (Spec). Not applied: 2.4 GHz above 20 MHz, 160 MHz, and a radio
		// whose channel is the mesh uplink's or automatic.
		tuned := r.Band == "5g" && uplink5 == 0 && r.Channel > 0
		if r.Width != 0 && !(r.Band == "2g" && r.Width == 20) && !(tuned && r.Width <= 80) {
			width = append(width, fmt.Sprintf("%s %d MHz", r.Name, r.Width))
		}
		if r.Band == "5g" && uplink5 > 0 && r.Channel > 0 && r.Channel != uplink5 {
			notes = append(notes, fmt.Sprintf("radio %s: channel %d requested, but the 5 GHz radio follows the mesh uplink on channel %d", r.Name, r.Channel, uplink5))
		}
	}
	if len(width) > 0 {
		notes = append(notes, fmt.Sprintf("channel width not applied (%s): 2.4 GHz stays at 20 MHz, 5 GHz does up to 80 MHz and takes the mesh uplink's width while there is one or the channel is automatic", strings.Join(width, ", ")))
	}
	if badMACs > 0 {
		notes = append(notes, fmt.Sprintf("MAC filter: %d entr(ies) that are not a MAC address were left out", badMACs))
	}
	if bigLimit > 0 {
		notes = append(notes, fmt.Sprintf("client limit above %d on %d WLAN(s): the driver's limit per WLAN is %d", apMaxSta, bigLimit, apMaxSta))
	}
	if pmf > 0 {
		notes = append(notes, fmt.Sprintf("protected management frames required on %d WLAN(s): not supported on this platform", pmf))
	}
	return vaps, notes
}

// airPortVAP renders one VAP. The key order is the firmware's own
// (hostap-samples.txt): this hostapd reads the file top to bottom and a few
// keys depend on the ones before them.
func airPortVAP(suffix, parent string, r translate.WantedRadio, l translate.WantedWLAN, uplink5 int, first string) AirPortVAP {
	g5 := r.Band == "5g"
	pick := func(a, b string) string {
		if g5 {
			return b
		}
		return a
	}
	hide := "0"
	if l.Hidden {
		hide = "1"
	}
	// The 5 GHz radio carries the mesh uplink and follows it: its channel is the
	// uplink's, and a hostapd that does not say so starts on 36 and drags the
	// uplink along. Elsewhere the controller's channel, or auto.
	ch := r.Channel
	if g5 && uplink5 > 0 {
		ch = uplink5
	}
	tune := ch > 0 && !(g5 && uplink5 > 0)
	spec := ""
	if tune {
		spec = fmt.Sprint(ch)
		if g5 {
			switch {
			case r.Width == 20:
				spec += ":ht/20"
			case r.Width == 40:
				spec += ":ht/40"
			default: // 80, more than this radio does, or not said
				spec += ":vht/80"
			}
		}
	}
	name := "wlan" + suffix
	dtim := apDTIM
	if l.DTIM >= 1 && l.DTIM <= 255 {
		dtim = l.DTIM
	}
	maxSta := apMaxSta
	if l.MaxClients >= 1 && l.MaxClients < apMaxSta {
		maxSta = l.MaxClients
	}
	tx := r.TxPower
	if tx < 0 {
		tx = 0
	}

	var h strings.Builder
	w := func(k, v string) { h.WriteString(k + "=" + v + "\n") }
	w("interface", name)
	w("driver", "net80211")
	w("logger_syslog", "0")
	w("logger_syslog_level", "3")
	w("logger_stdout", "-1")
	w("logger_stdout_level", "3")
	w("dump_file", apRunDir+"/"+name+".dump")
	w("ap_table_max_size", "512")
	w("ap_table_expiration_time", "-1")
	w("bridge_packets", "1")
	w("ssid", l.SSID) // raw to the end of the line: hostapd 0.6.5 takes no quotes
	w("hide_ssid", hide)
	w("ampdu", "1")
	w("stbc", "1")
	w("rrm", "1")
	w("phy_mode", pick("6", "10"))
	w("auth_algs", "1")
	w("ieee8021x", "0")
	w("eap_server", "0")
	w("hw_mode", pick("ng", "a"))
	w("doth", "1")
	w("dotd", "1")
	w("dtim", fmt.Sprint(dtim))
	w("mcast_rate", pick("2", "6"))
	if l.Security == "psk2" {
		w("wpa", "2")
		w("wpa_passphrase", l.Passphrase)
		w("wpa_key_mgmt", "WPA-PSK")
		w("rsn_pairwise", "CCMP")
		w("wpa_group_rekey", "3600")
		w("wpa_strict_rekey", "1")
		w("wpa_gmk_rekey", "3600")
	}
	w("ifparent", parent)

	var t strings.Builder
	w = func(k, v string) { t.WriteString(k + "=" + v + "\n") }
	w("allow_ht40", pick("0", "1"))
	w("allow_vht80", pick("0", "1"))
	follow := ""
	if ch == 0 {
		follow = first // "" for the radio's first VAP: that one chooses
	}
	if ch > 0 || follow != "" {
		w("acs", "0")
	} else {
		w("acs", "1")
	}
	w("userland_dfs", "0")
	w("tx_power", "100")
	w("max_clients", fmt.Sprint(maxSta))
	if ch > 0 {
		w("channel", fmt.Sprint(ch))
	}
	return AirPortVAP{Name: name, Parent: parent, SSID: l.SSID, Security: l.Security, Channel: ch, ChannelOf: follow, Tune: tune, Spec: spec, Head: h.String(), Tail: t.String(),
		Isolate: l.Isolate, TxPower: tx}
}

// UplinkChannel is the channel of the mesh uplink (the client VAP) on the radio
// of that band ("ng", "na"); 0 = no uplink there.
func (s *Snapshot) UplinkChannel(band string) int {
	for _, r := range s.Radios {
		if r.Band != band {
			continue
		}
		for _, v := range r.VAPs {
			if v.Mode == "Client" {
				return v.Channel
			}
		}
	}
	return 0
}

// APVAP is the running AP VAP with this interface name, or nil.
func (s *Snapshot) APVAP(iface string) *Radio {
	for i := range s.Radios {
		for j := range s.Radios[i].VAPs {
			if v := &s.Radios[i].VAPs[j]; v.Iface == iface && v.Mode == "Master" {
				return v
			}
		}
	}
	return nil
}

// ApplyAirPort makes the AirPort run exactly these VAPs (and none else in the
// daemon's name ranges). It returns the script's report, one line per VAP
// ("wlan10 same", "wlan10 started", "wlan10 removed", "wlan10 failed: why");
// the error is for a script that could not run or refused to (no seed file).
func (cl *Client) ApplyAirPort(vaps []AirPortVAP) ([]string, error) {
	if !cl.IsAirPort() {
		return nil, errors.New("not an AirPort base station")
	}
	if err := checkAirPortVAPs(vaps); err != nil {
		return nil, err
	}
	// The script is the stdin of a plain sh: the passphrases never reach a
	// command line, where ps would show them.
	out, err := cl.runStdin("sh", []byte(AirPortApplyScript(vaps)))
	lines := reportLines(out)
	if err != nil {
		return lines, fmt.Errorf("airport apply: %w", err)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "failed:") {
			return lines, errors.New(l)
		}
	}
	return lines, nil
}

func checkAirPortVAPs(vaps []AirPortVAP) error {
	seen := map[string]bool{}
	for _, v := range vaps {
		switch {
		case !apVAPName.MatchString(v.Name):
			return fmt.Errorf("refusing VAP name %q", v.Name)
		case seen[v.Name]:
			return fmt.Errorf("VAP %s twice", v.Name)
		case v.ChannelOf != "" && !apVAPName.MatchString(v.ChannelOf):
			return fmt.Errorf("%s: refusing to follow %q", v.Name, v.ChannelOf)
		case v.Parent != "bwl0" && v.Parent != "bwl1":
			return fmt.Errorf("%s: refusing parent %q", v.Name, v.Parent)
		case strings.ContainsAny(v.Head+v.Tail, "\x00\r"):
			return fmt.Errorf("%s: config has a NUL or a carriage return", v.Name)
		}
		seen[v.Name] = true
	}
	return nil
}

// apScriptHead reads the unit's sku and region and defines the helpers. It
// runs on the device's own sh, which has sed, sort, tail, cat, ps, ifconfig,
// brconfig, dd, ls, kill, sleep, mkdir and rm, and no grep, awk, head, tr or
// cut. Processes are tracked by pid file (ps ax cuts the command lines).
const apScriptHead = `PATH=/bin:/sbin:/usr/bin:/usr/sbin
umask 077
D=` + apRunDir + `
seed=` + apSeedFile + `
sku=; region=
if [ -f $seed ]; then
	while read l || [ -n "$l" ]; do
		case "$l" in sku=*) sku=${l#sku=};; region=*) region=${l#region=};; esac
	done < $seed
fi
case "$sku" in ""|*[!0-9A-Za-z]*) sku=;; esac
case "$region" in ""|*[!0-9A-Za-z]*) region=;; esac
if [ -z "$sku" ] || [ -z "$region" ]; then
	echo "failed: $seed is missing or has no sku= and region= lines"
	exit 0
fi
mkdir -p $D
alive() {
	p=$(cat $D/$1.pid 2>/dev/null)
	[ -n "$p" ] || return 1
	ps ax | {
		while read pa pr; do
			[ "$pa" = "$p" ] || continue
			case "$pr" in *hostapd*) exit 0;; esac
		done
		exit 1
	}
}
stop() {
	if alive $1; then
		kill $(cat $D/$1.pid) 2>/dev/null
		n=0
		while alive $1 && [ $n -lt 5 ]; do sleep 1; n=$((n + 1)); done
		alive $1 && kill -9 $(cat $D/$1.pid) 2>/dev/null
	fi
	rm -f $D/$1.pid $D/$1.acl # a VAP made again starts with an empty driver ACL
	brconfig bridge0 delete $1 >/dev/null 2>&1
	ifconfig $1 destroy >/dev/null 2>&1
}
chan() {
	ifconfig $1 2>/dev/null | sed -n 's/.* channel \([0-9][0-9]*\) (.*/\1/p'
}
running() {
	case "$(ifconfig $1 2>&1)" in *RUNNING*"status: running"*) return 0;; esac
	return 1
}
# ok VAP: it runs the config just written for it
ok() {
	[ "$(cat $D/$1.new)" = "$(cat $D/$1.want 2>/dev/null)" ] && alive $1 && running $1
}
# start VAP RADIO CHANNEL FIRST. CHANNEL is a channel as ifconfig takes it, "6"
# or "44:vht/80" (tune the radio: hostapd's channel= and width keys do not move
# it, ifconfig on the first VAP of an idle radio does),
# "auto" (the first VAP chooses, the others get the channel it is on) or "-"
# (the config has the channel of the mesh uplink, which alone tunes the radio).
start() {
	v=$1
	rm -f $D/$v.acl
	ifconfig $v create wlandev $2 wlanmode hostap >/dev/null 2>&1 || { echo "$v failed: ifconfig create"; rm -f $D/$v.new; return; }
	case $3 in auto|-) ;; *) ifconfig $v channel $3 >/dev/null 2>&1;; esac
	mv $D/$v.new $D/$v.want
	cat $D/$v.want > $D/$v.conf
	if [ $3 = auto ] && [ $v != $4 ]; then
		c=$(chan $4); [ -n "$c" ] && echo "channel=$c" >> $D/$v.conf
	fi
	/sbin/hostapd -t -F $D/$v.log $D/$v.conf > $D/$v.out 2>&1 < /dev/null &
	echo $! > $D/$v.pid
	n=0
	until running $v; do
		n=$((n + 1))
		[ $n -gt 15 ] && break
		sleep 1
	done
	if running $v; then
		brconfig bridge0 add $v >/dev/null 2>&1 && echo "$v started" || echo "$v failed: brconfig bridge0 add"
	else
		echo "$v failed: hostapd did not bring the VAP up (see $D/$v.out)"
		stop $v
		rm -f $D/$v.want $D/$v.conf
	fi
}
# radio RADIO PATTERN CHANNEL VAP...: make the radio run exactly these VAPs.
# All or nothing: when one of them has to start, stop or change, or the radio
# is on another channel, every VAP of the radio starts again. They are not
# independent in this driver: taking one down can leave the others down too
# (measured), and they all follow the radio's channel.
radio() {
	r=$1; pat=$2; ch=$3; shift 3
	dirty=0
	case $ch in auto|-) ;; *) [ "$(chan $r)" = "${ch%%:*}" ] || dirty=1;; esac
	[ "$(cat $D/$r.chan 2>/dev/null)" = "$ch" ] || dirty=1 # a width alone can change
	for v in "$@"; do ok $v || dirty=1; done
	have=
	for v in $(ifconfig -l); do
		case $v in $pat) have="$have $v"; case " $* " in *" $v "*) ;; *) dirty=1;; esac;; esac
	done
	if [ $dirty = 0 ]; then
		for v in "$@"; do rm -f $D/$v.new; echo "$v same"; done
		return
	fi
	for v in $have; do
		stop $v
		rm -f $D/$v.want $D/$v.conf
		case " $* " in *" $v "*) ;; *) rm -f $D/$v.new; echo "$v removed";; esac
	done
	echo "$ch" > $D/$r.chan
	for v in "$@"; do start $v $r $ch $1; done
}
# iso VAP FLAG: client isolation is the driver's AP bridging: "-apbridge" keeps
# the stations of the VAP apart, "apbridge" lets them talk (measured: wl
# ap_isolate reads 1 and 0). Set every time, on every VAP: it costs one call.
iso() {
	ifconfig $1 $2 >/dev/null 2>&1
}
# txpow RADIO VAP N: the transmit power is the radio's, set through any of its
# VAPs. N is dBm; "auto" puts back the power the radio had before the first
# change (kept in RADIO.txpower0, read from ifconfig). A number above that
# original is cut to it: the driver takes a higher target and does nothing
# useful with it.
# tenths N: dBm as an integer of tenths ("18.5" -> 185, "19" -> 190)
tenths() {
	echo "$1" | sed -e 's/^\([0-9]*\)\.\(.\).*/\1\2/;t' -e 's/$/0/'
}
txpow() {
	r=$1; v=$2; n=$3
	running $v || return
	cur=$(ifconfig $v 2>/dev/null | sed -n 's/.*[[:space:]]txpower \([0-9][0-9.]*\).*/\1/p')
	[ -n "$cur" ] || return
	f=$D/$r.txpower0
	if [ "$n" = auto ]; then
		[ -f $f ] || return
		n=$(cat $f)
	else
		[ -f $f ] || echo "$cur" > $f
		[ $(tenths $n) -gt $(tenths $(cat $f)) ] && n=$(cat $f)
	fi
	[ "${cur%.0}" = "${n%.0}" ] && return
	if ifconfig $v txpower $n >/dev/null 2>&1 || ifconfig $v txpower ${n%.*} >/dev/null 2>&1; then
		echo "$r txpower $n dBm"
	else
		echo "$r txpower $n dBm: ifconfig refused it"
	fi
}
`

// AirPortApplyScript is the shell script ApplyAirPort runs. Each config goes in
// through a quoted heredoc, so nothing in an SSID or a passphrase is expanded;
// the delimiter is not a line the config can contain (every config line has an
// "=", and the delimiter is lengthened if one ever equals it). Then one radio
// call per radio, with or without VAPs: it is what removes the ones nobody
// wants, and only names in the radio's own range are ever matched.
func AirPortApplyScript(vaps []AirPortVAP) string {
	var b strings.Builder
	b.WriteString(apScriptHead)
	for i, v := range vaps {
		fmt.Fprintf(&b, "{\ncat <<'%s'\n%s%s\n", delim(v.Head, fmt.Sprintf("UOW_EOF_%dA", i)), v.Head, delim(v.Head, fmt.Sprintf("UOW_EOF_%dA", i)))
		b.WriteString("echo \"sku=$sku\"\necho \"region=$region\"\n")
		fmt.Fprintf(&b, "cat <<'%s'\n%s%s\n", delim(v.Tail, fmt.Sprintf("UOW_EOF_%dB", i)), v.Tail, delim(v.Tail, fmt.Sprintf("UOW_EOF_%dB", i)))
		fmt.Fprintf(&b, "} > $D/%s.new\n", v.Name)
	}
	for _, r := range []struct{ parent, pat string }{{"bwl1", "wlan1[0-7]"}, {"bwl0", "wlan2[0-7]"}} {
		ch, names := "-", ""
		for _, v := range vaps {
			if v.Parent != r.parent {
				continue
			}
			if names == "" { // the radio's first VAP says how its channel is set
				switch {
				case v.Tune:
					ch = v.Spec
				case v.Channel == 0:
					ch = "auto"
				}
			}
			names += " " + v.Name
		}
		fmt.Fprintf(&b, "radio %s '%s' %s%s\n", r.parent, r.pat, ch, names)
	}
	// after the radios are settled: what is not in a hostapd config
	for _, v := range vaps {
		flag := "apbridge"
		if v.Isolate {
			flag = "-apbridge"
		}
		fmt.Fprintf(&b, "iso %s %s\n", v.Name, flag)
	}
	done := map[string]bool{}
	for _, v := range vaps {
		if done[v.Parent] {
			continue
		}
		done[v.Parent] = true
		p := "auto"
		if v.TxPower > 0 {
			p = fmt.Sprint(v.TxPower)
		}
		fmt.Fprintf(&b, "txpow %s %s %s\n", v.Parent, v.Name, p)
	}
	b.WriteString("exit 0\n")
	return b.String()
}

// delim lengthens base until no line of text equals it.
func delim(text, base string) string {
	for {
		clash := false
		for _, l := range strings.Split(text, "\n") {
			if l == base {
				clash = true
				break
			}
		}
		if !clash {
			return base
		}
		base += "_"
	}
}
