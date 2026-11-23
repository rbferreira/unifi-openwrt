// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// What the controller can ask of a station on an AirPort, beyond the WLANs
// themselves: Reconnect (kick), Block, and the WLAN's MAC filter. The last two
// are one mechanism, the driver's own ACL per VAP (ifconfig VAP mac:add,
// mac:deny, mac:allow, mac:open, mac:flush, mac:kick; measured). It takes
// effect at once and needs no hostapd restart; hostapd's own deny_mac_file only
// reads its list at start.

// apPATH is the first line of every script here: the device's sh has no PATH of
// its own worth trusting.
const apPATH = "PATH=/bin:/sbin:/usr/bin:/usr/sbin\n"

// cleanMAC lower-cases a MAC and checks it is six hex pairs. Only a MAC that
// passes may reach a script.
func cleanMAC(m string) (string, bool) {
	m = strings.ToLower(strings.TrimSpace(m))
	return m, macOK.MatchString(m)
}

// apCleanMACs is the valid MACs of in, lower case, sorted, once each; bad counts
// the entries that were not a MAC.
func apCleanMACs(in []string) (out []string, bad int) {
	seen := map[string]bool{}
	for _, m := range in {
		c, ok := cleanMAC(m)
		switch {
		case !ok:
			bad++
		case !seen[c]:
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, bad
}

// aclPolicy is the WLAN's MAC filter as kept on the VAP: policy "allow" or
// "deny" with its clean list, or "disable" (no list). bad counts what was left
// out of the list.
func aclPolicy(policy string, macs []string) (string, []string, int) {
	if policy != "allow" && policy != "deny" {
		return "disable", nil, 0
	}
	list, bad := apCleanMACs(macs)
	return policy, list, bad
}

// AirPortACL is what one VAP's driver ACL has to be.
type AirPortACL struct {
	Mode string   // "open", "deny" or "allow"
	MACs []string // clean and sorted; none for open
}

// Text is the ACL as one line, the form the script keeps in VAP.acl to know
// what it applied.
func (a AirPortACL) Text() string {
	return strings.TrimSpace(a.Mode + " " + strings.Join(a.MACs, " "))
}

// WantedACL computes the ACL of a VAP from the WLAN's MAC filter and the
// controller's blocked clients:
//   - policy disable or deny: deny mode, the WLAN's deny list plus the blocked
//     clients; open mode when that is empty;
//   - policy allow: allow mode, the allow list without the blocked clients (an
//     empty list lets nobody in).
//
// blocked entries that are not a MAC are ignored.
func WantedACL(policy string, macs, blocked []string) AirPortACL {
	blk, _ := apCleanMACs(blocked)
	list, _ := apCleanMACs(macs)
	if policy == "allow" {
		drop := map[string]bool{}
		for _, m := range blk {
			drop[m] = true
		}
		var keep []string
		for _, m := range list {
			if !drop[m] {
				keep = append(keep, m)
			}
		}
		return AirPortACL{Mode: "allow", MACs: keep}
	}
	if policy != "deny" {
		list = nil // "disable": whatever list came along is not in force
	}
	all, _ := apCleanMACs(append(append([]string{}, list...), blk...))
	if len(all) == 0 {
		return AirPortACL{Mode: "open"}
	}
	return AirPortACL{Mode: "deny", MACs: all}
}

// apACLHead defines acl VAP MODE MAC..., which makes the driver's ACL of a VAP
// exactly that. The text applied last time is kept in VAP.acl: when it is the
// same nothing runs (a pass costs one cat per VAP); the file goes away when the
// VAP is stopped or started (airport_apply.go), so a new VAP gets its ACL.
// Kicks: a MAC that is now denied and was not before (deny mode); every station
// that is not on the list (allow mode); none for open. The associated stations
// come from wl assoclist, whose MAC format is the one measured.
const apACLHead = apPATH + `umask 077
D=` + apRunDir + `
mkdir -p $D
acl() {
	v=$1; m=$2; shift 2
	new=$m; for x in "$@"; do new="$new $x"; done
	f=$D/$v.acl
	old=$(cat $f 2>/dev/null)
	[ "$old" = "$new" ] && return
	case "$(ifconfig $v 2>&1)" in *RUNNING*"status: running"*) ;; *) rm -f $f; return;; esac
	ifconfig $v mac:flush >/dev/null 2>&1 || { rm -f $f; echo "$v acl failed: mac:flush"; return; }
	for x in "$@"; do ifconfig $v mac:add $x >/dev/null 2>&1 || echo "$v acl: mac:add $x refused"; done
	ifconfig $v mac:$m >/dev/null 2>&1 || { rm -f $f; echo "$v acl failed: mac:$m"; return; }
	echo "$new" > $f
	case "$old" in "deny "*) ;; *) old=;; esac
	k=0
	case $m in
	deny)
		for x in "$@"; do
			case " $old " in *" $x "*) continue;; esac
			ifconfig $v mac:kick $x >/dev/null 2>&1
			k=$((k + 1))
		done;;
	allow)
		for x in $(wl -i $v assoclist 2>/dev/null | while read a b; do echo $b; done | sed 'y/ABCDEF/abcdef/'); do
			case $x in [0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]) ;; *) continue;; esac
			case " $* " in *" $x "*) continue;; esac
			ifconfig $v mac:kick $x >/dev/null 2>&1
			k=$((k + 1))
		done;;
	esac
	echo "$v acl $m $# MAC(s), kicked $k"
}
`

// AirPortACLScript is the script that brings every VAP's driver ACL to what
// WantedACL says. It prints a line for each VAP it changed, nothing for the
// rest. Only the daemon's own VAP names are accepted.
func AirPortACLScript(vaps []AirPortVAP, blocked []string) (string, error) {
	if err := checkAirPortVAPs(vaps); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(apACLHead)
	for _, v := range vaps {
		a := WantedACL(v.MACPolicy, v.MACs, blocked)
		fmt.Fprintf(&b, "acl %s %s", v.Name, a.Mode)
		for _, m := range a.MACs {
			if !macOK.MatchString(m) { // WantedACL cleans them; this is the last look before a shell
				return "", fmt.Errorf("refusing MAC %q", m)
			}
			b.WriteString(" " + m)
		}
		b.WriteString("\n")
	}
	b.WriteString("exit 0\n")
	return b.String(), nil
}

// SyncAirPortACL makes the driver ACL of each of these VAPs (the ones the last
// apply asked for) what the WLAN's MAC filter and the blocked list say. It is
// idempotent and cheap, so call it after every apply, when the blocked list
// changes, and now and then. It returns one line per VAP it changed.
func (cl *Client) SyncAirPortACL(vaps []AirPortVAP, blocked []string) ([]string, error) {
	if !cl.IsAirPort() {
		return nil, errors.New("not an AirPort base station")
	}
	if len(vaps) == 0 {
		return nil, nil
	}
	s, err := AirPortACLScript(vaps, blocked)
	if err != nil {
		return nil, err
	}
	out, err := cl.runStdin("sh", []byte(s))
	lines := reportLines(out)
	if err != nil {
		return lines, fmt.Errorf("airport acl: %w", err)
	}
	for _, l := range lines {
		if strings.Contains(l, " failed: ") {
			return lines, errors.New(l)
		}
	}
	return lines, nil
}

func reportLines(out []byte) []string {
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// airPortKickScript deauthenticates one station on every VAP of the daemon's
// that exists. It may come straight back (that is what Reconnect is).
func airPortKickScript(mac string) (string, error) {
	m, ok := cleanMAC(mac)
	if !ok {
		return "", fmt.Errorf("bad MAC %q", mac)
	}
	return apPATH + "for v in $(ifconfig -l); do\n" +
		"\tcase $v in wlan1[0-7]|wlan2[0-7]) wl -i $v deauthenticate " + m + " >/dev/null 2>&1;; esac\n" +
		"done\nexit 0\n", nil
}

func (cl *Client) kickAirPort(mac string) error {
	s, err := airPortKickScript(mac)
	if err != nil {
		return err
	}
	_, err = cl.runStdin("sh", []byte(s))
	return err
}
