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
// cap on the WLAN's VAP, shared by its clients, and the upload limit is noted
// as not applied.

// apPFMaxKbps is the parent queue's bandwidth (1Gb): a child may not ask for
// more, and a cap that high is no cap on this unit's radios anyway.
const apPFMaxKbps = 1000000

// PFLimit is the download cap of one VAP.
type PFLimit struct {
	VAP      string // wlan10..17, wlan20..27
	DownKbps int
}

// AirPortLimits maps the controller's rate limits to the VAPs of the last apply
// (by the controller's devname, VAP.Devname) and returns the caps to set, in
// the order of vaps, with the notes for the log: once for the shared download
// limit, once for the upload limits that are ignored.
func AirPortLimits(lims []translate.VAPLimit, vaps []AirPortVAP) ([]PFLimit, []string) {
	byDev := map[string]AirPortVAP{}
	for _, v := range vaps {
		if v.Devname != "" {
			byDev[v.Devname] = v
		}
	}
	down := map[string]int{} // VAP -> kbps, the lowest when a devname has two
	upSSID := map[string]bool{}
	var notes, noVAP, tooBig []string
	for _, l := range lims {
		v, ok := byDev[l.Devname]
		if !ok {
			noVAP = append(noVAP, l.Devname)
			continue
		}
		if l.UpKbps > 0 {
			upSSID[fmt.Sprintf("%q", v.SSID)] = true
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
	if len(upSSID) > 0 {
		notes = append(notes, fmt.Sprintf("upload limit ignored on WLAN(s) %s: a per-client upload limit needs a queue on the uplink, which slows every client", sortedKeys(upSSID)))
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

// apPFHead builds the ruleset in $T from the capped VAPs that run (an altq
// needs its interface), then loads it only when something is to change. pf is
// the unit's own and was off, with a stored ruleset of "block drop all": our
// ruleset (queues, then "pass all", never a block) is loaded BEFORE pf is
// enabled, and pf is never enabled after a failed load. pf.conf is the proof
// that the ruleset in force is ours, so with no cap wanted nothing is touched
// unless that file exists.
//
// A VAP that was destroyed loses its queue, and pf does not say so: VAP.pfq
// marks "the ruleset was loaded with this VAP in it" and the apply script
// removes it when it stops or starts a VAP (as it does VAP.acl). A running
// capped VAP without a marker makes the ruleset load again; parsing
// "pfctl -sq" instead would depend on a format never seen on this unit.
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
`

const apPFTail = `if [ $n -eq 0 ]; then
	rm -f $T
	if [ -e $F ]; then
		pfctl -d >/dev/null 2>&1
		pfctl -F all >/dev/null 2>&1
		rm -f $F $D/wlan1?.pfq $D/wlan2?.pfq
		echo "pf: off"
	fi
	exit 0
fi
echo "pass all" >> $T
need=
[ "$(cat $T)" = "$(cat $F 2>/dev/null)" ] || need=1
case "$(pfctl -s info 2>&1)" in *"Status: Enabled"*) ;; *) need=1;; esac
for v in $w; do [ -e $D/$v.pfq ] || need=1; done
if [ -z "$need" ]; then
	rm -f $T
	exit 0
fi
if ! r=$(pfctl -f $T 2>&1); then
	echo "pf failed: load: $(echo "$r" | sed -n 1p)"
	rm -f $T
	exit 0
fi
mv $T $F
rm -f $D/wlan1?.pfq $D/wlan2?.pfq
for v in $w; do : > $D/$v.pfq; done
if ! r=$(pfctl -e 2>&1); then
	case "$r" in
	*"already enabled"*) ;;
	*) echo "pf failed: enable: $(echo "$r" | sed -n 1p)"; rm -f $D/wlan1?.pfq $D/wlan2?.pfq; exit 0;;
	esac
fi
echo "pf: $n limited VAP(s)"
exit 0
`

// AirPortLimitsScript is the script that makes pf cap these VAPs (see apPFHead).
// It prints one line per change and nothing when pf is already as wanted. Only
// the daemon's own VAP names and positive whole rates reach the shell.
func AirPortLimitsScript(lims []PFLimit) (string, error) {
	var b strings.Builder
	b.WriteString(apPFHead)
	seen := map[string]bool{}
	for _, l := range lims {
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
	b.WriteString(apPFTail)
	return b.String(), nil
}

// SyncAirPortLimits makes pf cap what lims say. It is idempotent and cheap
// (no pfctl call at all when nothing is wanted and we never loaded a ruleset),
// so call it after every apply and now and then. It returns one line per change.
func (cl *Client) SyncAirPortLimits(lims []PFLimit) ([]string, error) {
	if !cl.IsAirPort() {
		return nil, errors.New("not an AirPort base station")
	}
	s, err := AirPortLimitsScript(lims)
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
