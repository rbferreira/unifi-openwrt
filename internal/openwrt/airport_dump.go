// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// uow -dump on an AirPort: the reading airportScript makes, with what is
// personal replaced, in the shape ParseAirPort reads back. The script reads no
// hostapd config (the passphrases of the WLANs live only in
// apRunDir/VAP.conf, and nothing here opens them), and the scrubber does not
// trust that: only sections it knows are kept, and any line that looks like a
// secret goes whatever section it is in.

// apDumpKeep is the kinds of section airportScript prints ("=== iface wlan10"
// is kind "iface"). A section of another kind is dropped.
var apDumpKeep = map[string]bool{"sys": true, "env": true, "country": true, "vmstat": true, "lan": true,
	"route": true, "arp": true, "fdb": true, "phy": true, "netstat": true, "netpkts": true,
	"radio": true, "iface": true, "sta": true, "noise": true, "counters": true, "assoc": true}

var (
	// a MAC as the device prints it, with or without the leading zeros (arp: 2:bb:0:0:0:1)
	apMACRE    = regexp.MustCompile(`\b[0-9a-fA-F]{1,2}(?::[0-9a-fA-F]{1,2}){5}\b`)
	apSSIDRE   = regexp.MustCompile(`\bssid ("[^"]*"|\S+)`)
	apInet6RE  = regexp.MustCompile(`\binet6 \S+`)
	apSecretRE = regexp.MustCompile(`(?i)pass(phrase|word|wd)|wpa_psk|\bpsk=|secret|\bkey=|private`)
)

// AirPortScrub removes what is personal from airportScript output: MAC, IPv4
// and IPv6 addresses (a link-local IPv6 address carries the MAC), SSIDs and the
// hostname are replaced, the same value the same way each time, so what belongs
// together still does; sections it does not know and lines that look like a
// secret are dropped or redacted.
func AirPortScrub(out []byte) []byte {
	s := &scrubber{mac: map[string]string{}, ip: map[string]string{}, ssid: map[string]string{}}
	secs := sections(out)
	var b bytes.Buffer
	for _, name := range secs.order {
		kind := strings.Fields(name + " x")[0]
		if !apDumpKeep[kind] {
			continue
		}
		fmt.Fprintf(&b, "=== %s\n", s.text(name))
		for _, line := range strings.Split(strings.TrimRight(string(secs.get(name)), "\n"), "\n") {
			b.WriteString(s.airportLine(kind, line))
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

func (s *scrubber) airportLine(kind, line string) string {
	if apSecretRE.MatchString(line) {
		return "REDACTED"
	}
	if kind == "sys" {
		if _, ok := strings.CutPrefix(line, "hostname="); ok {
			return "hostname=airport"
		}
	}
	line = apInet6RE.ReplaceAllStringFunc(line, func(m string) string {
		// keep the interface scope ("fe80::1%bridge0")
		if _, scope, ok := strings.Cut(m, "%"); ok {
			return "inet6 fe80::1%" + scope
		}
		return "inet6 fe80::1"
	})
	line = apSSIDRE.ReplaceAllStringFunc(line, func(m string) string {
		v := strings.TrimPrefix(m, "ssid ")
		quoted := strings.HasPrefix(v, `"`)
		v = strings.Trim(v, `"`)
		n, ok := s.ssid[v]
		if !ok {
			n = fmt.Sprintf("ssid%d", len(s.ssid)+1)
			s.ssid[v] = n
		}
		if quoted {
			return `ssid "` + n + `"`
		}
		return "ssid " + n
	})
	line = apMACRE.ReplaceAllStringFunc(line, func(m string) string { return s.newMAC(normMAC(m)) })
	return ipv4RE.ReplaceAllStringFunc(line, s.newIP)
}
