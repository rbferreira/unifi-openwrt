// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/openwrt"
	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// What the daemon does on an AirPort by itself, because the base station has no
// cron, no uci, no usteer and no hostapd control interface: the automatic
// channel (openwrt.AutoChannels), "prefer 5 GHz" band steering and, in
// airport_syslog.go, remote syslog. The decisions are pure functions in
// internal/openwrt; this file holds their state and calls them.

// airportChans remembers the automatic channel chosen for each band, so that a
// configuration push does not move a radio (syncAirPort, airportKeep).
var airportChans openwrt.AutoChannels

var (
	// the SSIDs the controller asks to steer to 5 GHz, nil = band steering is off.
	// Set by syncAirPort, read by the inform loop: one goroutine.
	airportSteerSSIDs map[string]bool
	airportSteer      openwrt.SteerMemory
)

// steeredSSIDs maps the VAP devnames of the controller's steering groups to their
// SSIDs, once each, in order.
func steeredSSIDs(cfgText string, st translate.Steering) []string {
	ssidOf := map[string]string{}
	for k, d := range openwrt.InfoFromCfg(cfgText).DevName {
		ssid, _, _ := strings.Cut(k, "|")
		ssidOf[d] = ssid
	}
	seen := map[string]bool{}
	var ssids []string
	for _, d := range st.Devnames {
		if s := ssidOf[d]; s != "" && !seen[s] {
			seen[s] = true
			ssids = append(ssids, s)
		}
	}
	return ssids
}

// airportSteerSetup reads the controller's band steering (bandsteering.*, the
// keys syncSteering reads for OpenWrt) and arms or disarms the daemon's own
// "prefer 5 GHz" steering. It returns the notes for the log. Load balancing
// (stamgr maxsta) is not a steering: it is rendered as the radio's max_clients.
func airportSteerSetup(cfg translate.Cfg, cfgText string) []string {
	st := translate.BandSteering(cfg)
	switch {
	case st.Mode == "prefer_5g":
		ssids := steeredSSIDs(cfgText, st)
		airportSteerSSIDs = map[string]bool{}
		for _, s := range ssids {
			airportSteerSSIDs[s] = true
		}
		if len(ssids) == 0 {
			return []string{"band steering prefer_5g asked, but no WLAN is in a steering group: nothing is steered"}
		}
		sort.Strings(ssids)
		return []string{fmt.Sprintf("band steering prefer_5g on %q: the daemon kicks a client that has just joined on 2.4 GHz, is known on 5 GHz and has a good signal, and denies it on 2.4 GHz for a few seconds (at most 3 tries per client per 6 h)", ssids)}
	case st.Mode != "":
		airportSteerSSIDs = nil
		return []string{fmt.Sprintf("band steering mode %q: not done on this platform (only prefer_5g is; the client limit of load balancing is applied as max_clients)", st.Mode)}
	}
	airportSteerSSIDs = nil
	return nil
}

// airportSteerPass runs once per inform on the reading the inform is built
// from: it notes which clients are seen on 5 GHz and, when the controller steers,
// kicks at most one. Only the in-memory decision runs every time; the unit is
// called only for a client that is to be steered.
func airportSteerPass(ap *openwrt.Client, s *openwrt.Snapshot) {
	clients, ssid5 := s.SteerClients()
	var steered map[string]bool
	if airportApply {
		steered = airportSteerSSIDs
	}
	for _, a := range airportSteer.Next(time.Now(), steered, ssid5, clients) {
		lines, err := ap.SteerAirPort(a)
		for _, l := range lines {
			log.Printf("STEER: %s", l)
		}
		if err != nil {
			log.Printf("STEER FAILED: %v", err)
		}
	}
}
