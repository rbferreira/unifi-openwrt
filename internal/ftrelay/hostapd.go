// SPDX-License-Identifier: GPL-2.0-or-later

package ftrelay

import (
	"encoding/hex"
	"strings"
)

// ParseHostapd reads hostapd's running configuration files (their text, one
// after the other) and returns the BSSes the relay can serve: 802.11r on,
// and the wildcard R0KH entry that carries the WLAN's key.
func ParseHostapd(conf string) []BSS {
	var out []BSS
	var cur map[string]string
	flush := func() {
		if cur == nil {
			return
		}
		b := BSS{Iface: cur["iface"], Bridge: cur["ft_iface"]}
		if b.Bridge == "" {
			b.Bridge = cur["bridge"]
		}
		md, err1 := hex.DecodeString(cur["mobility_domain"])
		bssid, err2 := ParseMAC(cur["bssid"])
		key, err3 := hex.DecodeString(cur["key"])
		if b.Iface == "" || b.Bridge == "" || err1 != nil || len(md) != 2 || err2 != nil || err3 != nil || len(key) != 16 {
			return
		}
		copy(b.WLAN[:], md)
		b.BSSID, b.Key = bssid, key
		out = append(out, b)
	}
	for _, line := range strings.Split(conf, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		switch k {
		case "interface", "bss":
			flush()
			cur = map[string]string{"iface": v}
		case "bssid", "bridge", "ft_iface", "mobility_domain":
			if cur != nil {
				cur[k] = v
			}
		case "r0kh":
			// r0kh=ff:ff:ff:ff:ff:ff * <key>
			if f := strings.Fields(v); cur != nil && len(f) == 3 && f[0] == "ff:ff:ff:ff:ff:ff" && f[1] == "*" {
				cur["key"] = f[2]
			}
		}
	}
	flush()
	return out
}

// ParseEvent reads a hostapd log line. joined: a client was authorized on
// iface; left: it disconnected.
//
//	... hostapd: phy1-ap0: AP-STA-CONNECTED 02:33:33:33:33:01 auth_alg=ft
func ParseEvent(line string) (iface string, client MAC, joined, ok bool) {
	for _, ev := range []string{"AP-STA-CONNECTED ", "AP-STA-DISCONNECTED "} {
		i := strings.Index(line, ": "+ev)
		if i < 0 {
			continue
		}
		head := strings.Fields(line[:i])
		rest := strings.Fields(line[i+2+len(ev):])
		if len(head) == 0 || len(rest) == 0 {
			return "", client, false, false
		}
		m, err := ParseMAC(rest[0])
		if err != nil {
			return "", client, false, false
		}
		return head[len(head)-1], m, ev == "AP-STA-CONNECTED ", true
	}
	return "", client, false, false
}
