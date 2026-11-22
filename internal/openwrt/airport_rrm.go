// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The AirPort's 802.11k neighbour lists. The driver keeps one per VAP
// (`wl -i VAP rrm_nbr_add_nbr BSSID INFO CLASS CHANNEL PHY`, `rrm_nbr_del_nbr
// BSSID`, `rrm_nbr_list`; all measured) and the VAP's hostapd runs with rrm=1,
// so what is listed reaches the clients in their neighbour reports. The
// candidates are the ones the OpenWrt path uses (rrm.go: another BSS with the
// VAP's SSID that belongs to a UniFi device on the LAN, from the scans of both
// bands) minus the unit's own BSSIDs; the entry fields are rrm.go's choices
// written out as numbers, since `wl` takes the five fields and not hostapd's hex
// element. Nothing here touches the VAP's hostapd or the radio.

// apNbrMax caps one VAP's list. rrm.go has no cap; a client is told about the
// best few, and the driver's list is small.
const apNbrMax = 16

// AirPortNbr is one neighbour entry, the five fields of rrm_nbr_add_nbr.
type AirPortNbr struct {
	BSSID   string // lower case
	Info    int    // BSSID information
	Class   int    // regulatory (operating) class, 20 MHz
	Channel int
	Phy     int // 7 = HT, 9 = VHT
}

// apOpClass is the 20 MHz operating class of a channel: 81 for 2.4 GHz 1..13,
// and on 5 GHz 115 (36..48), 118 (52..64), 121 (100..144), 124 (149..161),
// 125 (165). 0 for any other channel (14, the 4.9 GHz and 6 GHz ones): no entry.
func apOpClass(freq, ch int) int {
	switch {
	case freq < 3000:
		if ch >= 1 && ch <= 13 {
			return 81
		}
	case ch >= 36 && ch <= 48:
		return 115
	case ch >= 52 && ch <= 64:
		return 118
	case ch >= 100 && ch <= 144:
		return 121
	case ch >= 149 && ch <= 161:
		return 124
	case ch == 165:
		return 125
	}
	return 0
}

// apNbrFor builds the entry of a scanned BSS. The BSSID information is
// NeighborElement's (reachable, security, key scope, QoS, RRM, HT: 0x08af, and
// VHT, 0x1000, on 5 GHz) and the PHY type follows the band the same way (HT 7 on
// 2.4 GHz, VHT 9 on 5 GHz): a scan row does not say more than that.
func apNbrFor(b BSS) (AirPortNbr, bool) {
	oc := apOpClass(b.Freq, b.Channel)
	m, ok := cleanMAC(b.BSSID)
	if oc == 0 || !ok {
		return AirPortNbr{}, false
	}
	n := AirPortNbr{BSSID: m, Info: 0x08af, Class: oc, Channel: b.Channel, Phy: 7}
	if b.Freq > 5000 {
		n.Info |= 0x1000
		n.Phy = 9
	}
	return n, true
}

// Arg is the entry as the script takes it: BSSID,INFO,CLASS,CHANNEL,PHY, in
// decimal (wl reads either; a decimal number cannot be taken for something else).
func (n AirPortNbr) Arg() string {
	return fmt.Sprintf("%s,%d,%d,%d,%d", n.BSSID, n.Info, n.Class, n.Channel, n.Phy)
}

// AirPortNeighbors picks the neighbour entries of one SSID: BSSes of the scans
// with that SSID that are not in own (lower case BSSIDs) and are matched to a
// UniFi device in arp (as Siblings does), on a channel that has an operating
// class. A BSS seen on both bands' scans counts once, with its strongest
// signal. At most apNbrMax, the strongest first kept, listed by BSSID.
func AirPortNeighbors(results []ScanResult, ssid string, own map[string]bool, arp map[string]string) []AirPortNbr {
	type cand struct {
		n   AirPortNbr
		sig int
	}
	best := map[string]cand{}
	for _, r := range results {
		for _, b := range r.BSS {
			if b.SSID != ssid || own[strings.ToLower(b.BSSID)] || parentDevice(b.BSSID, arp) == "" {
				continue
			}
			n, ok := apNbrFor(b)
			if !ok {
				continue
			}
			if c, seen := best[n.BSSID]; seen && c.sig >= b.Signal {
				continue
			}
			best[n.BSSID] = cand{n, b.Signal}
		}
	}
	all := make([]cand, 0, len(best))
	for _, c := range best {
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].sig != all[j].sig {
			return all[i].sig > all[j].sig
		}
		return all[i].n.BSSID < all[j].n.BSSID
	})
	if len(all) > apNbrMax {
		all = all[:apNbrMax]
	}
	out := make([]AirPortNbr, 0, len(all))
	for _, c := range all {
		out = append(out, c.n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BSSID < out[j].BSSID })
	return out
}

// AirPortNbrSet is one VAP and the list it is to have.
type AirPortNbrSet struct {
	VAP  string
	SSID string
	Nbrs []AirPortNbr
}

// AirPortNbrSets makes the wanted list of every AP VAP of the daemon the
// snapshot shows (wlan10..17, wlan20..27 in Master mode with an SSID; not the
// uplink, not the firmware's own VAPs), from the scan results of both bands.
// The unit's own BSSIDs, radios and VAPs alike, are never listed.
func AirPortNbrSets(s *Snapshot, results []ScanResult) []AirPortNbrSet {
	if s == nil {
		return nil
	}
	own := map[string]bool{}
	for _, r := range s.Radios {
		own[strings.ToLower(r.BSSID)] = true
		for _, v := range r.VAPs {
			own[strings.ToLower(v.BSSID)] = true
		}
	}
	var out []AirPortNbrSet
	for _, r := range s.Radios {
		for _, v := range r.VAPs {
			if v.Mode != "Master" || v.SSID == "" || !apVAPName.MatchString(v.Iface) {
				continue
			}
			out = append(out, AirPortNbrSet{VAP: v.Iface, SSID: v.SSID, Nbrs: AirPortNeighbors(results, v.SSID, own, apKnownMACs(s))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VAP < out[j].VAP })
	return out
}

// apNbrHead defines nbr VAP ENTRY..., which makes the driver's neighbour list of
// a VAP exactly those entries. What was applied last time is kept in VAP.nbr
// (the entries, space separated); when it is the same nothing runs. The entries
// of the old text are deleted one by one, then the new ones added, so the list
// ends as wanted even after a change of channel. The file goes away when the VAP
// is stopped or started (airport_apply.go): a new VAP has an empty list and no
// file, so it gets its entries; a VAP that is not running has no list to fill.
// An entry the driver refuses is left out of the file, so the next pass tries it
// again. The old text is read from the device's RAM, but only its BSSIDs are
// ever used, and only after a shape check.
const apNbrHead = apPATH + `umask 077
D=` + apRunDir + `
mkdir -p $D
nbr() {
	v=$1; shift
	new="$*"
	f=$D/$v.nbr
	case "$(ifconfig $v 2>&1)" in *RUNNING*"status: running"*) ;; *) rm -f $f; return;; esac
	if [ -f $f ]; then
		old=$(cat $f)
		[ "$old" = "$new" ] && return
	else
		old=
		[ -z "$new" ] && { : > $f; return; }
	fi
	for x in $old; do
		m=${x%%,*}
		case $m in [0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]) wl -i $v rrm_nbr_del_nbr $m >/dev/null 2>&1;; esac
	done
	kept=; n=0
	for x in "$@"; do
		m=${x%%,*}; r=${x#*,}
		i=${r%%,*}; r=${r#*,}
		c=${r%%,*}; r=${r#*,}
		h=${r%%,*}; p=${r#*,}
		if wl -i $v rrm_nbr_add_nbr $m $i $c $h $p >/dev/null 2>&1; then
			n=$((n + 1))
			if [ -n "$kept" ]; then kept="$kept $x"; else kept=$x; fi
		else
			echo "$v nbr: add $m refused"
		fi
	done
	echo "$kept" > $f
	echo "$v neighbours: $n"
}
`

// AirPortNbrScript is the script that brings every listed VAP's neighbour list
// to its entries. It prints a line for each VAP it changed ("wlan11
// neighbours: 3"), nothing for the rest. Only the daemon's own VAP names and
// well-formed entries reach it.
func AirPortNbrScript(sets []AirPortNbrSet) (string, error) {
	var b strings.Builder
	b.WriteString(apNbrHead)
	seen := map[string]bool{}
	for _, s := range sets {
		if !apVAPName.MatchString(s.VAP) {
			return "", fmt.Errorf("refusing VAP name %q", s.VAP)
		}
		if seen[s.VAP] {
			return "", fmt.Errorf("VAP %s twice", s.VAP)
		}
		seen[s.VAP] = true
		list := append([]AirPortNbr(nil), s.Nbrs...)
		sort.Slice(list, func(i, j int) bool { return list[i].BSSID < list[j].BSSID })
		fmt.Fprintf(&b, "nbr %s", s.VAP)
		for _, n := range list {
			// the last look before a shell: a MAC as the script's own pattern
			// takes it, and numbers
			if !macOK.MatchString(n.BSSID) {
				return "", fmt.Errorf("refusing MAC %q", n.BSSID)
			}
			if n.Info < 0 || n.Info > 0xffff || n.Class < 1 || n.Class > 255 || n.Channel < 1 || n.Channel > 255 || n.Phy < 0 || n.Phy > 255 {
				return "", fmt.Errorf("refusing entry %+v", n)
			}
			b.WriteString(" " + n.Arg())
		}
		b.WriteString("\n")
	}
	b.WriteString("exit 0\n")
	return b.String(), nil
}

// SyncAirPortNeighbors makes the neighbour list of each of these VAPs what its
// set says. It is idempotent and cheap (one cat per VAP when nothing changed).
// It returns one line per VAP it changed.
func (cl *Client) SyncAirPortNeighbors(sets []AirPortNbrSet) ([]string, error) {
	if !cl.IsAirPort() {
		return nil, errors.New("not an AirPort base station")
	}
	if len(sets) == 0 {
		return nil, nil
	}
	s, err := AirPortNbrScript(sets)
	if err != nil {
		return nil, err
	}
	out, err := cl.runStdin("sh", []byte(s))
	lines := reportLines(out)
	if err != nil {
		return lines, fmt.Errorf("airport neighbours: %w", err)
	}
	return lines, nil
}

// apKnownMACs is every MAC the unit knows on its LAN: its ARP table, which
// only has the few hosts it talks to, plus the bridge's forwarding table,
// where the other APs of the site show up (measured: with the ARP table alone
// no BSS of the same network was ever recognised as a sibling).
func apKnownMACs(s *Snapshot) map[string]string {
	m := map[string]string{}
	for mac, ip := range s.ARP {
		m[mac] = ip
	}
	for _, e := range s.FDB {
		if _, ok := m[e.MAC]; !ok && !e.Local {
			m[e.MAC] = ""
		}
	}
	return m
}
