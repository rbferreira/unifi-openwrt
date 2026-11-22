// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// The network of a WLAN on an AirPort. A WLAN on the LAN is a VAP in bridge0. A
// WLAN on VLAN N is a VAP in bridgeN together with vlanN, an 802.1Q interface on
// the mesh uplink (measured, gaps.md #13: the Pi got an address on the VLAN from
// the gateway). The unit has no per-VAP VLAN tag, so the uplink is the only way
// out; with a wired uplink the tagging interface is not known, and a VLAN WLAN
// stays off.
//
// The daemon creates only vlanN and bridgeN (N = 2..4094), keeps their names in
// apRunDir/nets, and destroys only what is listed there. The uplink interface is
// named as vlanN's parent and never touched otherwise; bridge0's other members
// are never touched.

const (
	apMinVLAN = 2
	apMaxVLAN = 4094
)

// apUpIfRE is what the uplink's interface may look like: a net80211 wlanN, and not
// one of the daemon's own (wlan10..17, wlan20..27).
var apUpIfRE = regexp.MustCompile(`^wlan[0-9]{1,3}$`)

func apUpIfOK(name string) bool {
	return apUpIfRE.MatchString(name) && !apVAPName.MatchString(name)
}

// APNet is what the network of a WLAN depends on, besides the WLAN itself.
type APNet struct {
	// UpIf is the mesh uplink's interface ("" = no wireless uplink: wired, or
	// none yet), the parent of every vlanN.
	UpIf string
	// Portal: the controller's guest portal is on (redirector), so a guest WLAN
	// expects the AP to hold back unauthorised guests.
	Portal bool
	// Restricted and PreAllow are the guest policy's ranges (translate.Guest),
	// checked before a guest WLAN gets a VAP.
	Restricted, PreAllow []string
}

// Bridge is the bridge the VAP belongs in.
func (v AirPortVAP) Bridge() string {
	if v.VLAN > 0 {
		return fmt.Sprintf("bridge%d", v.VLAN)
	}
	return "bridge0"
}

// UplinkIface is the interface of the mesh uplink (the client VAP), "" when the
// unit has none or the name is not one this code would pass to a shell.
func (s *Snapshot) UplinkIface() string {
	for _, r := range s.Radios {
		for _, v := range r.VAPs {
			if v.Mode == "Client" && apUpIfOK(v.Iface) {
				return v.Iface
			}
		}
	}
	return ""
}

// apNetFor decides the network of one WLAN: its VLAN (0 = the LAN), whether
// client isolation is forced on, and skip, the reason the WLAN gets no VAP ("" =
// it does).
//
// Guest WLANs: the controller's guest policy is the authorisation portal
// (redirector) and the restricted ranges (ipset guest_restricted). The ranges
// are pf rules on the guest VAP (airport_guest.go); the portal is not possible
// (pf cannot match a MAC address). So a guest WLAN runs with isolation forced
// on, on a VLAN or on the LAN, but not with the portal, and on the LAN only when
// a restricted range covers private space (else guests would reach the LAN).
func apNetFor(l translate.WantedWLAN, n APNet) (vlan int, isolate bool, skip string) {
	if l.Guest {
		if n.Portal {
			return 0, false, "guest network with the captive portal: holding back unauthorised guests needs the AP to match a client's MAC address, and pf cannot"
		}
		if why := apGuestSkip(l, n); why != "" {
			return 0, false, why
		}
	}
	switch {
	case l.VLAN == 0:
		return 0, l.Guest, ""
	case l.VLAN < apMinVLAN || l.VLAN > apMaxVLAN:
		return 0, false, fmt.Sprintf("VLAN %d is not a usable id (%d to %d)", l.VLAN, apMinVLAN, apMaxVLAN)
	case n.UpIf == "":
		return 0, false, fmt.Sprintf("VLAN %d needs the mesh uplink to tag on, and this unit has none (the tagging interface of a wired uplink is not known yet)", l.VLAN)
	case !apUpIfOK(n.UpIf):
		return 0, false, fmt.Sprintf("VLAN %d: uplink interface %q is not usable", l.VLAN, n.UpIf)
	}
	return l.VLAN, l.Guest, ""
}

// apNetFuncs are the shell helpers of the network step, after apScriptHead
// (which has stop, running, $D and $started).
//
// Formats read, as measured or expected on NetBSD 6 (gaps.md #13):
//
//	ifconfig vlan77   ->  "\tvlan: 77 parent interface: wlan9" (nothing when it has no parent)
//	brconfig bridge77 ->  members as "\t\twlan5 flags=3<LEARNING,DISCOVER>"; the
//	                      address cache lines start with a MAC, the header with "name:"
const apNetFuncs = `# members BRIDGE: the interfaces in it, one per line
members() {
	brconfig $1 2>/dev/null | while read a b c; do
		case $a in
		*:) ;;
		*) case $b in flags=*) echo $a;; esac;;
		esac
	done
}
# inbr BRIDGE IF: is IF a member
inbr() {
	for bm in $(members $1); do [ "$bm" = "$2" ] && return 0; done
	return 1
}
# unbridge VAP KEEP: take the VAP out of every bridge but KEEP ("-" = all); sets moved
unbridge() {
	for ub in $(ifconfig -l); do
		case $ub in
		bridge*) [ $ub = $2 ] || { inbr $ub $1 && brconfig $ub delete $1 >/dev/null 2>&1 && moved=1; };;
		esac
	done
}
# own IF / owned IF / disown IF: the list of what the daemon created ($D/nets)
owned() {
	[ -f $D/nets ] || return 1
	while read o || [ -n "$o" ]; do [ "$o" = "$1" ] && return 0; done < $D/nets
	return 1
}
own() {
	owned $1 || echo $1 >> $D/nets
}
disown() {
	sed "/^$1\$/d" $D/nets > $D/nets.t && mv $D/nets.t $D/nets
}
# vlinfo VLAN: "ID PARENT" as the interface says, nothing when it has no parent
vlinfo() {
	ifconfig $1 2>/dev/null | sed -n 's/^[[:space:]]*vlan: \([0-9][0-9]*\) parent[A-Za-z ]*: \([^ ]*\).*$/\1 \2/p'
}
# mkvlan ID UPIF: vlanID exists, tagged ID on UPIF, up. A vlan that lost its
# parent (the boot script made the uplink again) or has another one is made again,
# but only when it is the daemon's own.
mkvlan() {
	vl=vlan$1
	if ifconfig $vl >/dev/null 2>&1; then
		if [ "$(vlinfo $vl)" = "$1 $2" ]; then
			ifconfig $vl up >/dev/null 2>&1
			return 0
		fi
		owned $vl || { echo "$vl exists and is not the daemon's"; return 1; }
		brconfig bridge$1 delete $vl >/dev/null 2>&1
		ifconfig $vl destroy >/dev/null 2>&1
		msg="$vl repaired (parent $2)"
	else
		msg="$vl created"
	fi
	ifconfig $vl create >/dev/null 2>&1 || return 1
	own $vl
	if ifconfig $vl vlan $1 vlanif $2 >/dev/null 2>&1 && ifconfig $vl up >/dev/null 2>&1; then
		echo "$msg"
		return 0
	fi
	ifconfig $vl destroy >/dev/null 2>&1 && disown $vl
	return 1
}
# mkbridge BRIDGE: it exists (one that is there already is used, never destroyed)
mkbridge() {
	ifconfig $1 >/dev/null 2>&1 && return 0
	ifconfig $1 create >/dev/null 2>&1 || return 1
	own $1
	echo "$1 created"
}
# netfail VAP WHAT: a VAP that cannot be put in its network must not stay on the
# air in another one. Stopped; the next apply starts it again.
netfail() {
	echo "$1 failed: $2"
	stop $1
	rm -f $D/$1.want $D/$1.conf
}
# net VAP BRIDGE [ID UPIF]: the VAP is in BRIDGE and in no other; with ID, BRIDGE
# holds vlanID on UPIF. Idempotent: it prints only what it changed.
net() {
	nv=$1; nb=$2
	running $nv || return
	moved=0
	if [ -n "$3" ]; then
		# no uplink now (it is made again, or gone): the VAP keeps off every
		# bridge, off the LAN above all, until it is back
		case " $(ifconfig -l) " in *" $4 "*) ;; *) unbridge $nv -; return;; esac
		mkvlan $3 $4 || { netfail $nv "vlan$3 on the uplink"; return; }
		mkbridge $nb || { netfail $nv "ifconfig $nb create"; return; }
		inbr $nb vlan$3 || brconfig $nb add vlan$3 >/dev/null 2>&1 || { netfail $nv "brconfig $nb add vlan$3"; return; }
	fi
	unbridge $nv $nb
	if ! inbr $nb $nv; then
		brconfig $nb add $nv >/dev/null 2>&1 || { netfail $nv "brconfig $nb add"; return; }
		moved=1
	fi
	if [ -n "$3" ]; then
		brconfig $nb up >/dev/null 2>&1 || { netfail $nv "brconfig $nb up"; return; }
	fi
	if [ $moved = 1 ]; then
		case " $started " in *" $nv "*) ;; *) echo "$nv bridged $nb";; esac
	fi
}
# netgc ID...: remove the vlans and bridges of the daemon's list that are not
# for one of these ids (bridges first, with their members)
netgc() {
	[ -f $D/nets ] || return 0
	list=$(cat $D/nets)
	for pass in bridge vlan; do
		for n in $list; do
			case $n in
			$pass[0-9]*) id=${n#$pass};;
			*) continue;;
			esac
			case $id in
			*[!0-9]*) continue;;
			esac
			[ $id -ge 2 ] && [ $id -le 4094 ] || continue # never bridge0, never a made-up name
			case " $* " in
			*" $id "*) continue;;
			esac
			if [ $pass = bridge ]; then
				for m in $(members $n); do brconfig $n delete $m >/dev/null 2>&1; done
				ifconfig $n down >/dev/null 2>&1
			fi
			if ifconfig $n destroy >/dev/null 2>&1 || ! ifconfig $n >/dev/null 2>&1; then
				disown $n
				echo "$n removed"
			else
				echo "$n failed: ifconfig destroy"
			fi
		done
	done
}
`

// apNetCalls is the network step for these VAPs, then the clean-up of the
// VLANs nobody wants any more.
func apNetCalls(vaps []AirPortVAP) string {
	var b strings.Builder
	ids := map[int]bool{}
	for _, v := range vaps {
		if v.VLAN > 0 {
			ids[v.VLAN] = true
			fmt.Fprintf(&b, "net %s %s %d %s\n", v.Name, v.Bridge(), v.VLAN, v.UpIf)
		} else {
			fmt.Fprintf(&b, "net %s bridge0\n", v.Name)
		}
	}
	var list []int
	for id := range ids {
		list = append(list, id)
	}
	sort.Ints(list)
	b.WriteString("netgc")
	for _, id := range list {
		fmt.Fprintf(&b, " %d", id)
	}
	b.WriteString("\n")
	return b.String()
}

// AirPortNetScript is the network step alone: it makes every VAP's bridge, vlan
// and membership what the VAPs say, and prints nothing when that already is so.
// It is what airportKeep runs once a minute while a VLAN WLAN exists: the unit's
// boot script can make the mesh uplink again, and vlanN loses its parent.
func AirPortNetScript(vaps []AirPortVAP) string {
	return apScriptHead + apNetFuncs + apNetCalls(vaps) + "exit 0\n"
}

// HasVLAN says whether any of the VAPs is on a VLAN.
func HasVLAN(vaps []AirPortVAP) bool {
	for _, v := range vaps {
		if v.VLAN > 0 {
			return true
		}
	}
	return false
}

// SyncAirPortNets runs AirPortNetScript. It returns one line per change.
func (cl *Client) SyncAirPortNets(vaps []AirPortVAP) ([]string, error) {
	if !cl.IsAirPort() {
		return nil, errors.New("not an AirPort base station")
	}
	if err := checkAirPortVAPs(vaps); err != nil {
		return nil, err
	}
	out, err := cl.runStdin("sh", []byte(AirPortNetScript(vaps)))
	lines := reportLines(out)
	if err != nil {
		return lines, fmt.Errorf("airport net: %w", err)
	}
	for _, l := range lines {
		if strings.Contains(l, " failed: ") || strings.HasPrefix(l, "failed:") {
			return lines, errors.New(l)
		}
	}
	return lines, nil
}
