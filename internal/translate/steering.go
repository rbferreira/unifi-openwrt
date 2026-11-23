// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"sort"
	"strconv"
)

// Steering is the controller's band steering (bandsteering section): mode
// "prefer_5g" | "equal" | "" (off) and the VAP devnames in steering groups.
type Steering struct {
	Mode     string
	Devnames []string
}

func BandSteering(cfg Cfg) Steering {
	if cfg["bandsteering.status"] != "enabled" || cfg["bandsteering.mode"] == "off" {
		return Steering{}
	}
	s := Steering{Mode: cfg["bandsteering.mode"]}
	for _, g := range indices(cfg, "bandsteering") {
		if cfg["bandsteering."+g+".status"] != "enabled" {
			continue
		}
		for _, v := range indices(cfg, "bandsteering."+g+".vap") {
			if d := cfg["bandsteering."+g+".vap."+v+".devname"]; d != "" {
				s.Devnames = append(s.Devnames, d)
			}
		}
	}
	sort.Strings(s.Devnames)
	if len(s.Devnames) == 0 {
		return Steering{}
	}
	return s
}

// RoamAssist: the WLANs' Roaming Assistant (a per-VAP key, sent for the
// 5 GHz VAPs): wireless.N.btm_disassoc.status=enabled and .threshold=<dBm>,
// as "<ssid>|<radio name>" (wireless.N.ssid, wireless.N.parent = wifiN) ->
// dBm. A positive threshold is RSSI above the noise floor (value - 95 =
// dBm), as with minimum RSSI. The daemon acts on it (openwrt.RoamAssist).
func RoamAssist(cfg Cfg) map[string]int {
	out := map[string]int{}
	for _, n := range indices(cfg, "wireless") {
		w := "wireless." + n + "."
		if cfg[w+"btm_disassoc.status"] != "enabled" || cfg[w+"ssid"] == "" || cfg[w+"parent"] == "" {
			continue
		}
		v, err := strconv.Atoi(cfg[w+"btm_disassoc.threshold"])
		if err != nil || v == 0 {
			continue
		}
		if v > 0 {
			v -= 95
		}
		out[cfg[w+"ssid"]+"|"+cfg[w+"parent"]] = v
	}
	return out
}
