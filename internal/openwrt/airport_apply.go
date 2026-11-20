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
	// Head is the hostapd config up to and including ifparent=; Tail the rest.
	// The unit's own sku= and region= lines go between them, and only the
	// device knows them (apSeedFile).
	Head, Tail string
}

// Config is the whole hostapd config, with the unit's regulatory ids.
func (v AirPortVAP) Config(sku, region string) string {
	return v.Head + "sku=" + sku + "\nregion=" + region + "\n" + v.Tail
}

// String is the log line: no passphrase in it.
func (v AirPortVAP) String() string {
	ch := "auto channel"
	if v.Channel > 0 {
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
	isolate, macs, pmf := 0, 0, 0

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
			vaps = append(vaps, v)
			if l.Isolate {
				isolate++
			}
			if l.MACPolicy != "" && l.MACPolicy != "disable" {
				macs++
			}
			if l.PMF == 2 {
				pmf++
			}
		}
	}

	var width, power []string
	for _, r := range w.Radios {
		if !hasVAP[r.Name] {
			continue
		}
		if r.Width != 0 && !(r.Band == "2g" && r.Width == 20) {
			width = append(width, fmt.Sprintf("%s %d MHz", r.Name, r.Width))
		}
		if r.TxPower > 0 {
			power = append(power, fmt.Sprintf("%s %d dBm", r.Name, r.TxPower))
		}
		if r.Band == "5g" && uplink5 > 0 && r.Channel > 0 && r.Channel != uplink5 {
			notes = append(notes, fmt.Sprintf("radio %s: channel %d requested, but the 5 GHz radio follows the mesh uplink on channel %d", r.Name, r.Channel, uplink5))
		}
	}
	if len(width) > 0 {
		notes = append(notes, fmt.Sprintf("channel width not applied (%s): the radio keeps its own (with a mesh uplink, the uplink's)", strings.Join(width, ", ")))
	}
	if len(power) > 0 {
		notes = append(notes, fmt.Sprintf("transmit power not applied (%s): hostapd's tx_power has no effect here", strings.Join(power, ", ")))
	}
	if isolate > 0 {
		notes = append(notes, fmt.Sprintf("client isolation requested on %d WLAN(s): not supported on this platform", isolate))
	}
	if macs > 0 {
		notes = append(notes, fmt.Sprintf("MAC filter requested on %d WLAN(s): not supported on this platform", macs))
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
	name := "wlan" + suffix

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
	w("dtim", "3")
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
	w("max_clients", "64")
	if ch > 0 {
		w("channel", fmt.Sprint(ch))
	}
	return AirPortVAP{Name: name, Parent: parent, SSID: l.SSID, Security: l.Security, Channel: ch, ChannelOf: follow, Tune: tune, Head: h.String(), Tail: t.String()}
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
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
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
	rm -f $D/$1.pid
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
# start VAP RADIO CHANNEL FIRST. CHANNEL is a number (tune the radio: hostapd's
# channel= does not move it, ifconfig on the first VAP of an idle radio does),
# "auto" (the first VAP chooses, the others get the channel it is on) or "-"
# (the config has the channel of the mesh uplink, which alone tunes the radio).
start() {
	v=$1
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
	case $ch in auto|-) ;; *) [ "$(chan $r)" = "$ch" ] || dirty=1;; esac
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
	for v in "$@"; do start $v $r $ch $1; done
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
					ch = fmt.Sprint(v.Channel)
				case v.Channel == 0:
					ch = "auto"
				}
			}
			names += " " + v.Name
		}
		fmt.Fprintf(&b, "radio %s '%s' %s%s\n", r.parent, r.pat, ch, names)
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
