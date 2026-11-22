// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Band steering on an AirPort, "prefer 5 GHz" only. OpenWrt runs usteer for it;
// the base station has no hostapd control interface to refuse an association
// from, so the daemon approximates it the one way the driver allows: a client
// that has just joined on 2.4 GHz is kicked, and its MAC is denied on that VAP
// for a few seconds (the driver ACL of airport_ctl.go), which makes a dual-band
// client look at the same SSID on 5 GHz. The measured behaviour of that deny
// (gaps-device.md C) is that a refused PC stays "associating" and moves to
// another network.
//
// That is a blunt tool for a client that cannot use 5 GHz, so every limit below
// is on the conservative side, and none of it runs unless the controller turns
// band steering on in "prefer 5 GHz" mode:
//   - only a client that has already been seen on 5 GHz on this AP (so it is
//     known to do 5 GHz and to reach it from where it sits) and only while the
//     same SSID is running on 5 GHz;
//   - only a client that has just joined (no ongoing session is cut), with a
//     2.4 GHz signal that is good (a client at the edge of the cell may be worse
//     off on 5 GHz);
//   - at most steerMaxTries tries per client per steerWindow, at least
//     steerGap apart; a client seen on 5 GHz afterwards starts from zero;
//   - one client per pass, and the deny lasts steerDeny seconds, undone by the
//     unit itself (a background job in the script), so a daemon that dies
//     in between leaves nothing denied;
//   - not on a VAP whose driver ACL is an allow list or is not known.
//
// "Equal" mode (load balancing) is not done: the per-radio client limit is
// already rendered as max_clients (AirPortVAPs), and a balancer needs the
// client's 5 GHz signal, which the driver does not give for a client that is
// not associated there.
const (
	steerDeny      = 10 * time.Second
	steerMinSignal = -65 // dBm on 2.4 GHz: steer only a client with a good link
	steerFresh     = 30  // seconds since association: steer only a client that just joined
	steerMaxTries  = 3
	steerWindow    = 6 * time.Hour
	steerGap       = 2 * time.Minute
	steerSeen5     = 24 * time.Hour // how long "seen on 5 GHz" counts
	steerMaxMemory = 4096           // clients remembered
)

// SteerClient is one associated client of an AP VAP.
type SteerClient struct {
	MAC       string
	VAP, SSID string
	Band      string // "ng" or "na"
	Signal    int    // dBm
	Connected int64  // seconds since it associated
}

// SteerAct is one client to steer.
type SteerAct struct {
	MAC, VAP string
	Deny     time.Duration
}

// SteerMemory is what the steering decision remembers between passes. Not safe
// for concurrent use: one goroutine (the inform loop) owns it.
type SteerMemory struct {
	seen5 map[string]time.Time   // client -> last seen on a 5 GHz VAP
	tries map[string][]time.Time // client -> its recent steering tries
	busy  map[string]time.Time   // VAP -> its deny window ends
	prune time.Time
}

// Next looks at the clients of one pass and returns at most one to steer. steered
// is the SSIDs the controller steers; ssid5 the SSIDs that have a running AP VAP
// on 5 GHz. Both nil/empty = nothing is steered (but 5 GHz clients are still
// noted, so a later turn-on already knows them).
func (m *SteerMemory) Next(now time.Time, steered, ssid5 map[string]bool, clients []SteerClient) []SteerAct {
	if m.seen5 == nil {
		m.seen5, m.tries, m.busy = map[string]time.Time{}, map[string][]time.Time{}, map[string]time.Time{}
	}
	for _, c := range clients {
		if c.Band == "na" && len(m.seen5) < steerMaxMemory {
			m.seen5[c.MAC] = now
			delete(m.tries, c.MAC) // it went where it was sent
		}
	}
	if now.Sub(m.prune) > 10*time.Minute {
		m.prune = now
		for k, t := range m.seen5 {
			if now.Sub(t) > steerSeen5 {
				delete(m.seen5, k)
			}
		}
		for k, ts := range m.tries {
			if len(recent(ts, now, steerWindow)) == 0 {
				delete(m.tries, k)
			}
		}
		for k, t := range m.busy {
			if now.After(t) {
				delete(m.busy, k)
			}
		}
	}
	if len(steered) == 0 {
		return nil
	}
	// the freshest client first, so the one that is joining now is the one looked at
	sort.SliceStable(clients, func(i, j int) bool { return clients[i].Connected < clients[j].Connected })
	for _, c := range clients {
		switch {
		case c.Band != "ng", !steered[c.SSID], !ssid5[c.SSID]:
		case c.Connected > steerFresh, c.Signal < steerMinSignal, c.Signal >= 0:
		case now.Sub(m.seen5[c.MAC]) > steerSeen5 || m.seen5[c.MAC].IsZero():
		case now.Before(m.busy[c.VAP]):
		default:
			ts := recent(m.tries[c.MAC], now, steerWindow)
			if len(ts) >= steerMaxTries || (len(ts) > 0 && now.Sub(ts[len(ts)-1]) < steerGap) {
				continue
			}
			m.tries[c.MAC] = append(ts, now)
			m.busy[c.VAP] = now.Add(steerDeny)
			return []SteerAct{{MAC: c.MAC, VAP: c.VAP, Deny: steerDeny}}
		}
	}
	return nil
}

// recent is the times in ts that are not older than d.
func recent(ts []time.Time, now time.Time, d time.Duration) []time.Time {
	var out []time.Time
	for _, t := range ts {
		if now.Sub(t) <= d {
			out = append(out, t)
		}
	}
	return out
}

// airPortSteerScript denies mac on VAP for secs seconds and kicks it. The VAP's
// driver ACL is the one airport_ctl.go keeps in $D/VAP.acl: only an "open" ACL
// (no list: it becomes a deny list of this one MAC) or a "deny" list that does
// not have the MAC are steered; anything else is left alone. The undo runs on
// the unit, in the background, from what the file says then: a deny list loses
// the MAC, an open one goes back to open, and a VAP that was made again in the
// meantime (no file) is not touched.
func airPortSteerScript(vap, mac string, secs int) (string, error) {
	if !apVAPName.MatchString(vap) || !strings.HasPrefix(vap, "wlan1") {
		return "", fmt.Errorf("steer: refusing VAP %q (2.4 GHz VAPs only)", vap)
	}
	m, ok := cleanMAC(mac)
	if !ok {
		return "", fmt.Errorf("steer: bad MAC %q", mac)
	}
	if secs < 1 || secs > 30 {
		return "", fmt.Errorf("steer: refusing a deny of %d s", secs)
	}
	return apPATH + fmt.Sprintf(`D=%s
v=%s; x=%s; n=%d
f=$D/$v.acl
cur=$(cat $f 2>/dev/null)
case "$cur" in
open|open\ *|deny|deny\ *) ;;
*) echo "steer $v $x skipped: its ACL is not open or a deny list"; exit 0;;
esac
case " $cur " in *" $x "*) echo "steer $v $x skipped: already in its list"; exit 0;; esac
case "$(ifconfig $v 2>&1)" in *RUNNING*"status: running"*) ;; *) echo "steer $v $x skipped: VAP not running"; exit 0;; esac
ifconfig $v mac:add $x >/dev/null 2>&1 || { echo "steer $v $x failed: mac:add"; exit 0; }
ifconfig $v mac:deny >/dev/null 2>&1 || { ifconfig $v mac:del $x >/dev/null 2>&1; echo "steer $v $x failed: mac:deny"; exit 0; }
ifconfig $v mac:kick $x >/dev/null 2>&1
echo "steer $v $x denied for $n s"
(
	sleep $n
	c=$(cat $f 2>/dev/null)
	case "$c" in
	deny*|allow*) ifconfig $v mac:del $x >/dev/null 2>&1;;
	open*) ifconfig $v mac:flush >/dev/null 2>&1; ifconfig $v mac:open >/dev/null 2>&1;;
	esac
) >/dev/null 2>&1 </dev/null &
exit 0
`, apRunDir, vap, m, secs), nil
}

// SteerAirPort carries out one steering act and returns the script's report.
func (cl *Client) SteerAirPort(a SteerAct) ([]string, error) {
	if !cl.IsAirPort() {
		return nil, errors.New("not an AirPort base station")
	}
	s, err := airPortSteerScript(a.VAP, a.MAC, int(a.Deny/time.Second))
	if err != nil {
		return nil, err
	}
	out, err := cl.runStdin("sh", []byte(s))
	lines := reportLines(out)
	if err != nil {
		return lines, fmt.Errorf("airport steer: %w", err)
	}
	return lines, nil
}

// SteerClients lists the associated clients of the daemon's own AP VAPs of a
// reading, and the SSIDs that are running on 5 GHz.
func (s *Snapshot) SteerClients() (clients []SteerClient, ssid5 map[string]bool) {
	ssid5 = map[string]bool{}
	for _, r := range s.Radios {
		for _, v := range r.VAPs {
			if v.Mode != "Master" || !apVAPName.MatchString(v.Iface) {
				continue
			}
			if v.Band == "na" {
				ssid5[v.SSID] = true
			}
			for _, st := range v.Stations {
				clients = append(clients, SteerClient{MAC: st.MAC, VAP: v.Iface, SSID: v.SSID, Band: v.Band, Signal: st.Signal, Connected: st.Connected})
			}
		}
	}
	return clients, ssid5
}
