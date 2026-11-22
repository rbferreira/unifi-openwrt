// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Neighbour scan on an AirPort: `wl -i RADIO scan` starts a scan that the
// driver runs on its own (rc=0 at once), and about eight seconds later
// `wl -i RADIO scanresults` prints what it found (gaps-device.md D: measured on
// 2.4 GHz with clients on the radio, no loss). The result feeds the same
// scan_table / rogue-AP path as `iw scan` does on OpenWrt, through BSS and
// ScanResult.
//
// Two hard rules of this device, kept in the script builder and not left to
// the caller: the 5 GHz radio (bwl0) is never scanned while the mesh uplink is
// on it (the scan takes the uplink off its channel); there only the results the
// driver already holds are read. And nothing here runs chanim_stats or any
// other dump-style `wl` command.

// Timing of one scan: the wait before the first read and the retries after it.
// Variables so a test does not sleep.
var (
	apScanWait  = 8 * time.Second
	apScanRetry = 3 * time.Second
	apScanTries = 3
)

// apScanPhy matches the two radios, the names ParseAirPort gives Radio.Phy
// (bwl1 = 2.4 GHz, bwl0 = 5 GHz).
var apScanPhy = regexp.MustCompile(`^bwl[01]$`)

// ScanMode is what to do with one radio this time.
type ScanMode int

const (
	ScanSkip   ScanMode = iota // leave the radio alone
	ScanCached                 // read the results the driver already holds, no scan
	ScanActive                 // scan, then read
)

// AirPortScanMode decides what is safe for a radio. 2.4 GHz is scanned whenever
// asked (measured with clients on it). 5 GHz: never with the mesh uplink on it
// (uplink); with clients it follows OpenWrt's rule (background scans leave such
// a radio alone, only a scan the controller asked for, force, goes off channel).
func AirPortScanMode(phy string, uplink, clients, force bool) ScanMode {
	switch {
	case !apScanPhy.MatchString(phy):
		return ScanSkip
	case phy == "bwl1":
		return ScanActive
	case uplink || (clients && !force):
		return ScanCached
	}
	return ScanActive
}

// airPortScanScripts builds the two scripts of one scan of radio phy: kick (the
// `wl scan`, empty when only cached results are wanted) and read. It refuses a
// scan of bwl0 while the uplink is on it (uplink5 > 0), whatever the caller
// believes.
func airPortScanScripts(phy string, scan bool, uplink5 int) (kick, read string, err error) {
	if !apScanPhy.MatchString(phy) {
		return "", "", fmt.Errorf("scan: bad radio %q", phy)
	}
	if scan && phy == "bwl0" && uplink5 > 0 {
		return "", "", fmt.Errorf("scan: refusing to scan bwl0 while the mesh uplink is on it (channel %d)", uplink5)
	}
	if scan {
		kick = apPATH + "wl -i " + phy + " scan\n"
	}
	read = apPATH + "wl -i " + phy + " scanresults\n"
	return kick, read, nil
}

// ScanAirPort scans radio phy (or only reads the driver's cached results when
// scan is false) and returns the networks found. uplink5 is the mesh uplink's
// channel on the 5 GHz radio, 0 = none. It sleeps for the scan time, so run it
// off the main loop.
func (cl *Client) ScanAirPort(phy string, scan bool, uplink5 int) ([]BSS, error) {
	kick, read, err := airPortScanScripts(phy, scan, uplink5)
	if err != nil {
		return nil, err
	}
	if kick != "" {
		if out, err := cl.runStdin("sh", []byte(kick)); err != nil {
			return nil, fmt.Errorf("scan %s: %v (%s)", phy, err, firstLine(string(out)))
		}
		time.Sleep(apScanWait)
	}
	var bss []BSS
	for i := 0; ; i++ {
		// while the scan still runs the read may fail or come back empty: ask again
		out, err := cl.runStdin("sh", []byte(read))
		bss = ParseAirPortScan(string(out))
		if len(bss) > 0 {
			return bss, nil
		}
		if i+1 >= apScanTries || kick == "" {
			if err != nil {
				return nil, fmt.Errorf("scan %s: %v (%s)", phy, err, firstLine(string(out)))
			}
			return nil, nil
		}
		time.Sleep(apScanRetry)
	}
}

var (
	apScanSSIDRE   = regexp.MustCompile(`^SSID:\s*"(.*)"\s*$`)
	apScanBSSIDRE  = regexp.MustCompile(`^BSSID:\s*([0-9A-Fa-f]{2}(?::[0-9A-Fa-f]{2}){5})`)
	apScanRSSIRE   = regexp.MustCompile(`\bRSSI:\s*(-?\d+)`)
	apScanChanRE   = regexp.MustCompile(`\bChannel:\s*(\d+)`)
	apScanPrimRE   = regexp.MustCompile(`Primary channel:\s*(\d+)`)
	apScanSpecRE   = regexp.MustCompile(`Chanspec:\s*\S+ channel (\d+) (\d+)MHz`)
	apScanEscapeRE = regexp.MustCompile(`\\(?:x[0-9A-Fa-f]{2}|[0-7]{3}|\\)`)
)

// ParseAirPortScan reads `wl scanresults`: one block per network, starting at
// its SSID line,
//
//	SSID: "Example Net"
//	Mode: Managed   RSSI: -60 dBm   SNR: 0 dB   noise: -92 dBm   Flags: RSSI on-channel   Channel: 6
//	BSSID: 02:00:00:00:00:10   Capability: ESS Privacy ShortSlot
//	Supported Rates: [ 1(b) 2(b) 5.5(b) 11(b) 6 9 12 18 24 36 48 54 ]
//	HT Capable:
//	    Chanspec: 2.4GHz channel 6 20MHz (0x1006)
//	    Primary channel: 6
//	RSN:
//	    Unicast Ciphers (1): AES-CCMP
//	    AKM Suites (1): WPA2-PSK
//
// Channel is the primary one (the "Primary channel" line when the block has it,
// else the Mode line's), the width comes from the Chanspec line (20 when there
// is none), and security from the Privacy capability plus the RSN / WPA blocks
// (an 802.1X AKM is wpaeap, any other WPA or WPA2/3 key management is wpapsk,
// Privacy alone is wep). Lines it does not know are skipped, so a driver that
// prints more still parses.
func ParseAirPortScan(out string) []BSS {
	var res []BSS
	idx := map[string]int{}
	var b *BSS
	var privacy, rsn, wpa, eap bool
	var prim, modeCh, specCh, specW int
	section := ""
	flush := func() {
		if b == nil {
			return
		}
		defer func() { b = nil }()
		ch := prim
		if ch == 0 {
			ch = modeCh
		}
		if b.BSSID == "" || ch <= 0 || ch > 233 {
			return
		}
		ref := 5000
		if ch <= 14 {
			ref = 2400
		}
		b.Channel, b.Freq = ch, channelFreq(ch, ref)
		b.Width, b.CenterFreq = 20, b.Freq
		if specW >= 40 {
			b.Width = specW
			// The chanspec may name the centre channel (80 MHz at 36 shows 42) or the
			// control channel; only a number near the control channel is a centre.
			if d := specCh - ch; specCh > 0 && d != 0 && d >= -14 && d <= 14 {
				b.CenterFreq = channelFreq(specCh, ref)
			}
		}
		switch {
		case rsn || wpa:
			b.Security = "wpapsk"
			if eap {
				b.Security = "wpaeap"
			}
		case privacy:
			b.Security = "wep"
		default:
			b.Security = "open"
		}
		if i, ok := idx[b.BSSID]; ok {
			res[i] = *b // a later copy of the same BSS wins
		} else {
			idx[b.BSSID] = len(res)
			res = append(res, *b)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		t := strings.TrimSpace(line)
		if m := apScanSSIDRE.FindStringSubmatch(t); m != nil && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			flush()
			b = &BSS{SSID: unescapeWLSSID(m[1]), AgeMs: -1}
			if strings.Trim(b.SSID, "\x00") == "" {
				b.SSID = "" // hidden network
			}
			privacy, rsn, wpa, eap = false, false, false, false
			prim, modeCh, specCh, specW = 0, 0, 0, 0
			section = ""
			continue
		}
		if b == nil || t == "" {
			continue
		}
		indented := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
		if !indented {
			section = ""
		}
		switch {
		case strings.HasPrefix(t, "Mode:"):
			if m := apScanRSSIRE.FindStringSubmatch(t); m != nil {
				b.Signal, _ = strconv.Atoi(m[1])
			}
			if m := apScanChanRE.FindStringSubmatch(t); m != nil {
				modeCh, _ = strconv.Atoi(m[1])
			}
		case strings.HasPrefix(t, "BSSID:"):
			if m := apScanBSSIDRE.FindStringSubmatch(t); m != nil {
				b.BSSID = strings.ToLower(m[1])
			}
			if _, caps, ok := strings.Cut(t, "Capability:"); ok {
				privacy = strings.Contains(caps, "Privacy")
			}
		case strings.HasPrefix(t, "RSN:"):
			rsn, section = true, "rsn"
		case strings.HasPrefix(t, "WPA:"):
			wpa, section = true, "wpa"
		case strings.HasPrefix(t, "AKM Suites") && (section == "rsn" || section == "wpa"):
			_, akm, _ := strings.Cut(t, ":")
			for _, a := range strings.Fields(strings.NewReplacer(",", " ").Replace(akm)) {
				switch l := strings.ToLower(a); {
				case strings.Contains(l, "802.1x"), strings.Contains(l, "1x"), strings.Contains(l, "eap"),
					l == "wpa", l == "wpa2", l == "wpa2-ft", l == "wpa3":
					eap = true // the bare names are the 802.1X ones: the PSK ones say PSK
				}
			}
		case strings.HasPrefix(t, "Chanspec:"):
			if m := apScanSpecRE.FindStringSubmatch(t); m != nil && specW == 0 {
				specCh, _ = strconv.Atoi(m[1])
				specW, _ = strconv.Atoi(m[2])
			}
		case strings.HasPrefix(t, "Primary channel:"):
			if m := apScanPrimRE.FindStringSubmatch(t); m != nil {
				prim, _ = strconv.Atoi(m[1])
			}
		}
	}
	flush()
	return res
}

// unescapeWLSSID undoes the driver's escaping of an SSID: \ooo (octal), \xNN
// and \\.
func unescapeWLSSID(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	return apScanEscapeRE.ReplaceAllStringFunc(s, func(m string) string {
		switch {
		case m == `\\`:
			return `\`
		case m[1] == 'x':
			v, _ := strconv.ParseUint(m[2:], 16, 8)
			return string([]byte{byte(v)})
		}
		v, _ := strconv.ParseUint(m[1:], 8, 16)
		return string([]byte{byte(v)})
	})
}
