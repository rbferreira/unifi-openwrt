// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A `wl scanresults` in the standard Broadcom text format, made up (placeholder
// SSIDs and 02:00:00:.. addresses). NOT a capture from the unit: gaps-device.md
// D records only that the command works. "→" stands for the tab the driver
// prints. Confirm the real layout on the device.
const apScanSample = `SSID: "Example Net"
Mode: Managed→RSSI: -61 dBm→SNR: 0 dB→noise: -92 dBm→Flags: RSSI on-channel →Channel: 6
BSSID: 02:00:00:00:10:01→Capability: ESS Privacy ShortSlot
Supported Rates: [ 1(b) 2(b) 5.5(b) 11(b) 6 9 12 18 24 36 48 54 ]
HT Capable:
→Chanspec: 2.4GHz channel 6 20MHz (0x1006)
→Primary channel: 6
→HT Capabilities: 40Mhz SGI20 SGI40
RSN:
→Version 1
→Multicast Group Cipher Group: AES-CCMP
→Unicast Ciphers (1): AES-CCMP
→AKM Suites (1): WPA2-PSK
→Capabilities(0x000c)
SSID: "Example Corp"
Mode: Managed→RSSI: -47 dBm→SNR: 0 dB→noise: -95 dBm→Flags: RSSI on-channel →Channel: 36
BSSID: 02:00:00:00:10:02→Capability: ESS Privacy SpecMg
Supported Rates: [ 6(b) 9 12(b) 18 24(b) 36 48 54 ]
VHT Capable:
→Chanspec: 5GHz channel 42 80MHz (0xe02a)
→Primary channel: 36
→Supported VHT (tx) Rates: NSS: 3 MCS: 0-9
RSN:
→Version 1
→Unicast Ciphers (1): AES-CCMP
→AKM Suites (1): WPA2
SSID: ""
Mode: Managed→RSSI: -80 dBm→SNR: 0 dB→noise: -92 dBm→Flags: RSSI →Channel: 11
BSSID: 02:00:00:00:10:03→Capability: ESS ShortSlot
Supported Rates: [ 1(b) 2(b) 5.5(b) 11(b) ]
SSID: "Old WEP"
Mode: Managed→RSSI: -70 dBm→SNR: 0 dB→noise: -92 dBm→Flags: RSSI →Channel: 1
BSSID: 02:00:00:00:10:04→Capability: ESS Privacy
SSID: "Caf\303\251 WPA1"
Mode: Managed→RSSI: -75 dBm→SNR: 0 dB→noise: -92 dBm→Flags: RSSI →Channel: 3
BSSID: 02:00:00:00:10:05→Capability: ESS Privacy
WPA:
→Version 1
→AKM Suites (1): WPA-PSK
SSID: "Example Net"
Mode: Managed→RSSI: -55 dBm→SNR: 0 dB→noise: -92 dBm→Flags: RSSI on-channel →Channel: 6
BSSID: 02:00:00:00:10:01→Capability: ESS Privacy ShortSlot
RSN:
→AKM Suites (1): WPA2-PSK
SSID: "No Channel"
Mode: Managed→RSSI: -50 dBm
BSSID: 02:00:00:00:10:06→Capability: ESS
`

func TestParseAirPortScan(t *testing.T) {
	got := ParseAirPortScan(strings.ReplaceAll(apScanSample, "→", "\t"))
	want := []BSS{
		// the same BSSID twice: the later copy wins
		{BSSID: "02:00:00:00:10:01", SSID: "Example Net", Freq: 2437, Channel: 6, Width: 20, CenterFreq: 2437, Signal: -55, Security: "wpapsk"},
		{BSSID: "02:00:00:00:10:02", SSID: "Example Corp", Freq: 5180, Channel: 36, Width: 80, CenterFreq: 5210, Signal: -47, Security: "wpaeap"},
		{BSSID: "02:00:00:00:10:03", SSID: "", Freq: 2462, Channel: 11, Width: 20, CenterFreq: 2462, Signal: -80, Security: "open"},
		{BSSID: "02:00:00:00:10:04", SSID: "Old WEP", Freq: 2412, Channel: 1, Width: 20, CenterFreq: 2412, Signal: -70, Security: "wep"},
		{BSSID: "02:00:00:00:10:05", SSID: "Caf\u00e9 WPA1", Freq: 2422, Channel: 3, Width: 20, CenterFreq: 2422, Signal: -75, Security: "wpapsk"},
	}
	if len(got) != len(want) {
		t.Fatalf("%d networks, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		want[i].AgeMs = -1
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("network %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestParseAirPortScanTolerant(t *testing.T) {
	for name, tc := range map[string]struct {
		in    string
		n     int
		sec   string
		chan_ int
	}{
		"empty":                 {"", 0, "", 0},
		"driver noise only":     {"wl: scan in progress\n", 0, "", 0},
		"windows line endings":  {"SSID: \"A\"\r\nMode: Managed\tRSSI: -40 dBm\tChannel: 44\r\nBSSID: 02:00:00:00:20:01\tCapability: ESS\r\n", 1, "open", 44},
		"primary channel wins":  {"SSID: \"A\"\nMode: Managed RSSI: -40 dBm Channel: 38\nBSSID: 02:00:00:00:20:01 Capability: ESS\n  Primary channel: 36\n", 1, "open", 36},
		"unknown lines skipped": {"SSID: \"A\"\nMode: Managed RSSI: -40 dBm Channel: 1\nBSSID: 02:00:00:00:20:01 Capability: ESS\nWPS:\n  Version: 2.0\nSomething new: 1\n", 1, "open", 1},
		"1x with a comma list":  {"SSID: \"A\"\nMode: Managed RSSI: -40 dBm Channel: 1\nBSSID: 02:00:00:00:20:01 Capability: ESS Privacy\nRSN:\n  AKM Suites (2): WPA2-PSK, WPA2\n", 1, "wpaeap", 1},
		"sae only is psk":       {"SSID: \"A\"\nMode: Managed RSSI: -40 dBm Channel: 1\nBSSID: 02:00:00:00:20:01 Capability: ESS Privacy\nRSN:\n  AKM Suites (1): SAE\n", 1, "wpapsk", 1},
		"no bssid dropped":      {"SSID: \"A\"\nMode: Managed RSSI: -40 dBm Channel: 1\n", 0, "", 0},
	} {
		t.Run(name, func(t *testing.T) {
			got := ParseAirPortScan(tc.in)
			if len(got) != tc.n {
				t.Fatalf("%d networks: %+v", len(got), got)
			}
			if tc.n == 1 && (got[0].Security != tc.sec || got[0].Channel != tc.chan_) {
				t.Errorf("security %q channel %d, want %q %d", got[0].Security, got[0].Channel, tc.sec, tc.chan_)
			}
		})
	}
}

func TestUnescapeWLSSID(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:       "plain",
		`Caf\303\251`: "Caf\u00e9",
		`a\x41b`:      "aAb",
		`back\\slash`: `back\slash`,
		`trailing\`:   `trailing\`,
	} {
		if got := unescapeWLSSID(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestAirPortScanMode(t *testing.T) {
	for _, tc := range []struct {
		phy                    string
		uplink, clients, force bool
		want                   ScanMode
	}{
		{"bwl1", false, false, false, ScanActive},
		{"bwl1", false, true, false, ScanActive}, // measured: clients stay up
		{"bwl1", false, true, true, ScanActive},
		{"bwl0", true, false, false, ScanCached}, // the uplink is on it
		{"bwl0", true, false, true, ScanCached},  // even when the controller asks
		{"bwl0", true, true, true, ScanCached},
		{"bwl0", false, false, false, ScanActive},
		{"bwl0", false, true, false, ScanCached}, // background leaves clients alone, as on OpenWrt
		{"bwl0", false, true, true, ScanActive},
		{"wlan9", false, false, true, ScanSkip},
		{"", false, false, true, ScanSkip},
	} {
		if got := AirPortScanMode(tc.phy, tc.uplink, tc.clients, tc.force); got != tc.want {
			t.Errorf("%+v: %v, want %v", tc, got, tc.want)
		}
	}
}

func TestAirPortScanScripts(t *testing.T) {
	kick, read, err := airPortScanScripts("bwl1", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if kick != "PATH=/bin:/sbin:/usr/bin:/usr/sbin\nwl -i bwl1 scan\n" || read != "PATH=/bin:/sbin:/usr/bin:/usr/sbin\nwl -i bwl1 scanresults\n" {
		t.Errorf("kick %q read %q", kick, read)
	}
	// 5 GHz without the uplink may be scanned; with it, the scan is refused and a
	// cached read is a read and nothing else
	if k, _, err := airPortScanScripts("bwl0", true, 0); err != nil || k == "" {
		t.Errorf("bwl0 without uplink: %q %v", k, err)
	}
	if _, _, err := airPortScanScripts("bwl0", true, 36); err == nil {
		t.Error("a scan of bwl0 with the uplink on it was built")
	}
	k, r, err := airPortScanScripts("bwl0", false, 36)
	if err != nil || k != "" || strings.Contains(r, " scan\n") || !strings.Contains(r, "wl -i bwl0 scanresults") {
		t.Errorf("cached read of bwl0: %q %q %v", k, r, err)
	}
	// nothing but the two radios, and never the forbidden things
	for _, bad := range []string{"wlan9", "bwl1; reboot", "bwl", "wlan10", ""} {
		if _, _, err := airPortScanScripts(bad, true, 0); err == nil {
			t.Errorf("radio %q accepted", bad)
		}
	}
	for _, phy := range []string{"bwl0", "bwl1"} {
		for _, scan := range []bool{true, false} {
			k, r, _ := airPortScanScripts(phy, scan, 0)
			for _, s := range []string{k, r} {
				if strings.Contains(s, "chanim") || strings.Contains(s, "wlan9") || strings.Contains(s, "dump") {
					t.Errorf("forbidden text in %q", s)
				}
			}
		}
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		return
	}
	for name, s := range map[string]string{"kick": kick, "read": read} {
		f := filepath.Join(t.TempDir(), name+".sh")
		if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("sh -n %s: %v\n%s", name, err, out)
		}
	}
}

func TestScanAirPortRefusesWithoutRunningAnything(t *testing.T) {
	// a client for a host that is not there: a refusal must come before any command
	cl := NewClient(Target{Addr: "127.0.0.1:1", Platform: PlatformAirPort})
	old := apScanWait
	apScanWait = time.Millisecond
	defer func() { apScanWait = old }()
	if _, err := cl.ScanAirPort("bwl0", true, 36); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("err = %v", err)
	}
	if _, err := cl.ScanAirPort("wlan9", true, 0); err == nil || !strings.Contains(err.Error(), "bad radio") {
		t.Errorf("err = %v", err)
	}
}

func TestScanAirPortReachesTheScanTable(t *testing.T) {
	bss := ParseAirPortScan(strings.ReplaceAll(apScanSample, "→", "\t"))
	now := time.Unix(1000, 0)
	res := ScanResult{Band: "ng", Time: now, BSS: bss}
	own := map[string]bool{"02:00:00:00:10:05": true}
	rows := ScanTable(res, now, own, map[string]bool{}, nil)
	if len(rows) != len(bss)-1 {
		t.Fatalf("%d rows: %v", len(rows), rows)
	}
	var corp map[string]any
	for _, r := range rows {
		if r["essid"] == "Example Corp" {
			corp = r
		}
	}
	if corp == nil || corp["channel"] != 36 || corp["bw"] != 80 || corp["security"] != "wpaeap" || corp["signal"] != -47 || corp["rssi"] != 48 || corp["center_freq"] != 5210 {
		t.Errorf("row: %v", corp)
	}
}
