// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/ftrelay"
	"github.com/antoniocosta/unifi-openwrt/internal/openwrt"
)

// 802.11r with UniFi APs: see internal/ftrelay.

var (
	ftRelay   *ftrelay.Relay // nil = no relay on this AP
	ftServing string         // what the relay served at the last look, for the log
	ftProblem string
)

var ifaceName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// startFT finds out whether the relay can run here and starts it. The relay
// needs the daemon on the AP itself: it works on the AP's own interfaces.
func startFT(ap *openwrt.Client, verbose bool) {
	if err := ftrelay.Probe(); err != nil {
		log.Printf("FT: no fast roaming to and from UniFi APs: %v", err)
		return
	}
	ftRelay = &ftrelay.Relay{Logf: log.Printf, Verbose: verbose}
	ftRelay.Kick = func(iface string, client ftrelay.MAC) {
		if !ifaceName.MatchString(iface) {
			return
		}
		// reason 8: the client left the BSS
		cmd := fmt.Sprintf(`ubus call hostapd.%s del_client '{"addr":"%s","reason":8,"deauth":false,"ban_time":0}'`, iface, client)
		if _, err := ap.Run(cmd); err != nil {
			log.Printf("ROAM: dropping %s from %s: %v", client, iface, err)
		}
	}
	go watchHostapdLog()
}

// syncFT points the relay at the BSSes hostapd runs right now. Called after
// every Wi-Fi change and every few informs: hostapd needs some seconds to
// bring a changed BSS up, and a network reload takes the relay's bridge
// port away.
func syncFT(ap *openwrt.Client) {
	if ftRelay == nil {
		return
	}
	out, _ := ap.Run("cat /var/run/hostapd-*.conf 2>/dev/null")
	err := ftRelay.Sync(ftrelay.ParseHostapd(string(out)))
	var names []string
	for _, b := range ftRelay.Serving() {
		names = append(names, fmt.Sprintf("%s (%s, on %s)", b.Iface, b.BSSID, b.Bridge))
	}
	sort.Strings(names)
	if s := strings.Join(names, ", "); s != ftServing {
		ftServing = s
		if s == "" {
			log.Printf("FT: no WLAN with the controller's fast-roaming key is up: relay idle")
		} else {
			log.Printf("FT: relaying 802.11r key exchange and roam announcements for %s", s)
		}
	}
	problem := ""
	if err != nil {
		problem = err.Error()
	}
	if problem != ftProblem {
		ftProblem = problem
		if problem != "" {
			log.Printf("FT: %s", problem)
		}
	}
}

func closeFT() {
	if ftRelay != nil {
		ftRelay.Close()
	}
}

// watchHostapdLog follows the system log for clients joining and leaving.
// Reading the log does not touch hostapd; "logread -f" starts with new
// lines only, so nothing old is announced.
func watchHostapdLog() {
	for {
		c := exec.Command("logread", "-f", "-e", "AP-STA-")
		out, err := c.StdoutPipe()
		if err == nil {
			err = c.Start()
		}
		if err != nil {
			log.Printf("ROAM: cannot follow the system log (%v): no roam announcements", err)
			return
		}
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			iface, client, joined, ok := ftrelay.ParseEvent(sc.Text())
			switch {
			case !ok:
			case joined:
				ftRelay.Joined(iface, client, time.Now())
			default:
				ftRelay.Left(iface, client)
			}
		}
		c.Wait()
		time.Sleep(5 * time.Second)
	}
}

// Which fast-roaming key types the other APs of each WLAN lack, from the
// scans (openwrt.SiblingFT). Remembered in the state directory and kept for
// a week after the last sighting: a scan that misses the neighbour must not
// change the Wi-Fi configuration back and forth.
type ftLack struct {
	PSK time.Time `json:"psk,omitempty"` // last time a sibling without FT-PSK was seen
	SAE time.Time `json:"sae,omitempty"`
}

const ftLackKeep = 7 * 24 * time.Hour

var (
	ftPeers     = map[string]ftLack{} // by SSID
	ftPeersPath string
	ftPeersSig  string
)

func loadFTPeers(path string) {
	ftPeersPath = path
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &ftPeers)
	}
	ftPeersSig = ftLimitsSig()
}

// ftLimits is what the plan withholds right now.
func ftLimits() (noPSK, noSAE map[string]bool) {
	noPSK, noSAE = map[string]bool{}, map[string]bool{}
	for ssid, l := range ftPeers {
		if time.Since(l.PSK) < ftLackKeep {
			noPSK[ssid] = true
		}
		if time.Since(l.SAE) < ftLackKeep {
			noSAE[ssid] = true
		}
	}
	return
}

func ftLimitsSig() string {
	p, s := ftLimits()
	var out []string
	for ssid := range p {
		out = append(out, "psk "+ssid)
	}
	for ssid := range s {
		out = append(out, "sae "+ssid)
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// updateFTPeers takes in the latest scans. It returns true when what the
// plan withholds has changed, so the caller plans again.
func updateFTPeers(seen map[string]openwrt.PeerFT) bool {
	if ftRelay == nil {
		return false
	}
	now, dirty := time.Now(), false
	for ssid, p := range seen {
		l := ftPeers[ssid]
		// one write an hour is enough to keep the week running
		if p.LackPSK && now.Sub(l.PSK) > time.Hour {
			l.PSK, dirty = now, true
		}
		if p.LackSAE && now.Sub(l.SAE) > time.Hour {
			l.SAE, dirty = now, true
		}
		ftPeers[ssid] = l
	}
	if dirty && ftPeersPath != "" {
		if b, err := json.Marshal(ftPeers); err == nil {
			os.WriteFile(ftPeersPath, b, 0o600)
		}
	}
	sig := ftLimitsSig()
	if sig == ftPeersSig {
		return false
	}
	ftPeersSig = sig
	return true
}
