// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// The controller's rate limits (user groups) on an AirPort. Measured on the
// unit (gaps.md, #15): a pf altq queue on a VAP with every packet in its
// default class caps that VAP exactly and costs nothing to anyone else. A limit
// per client does not work for the download (pf sees frames the mesh uplink
// aggregated, and half the bytes leave the class), and one for the upload needs
// a queue on the uplink, which slows every client. So the download limit is a
// cap on the WLAN's VAP, shared by its clients.
//
// The upload of a WLAN on a VLAN leaves by vlanN, an interface only that VLAN's
// WLANs use, and a queue there costs nobody else (measured on a wired uplink: a
// 400 kbit/s cap gave 49998 B/s). So that upload limit is a cap on vlanN, shared
// by the clients of every WLAN on the VLAN. A WLAN on the LAN has no interface
// of its own on the way out (a queue on bcmeth0 cut the unit off the network,
// measured), and its upload limit is noted as not applied.

// apPFMaxKbps is the parent queue's bandwidth (1Gb): a child may not ask for
// more, and a cap that high is no cap on this unit's radios anyway.
const apPFMaxKbps = 1000000

// PFLimit is the download cap of one VAP (VAP, DownKbps), or the upload cap of
// one VLAN (VAP empty, VLAN, UpKbps).
type PFLimit struct {
	VAP      string // wlan10..17, wlan20..27
	DownKbps int
	VLAN     int // 2..4094: the cap goes on vlanN
	UpKbps   int
}

// AirPortLimits maps the controller's rate limits to the VAPs of the last apply
// (by the controller's devname, VAP.Devname) and returns the caps to set, in
// the order of vaps, then the upload caps by VLAN, with the notes for the log:
// once for the shared download limit, once for the upload caps, once for the
// upload limits that are ignored.
func AirPortLimits(lims []translate.VAPLimit, vaps []AirPortVAP) ([]PFLimit, []string) {
	byDev := map[string]AirPortVAP{}
	for _, v := range vaps {
		if v.Devname != "" {
			byDev[v.Devname] = v
		}
	}
	down := map[string]int{} // VAP -> kbps, the lowest when a devname has two
	upSSID := map[string]bool{}
	up := map[int]int{} // VLAN -> kbps, the lowest of its WLANs
	var notes, noVAP, tooBig []string
	for _, l := range lims {
		v, ok := byDev[l.Devname]
		if !ok {
			noVAP = append(noVAP, l.Devname)
			continue
		}
		switch {
		case l.UpKbps <= 0:
		case v.VLAN == 0:
			upSSID[fmt.Sprintf("%q", v.SSID)] = true
		case l.UpKbps >= apPFMaxKbps:
		case up[v.VLAN] == 0 || l.UpKbps < up[v.VLAN]:
			up[v.VLAN] = l.UpKbps
		}
		switch {
		case l.DownKbps <= 0:
		case l.DownKbps >= apPFMaxKbps:
			tooBig = append(tooBig, fmt.Sprintf("%q", v.SSID))
		case down[v.Name] == 0 || l.DownKbps < down[v.Name]:
			down[v.Name] = l.DownKbps
		}
	}
	var out []PFLimit
	for _, v := range vaps {
		if k := down[v.Name]; k > 0 {
			out = append(out, PFLimit{VAP: v.Name, DownKbps: k})
		}
	}
	if len(out) > 0 {
		notes = append(notes, "download limit: a cap on the WLAN's VAP (pf altq), shared by its clients: none gets more than the limit, and each gets less when several are active")
	}
	var ids []int
	for id := range up {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		out = append(out, PFLimit{VLAN: id, UpKbps: up[id]})
	}
	if len(ids) > 0 {
		notes = append(notes, "upload limit: a cap on the VLAN's interface (pf altq on vlanN), shared by the clients of every WLAN on that VLAN")
	}
	if len(upSSID) > 0 {
		notes = append(notes, fmt.Sprintf("upload limit ignored on WLAN(s) %s: it needs a queue on an interface only that WLAN's traffic leaves by, and only a WLAN on a VLAN has one", sortedKeys(upSSID)))
	}
	if len(tooBig) > 0 {
		sort.Strings(tooBig)
		notes = append(notes, fmt.Sprintf("download limit of %s is 1 Gbit/s or more: not applied (no cap)", strings.Join(tooBig, ", ")))
	}
	if len(noVAP) > 0 {
		sort.Strings(noVAP)
		notes = append(notes, fmt.Sprintf("rate limit on %s: no VAP of the daemon on this unit", strings.Join(noVAP, ", ")))
	}
	return out, notes
}

func sortedKeys(m map[string]bool) string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	sort.Strings(k)
	return strings.Join(k, ", ")
}

// Client isolation across radios (gaps.md, #5). The driver's apbridge flag
// keeps the clients of one VAP apart, but the two VAPs of a WLAN (one per
// radio) share a bridge, and the bridge forwards between them. pf stops that
// without knowing an address: with "ipf" on the bridge, pf sees the bridged
// frames, on input too (its input hook DOES run on the VAPs). Measured on the
// unit: "pass all" plus "block out on VAP all" is not enough. pf keeps a state
// per direction, so a packet that came in from the uplink has none on its way
// out of the VAP: a fresh client got no DHCP answer and nothing opened from the
// wired side reached a client. So the packet is tagged on the way in and the
// block lets the tag through:
//
//	pass in all tag UOWUP                  everything (the uplink side)
//	pass in on VAP all tag UOWSTA          every VAP of the daemon, isolated or
//	                                       not: it overrides UOWUP, so a frame
//	                                       from any client has no UOWUP
//	block out on ISO all                   then, for each isolated VAP: block,
//	pass out on ISO tagged UOWUP           let what came from the uplink in,
//	pass out on ISO ... port 67 to 68      and DHCP replies and the three
//	pass out on ISO inet6 ... icmp6        ICMPv6 types IPv6 needs: a tag set
//	                                       on input is lost on broadcast frames
//	                                       (the bridge copies them per member)
//
// The last matching rule wins, hence the order. ARP is not IP and is not
// filtered. Wired hosts stay reachable, both ways. It needs pf, so it shares the
// ruleset (and the switch) of the download limits.

// PFIso is one isolated VAP and the bridge it is in.
type PFIso struct {
	VAP    string // wlan10..17, wlan20..27
	Bridge string // bridge0, or the daemon's bridgeN (2..4094)
}

// apIsoNote is the plan note, once per apply when a VAP is isolated.
const apIsoNote = "client isolation: clients of an isolated WLAN cannot reach each other, on one radio (driver) or across radios (pf on the bridge); hosts on the wired side stay reachable"

// AirPortIsolation lists the isolated VAPs (Isolate: the WLAN's own flag, or a
// guest WLAN) in the order of vaps, with the note for the log.
func AirPortIsolation(vaps []AirPortVAP) ([]PFIso, []string) {
	var out []PFIso
	for _, v := range vaps {
		if v.Isolate {
			out = append(out, PFIso{VAP: v.Name, Bridge: v.Bridge()})
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, []string{apIsoNote}
}

// apBridgeName is a bridge the daemon may set the filter on: the LAN bridge or
// one of its own bridgeN, never anything else.
func apBridgeName(b string) bool {
	if b == "bridge0" {
		return true
	}
	n, ok := strings.CutPrefix(b, "bridge")
	if !ok || n == "" || n[0] == '0' || len(n) > 4 {
		return false
	}
	id := 0
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
		id = id*10 + int(c-'0')
	}
	return id >= 2 && id <= 4094
}

// apPFHead builds the ruleset in $T from the capped VAPs that run (an altq
// needs its interface) and the isolated ones, then loads it only when
// something is to change. pf is the unit's own and was off, with a stored
// ruleset of "block drop all": our ruleset (queues, "pass all", then, when a
// VAP is isolated, the tagging and blocking rules above, then the guest rules:
// the last matching rule wins) is loaded BEFORE pf is enabled, and pf is never enabled after a failed
// load.
// pf.conf is the proof that the ruleset in force is ours, so with nothing
// wanted pf is untouched unless that file exists.
//
// A VAP that was destroyed loses its queue or its block, and pf does not say
// so: VAP.pfq marks "the ruleset was loaded with this VAP in it" and the apply
// script removes it when it stops or starts a VAP (as it does VAP.acl). A
// running VAP named in the ruleset (capped, isolated or tagged) without a
// marker makes the ruleset load
// again; parsing "pfctl -sq" instead would depend on a format never seen on
// this unit.
//
// The bridges of the isolated VAPs get "brconfig BRIDGE ipf" (brsync). The
// flag is cleared only where BRIDGE.ipf says the daemon set it: one that was
// on before is never ours to turn off.
const apPFHead = apPATH + `umask 077
D=` + apRunDir + `
mkdir -p $D
F=$D/pf.conf
T=$D/pf.new
n=0
w=
: > $T
lim() {
	case "$(ifconfig $1 2>&1)" in *RUNNING*"status: running"*) ;; *) return;; esac
	echo "altq on $1 hfsc bandwidth 1Gb queue { lim_$1 }" >> $T
	echo "queue lim_$1 bandwidth $2 hfsc(default upperlimit $2)" >> $T
	n=$((n + 1))
	w="$w $1"
}
# ulim VLANIF RATE: the upload cap of the WLANs on that VLAN, a queue on vlanN
nu=0
ulim() {
	ifconfig $1 >/dev/null 2>&1 || return
	echo "altq on $1 hfsc bandwidth 1Gb queue { up_$1 }" >> $T
	echo "queue up_$1 bandwidth $2 hfsc(default upperlimit $2)" >> $T
	n=$((n + 1))
	nu=$((nu + 1))
	w="$w $1"
}
m=0
wi=
wb=
ws=
wg=
gres=
gpre=
# sta VAP: a running VAP of the daemon, whose frames are tagged as a client's
# (used only when a VAP is isolated)
sta() {
	case "$(ifconfig $1 2>&1)" in *RUNNING*"status: running"*) ws="$ws $1";; esac
}
# blk VAP BRIDGE: the VAP is isolated; its block rules come after "pass all"
blk() {
	case "$(ifconfig $1 2>&1)" in *RUNNING*"status: running"*) ;; *) return;; esac
	m=$((m + 1))
	wi="$wi $1"
	wb="$wb $2"
}
# gst VAP: a guest VAP that runs; gr / gp RANGE: a restricted / pre-allowed range
# (the guest policy, airport_guest.go); the rules come after the isolation lines
gst() {
	case "$(ifconfig $1 2>&1)" in *RUNNING*"status: running"*) wg="$wg $1";; esac
}
gr() { gres="$gres $1"; }
gp() { gpre="$gpre $1"; }
# brsync: the packet filter is on in every bridge of $wb and, of the bridges
# the daemon set it on (BRIDGE.ipf), only in those
brsync() {
	for b in $wb; do
		ifconfig $b >/dev/null 2>&1 || continue
		case "$(brconfig $b 2>&1)" in *"ipfilter enabled"*) continue;; esac
		if brconfig $b ipf >/dev/null 2>&1; then
			: > $D/$b.ipf
			echo "$b: ipf on"
		else
			echo "$b failed: brconfig ipf"
		fi
	done
	for f in $D/bridge*.ipf; do
		[ -e $f ] || continue
		b=${f##*/}
		b=${b%.ipf}
		case " $wb " in *" $b "*) continue;; esac
		case ${b#bridge} in ""|*[!0-9]*) rm -f $f; continue;; esac
		if ifconfig $b >/dev/null 2>&1; then
			case "$(brconfig $b 2>&1)" in *"ipfilter enabled"*)
				if brconfig $b -ipf >/dev/null 2>&1; then
					echo "$b: ipf off"
				else
					echo "$b failed: brconfig -ipf"
					continue
				fi;;
			esac
		fi
		rm -f $f
	done
}
`

const apPFTail = `if [ $n -eq 0 ] && [ $m -eq 0 ]; then
	rm -f $T
	brsync
	if [ -e $F ]; then
		pfctl -d >/dev/null 2>&1
		pfctl -F all >/dev/null 2>&1
		rm -f $F $D/wlan1?.pfq $D/wlan2?.pfq $D/vlan[0-9]*.pfq
		echo "pf: off"
	fi
	exit 0
fi
echo "pass all" >> $T
if [ $m -gt 0 ]; then
	echo "pass in all tag UOWUP" >> $T
	for v in $ws; do echo "pass in on $v all tag UOWSTA" >> $T; done
	for v in $wi; do
		echo "block out on $v all" >> $T
		echo "pass out on $v tagged UOWUP" >> $T
		echo "pass out on $v inet proto udp from any port 67 to any port 68" >> $T
		echo "pass out on $v inet6 proto icmp6 all icmp6-type { routeradv, neighbrsol, neighbradv }" >> $T
	done
	for v in $wg; do
		echo "pass in quick on $v inet proto udp from any port 68 to any port 67 tag UOWSTA" >> $T
		for r in $gpre; do echo "pass in quick on $v inet from any to $r tag UOWSTA" >> $T; done
		for r in $gres; do echo "block in quick on $v inet from any to $r" >> $T; done
		echo "block in quick on $v inet6 all" >> $T
	done
else
	ws=
fi
need=
[ "$(cat $T)" = "$(cat $F 2>/dev/null)" ] || need=1
case "$(pfctl -s info 2>&1)" in *"Status: Enabled"*) ;; *) need=1;; esac
for v in $w $wi $ws; do [ -e $D/$v.pfq ] || need=1; done
if [ -z "$need" ]; then
	rm -f $T
	brsync
	exit 0
fi
if ! r=$(pfctl -f $T 2>&1); then
	echo "pf failed: load: $(echo "$r" | sed -n 1p)"
	rm -f $T
	exit 0
fi
mv $T $F
rm -f $D/wlan1?.pfq $D/wlan2?.pfq $D/vlan[0-9]*.pfq
for v in $w $wi $ws; do : > $D/$v.pfq; done
if ! r=$(pfctl -e 2>&1); then
	case "$r" in
	*"already enabled"*) ;;
	*) echo "pf failed: enable: $(echo "$r" | sed -n 1p)"; rm -f $D/wlan1?.pfq $D/wlan2?.pfq $D/vlan[0-9]*.pfq; exit 0;;
	esac
fi
brsync
s=
[ $n -gt $nu ] && s="$((n - nu)) limited VAP(s)"
[ $nu -gt 0 ] && s="${s:+$s, }$nu VLAN upload cap(s)"
[ $m -gt 0 ] && s="${s:+$s, }$m isolated VAP(s)"
echo "pf: $s"
exit 0
`

// AirPortLimitsScript is the script for the download caps alone (see
// AirPortPFScript).
func AirPortLimitsScript(lims []PFLimit) (string, error) {
	return AirPortPFScript(lims, nil, nil)
}

// AirPortVAPNames is the name of every VAP of the last apply, in order: the
// VAPs whose frames pf tags as a client's when a VAP is isolated.
func AirPortVAPNames(vaps []AirPortVAP) []string {
	var out []string
	for _, v := range vaps {
		out = append(out, v.Name)
	}
	return out
}

// AirPortPFScript is the script that makes pf cap these VAPs and isolate those
// (see apPFHead). It prints one line per change and nothing when pf is already
// as wanted. Only the daemon's own VAP and bridge names and positive whole
// rates reach the shell.
//
// all is every VAP of the daemon (AirPortVAPNames); the script keeps the ones
// that run, and the isolated ones are in it whether or not the caller listed them.
func AirPortPFScript(lims []PFLimit, iso []PFIso, all []string) (string, error) {
	return AirPortPFScriptGuest(lims, iso, all, PFGuest{})
}

// AirPortPFScriptGuest is AirPortPFScript with the guest policy of the guest
// VAPs (airport_guest.go): their lines come last, after the isolation ones.
func AirPortPFScriptGuest(lims []PFLimit, iso []PFIso, all []string, g PFGuest) (string, error) {
	if err := g.check(iso); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(apPFHead)
	seen := map[string]bool{}
	seenUp := map[int]bool{}
	for _, l := range lims {
		if l.VAP == "" {
			switch {
			case l.VLAN < apMinVLAN || l.VLAN > apMaxVLAN:
				return "", fmt.Errorf("refusing VLAN %d for an upload cap", l.VLAN)
			case seenUp[l.VLAN]:
				return "", fmt.Errorf("VLAN %d twice", l.VLAN)
			case l.UpKbps <= 0 || l.UpKbps >= apPFMaxKbps:
				return "", fmt.Errorf("vlan%d: refusing rate %d kbit/s", l.VLAN, l.UpKbps)
			}
			seenUp[l.VLAN] = true
			fmt.Fprintf(&b, "ulim vlan%d %dKb\n", l.VLAN, l.UpKbps)
			continue
		}
		switch {
		case !apVAPName.MatchString(l.VAP):
			return "", fmt.Errorf("refusing VAP name %q", l.VAP)
		case seen[l.VAP]:
			return "", fmt.Errorf("VAP %s twice", l.VAP)
		case l.DownKbps <= 0 || l.DownKbps >= apPFMaxKbps:
			return "", fmt.Errorf("%s: refusing rate %d kbit/s", l.VAP, l.DownKbps)
		}
		seen[l.VAP] = true
		fmt.Fprintf(&b, "lim %s %dKb\n", l.VAP, l.DownKbps)
	}
	seenIso := map[string]bool{}
	for _, x := range iso {
		switch {
		case !apVAPName.MatchString(x.VAP):
			return "", fmt.Errorf("refusing VAP name %q", x.VAP)
		case !apBridgeName(x.Bridge):
			return "", fmt.Errorf("%s: refusing bridge name %q", x.VAP, x.Bridge)
		case seenIso[x.VAP]:
			return "", fmt.Errorf("isolated VAP %s twice", x.VAP)
		}
		seenIso[x.VAP] = true
		fmt.Fprintf(&b, "blk %s %s\n", x.VAP, x.Bridge)
	}
	if len(iso) > 0 {
		seenSta := map[string]bool{}
		names := append(append([]string{}, all...), isoNames(iso)...)
		for _, n := range names {
			if !apVAPName.MatchString(n) {
				return "", fmt.Errorf("refusing VAP name %q", n)
			}
			if !seenSta[n] {
				seenSta[n] = true
				fmt.Fprintf(&b, "sta %s\n", n)
			}
		}
	}
	for _, v := range g.VAPs {
		fmt.Fprintf(&b, "gst %s\n", v)
	}
	for _, r := range g.PreAllow {
		fmt.Fprintf(&b, "gp %s\n", r)
	}
	for _, r := range g.Restricted {
		fmt.Fprintf(&b, "gr %s\n", r)
	}
	b.WriteString(apPFTail)
	return b.String(), nil
}

func isoNames(iso []PFIso) []string {
	var out []string
	for _, x := range iso {
		out = append(out, x.VAP)
	}
	return out
}

// SyncAirPortPF makes pf cap what lims say, isolate what iso says and hold the
// guest VAPs to g; all is every VAP of the last apply. It is idempotent and cheap (no pfctl call at all
// when nothing is wanted and we never loaded a ruleset), so call it after every
// apply and now and then. It returns one line per change.
func (cl *Client) SyncAirPortPF(lims []PFLimit, iso []PFIso, all []string, g PFGuest) ([]string, error) {
	if !cl.IsAirPort() {
		return nil, errors.New("not an AirPort base station")
	}
	s, err := AirPortPFScriptGuest(lims, iso, all, g)
	if err != nil {
		return nil, err
	}
	out, err := cl.runStdin("sh", []byte(s))
	lines := reportLines(out)
	if err != nil {
		return lines, fmt.Errorf("airport limits: %w", err)
	}
	for _, l := range lines {
		if strings.Contains(l, " failed: ") {
			return lines, errors.New(l)
		}
	}
	return lines, nil
}
