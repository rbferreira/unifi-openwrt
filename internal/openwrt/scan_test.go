// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os"
	"testing"
	"time"
)

func TestParseScan(t *testing.T) {
	b, err := os.ReadFile("testdata/iw-scan.txt")
	if err != nil {
		t.Fatal(err)
	}
	bss := ParseScan(string(b))
	if len(bss) != 6 {
		t.Fatalf("got %d BSS, want 6", len(bss))
	}
	if bss[0].SSID != "" || bss[0].Channel != 6 || bss[0].Security != "wpapsk" || bss[0].Width != 20 || bss[0].AgeMs != 40 {
		t.Errorf("hidden 2.4 GHz: %+v", bss[0])
	}
	if bss[1].SSID != "neighbour2" || bss[1].Channel != 4 {
		t.Errorf("second: %+v", bss[1])
	}
	v := bss[3]
	if v.Freq != 5180 || v.Channel != 36 || v.Width != 80 || v.CenterFreq != 5210 || v.Signal == 0 {
		t.Errorf("5 GHz VHT80: %+v", v)
	}
}

func TestSpectrum(t *testing.T) {
	before := []ChanSurvey{{Freq: 5180, InUse: true, Active: 1000, Busy: 100}}
	after := []ChanSurvey{{Freq: 5180, InUse: true, Active: 1100, Busy: 150, Noise: -101},
		{Freq: 5200, Active: 50, Busy: 10, Noise: -100}, {Freq: 5220, Active: 50, Busy: 0, Noise: -99},
		{Freq: 5240, Active: 50, Busy: 25, Noise: -102}, {Freq: 5260, Active: 0}}
	sp := Spectrum(SurveyDelta(before, after), "na")
	want := map[[2]int]SpectrumEntry{
		{36, 20}: {36, 20, 5180, 50, -101},
		{40, 20}: {40, 20, 5200, 20, -100},
		{36, 40}: {36, 40, 5190, 40, -100},
		{44, 40}: {44, 40, 5230, 25, -99},
		{36, 80}: {36, 80, 5210, 34, -99},
	}
	got := map[[2]int]SpectrumEntry{}
	for _, e := range sp {
		got[[2]int{e.Channel, e.Width}] = e
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%v: got %+v, want %+v", k, got[k], w)
		}
	}
	ng := Spectrum([]ChanSurvey{{Freq: 2412, Active: 32, Busy: 21}, {Freq: 2437, InUse: true, Active: 100, Busy: 30, Noise: -98}}, "ng")
	if len(ng) != 2 || ng[0].Interference != -98 {
		t.Errorf("missing noise -> in-use floor: %+v", ng)
	}
	if _, ok := got[[2]int{52, 20}]; ok {
		t.Error("a channel the scan didn't visit must be left out")
	}
}

func TestScanTable(t *testing.T) {
	now := time.Unix(1000, 0)
	r := ScanResult{Band: "na", Time: now.Add(-5 * time.Second), BSS: []BSS{
		{BSSID: "5e:11:22:33:44:04", SSID: "home", Freq: 5180, Channel: 36, Width: 80, CenterFreq: 5210, Signal: -60, Security: "wpapsk", AgeMs: 2000},
		{BSSID: "aa:bb:cc:dd:ee:01", SSID: "home", Freq: 5180, Signal: -80},
		{BSSID: "aa:bb:cc:dd:ee:02", SSID: "other", Freq: 5200, Channel: 40, Signal: -97},
		{BSSID: "0a:55:66:77:00:02", SSID: "home", Freq: 5180},
		{BSSID: "52:a1:01:14:27:5c", SSID: "home", Freq: 5500, Channel: 100}, // UAP-AC-Pro
	}}
	rows := ScanTable(r, now, map[string]bool{"0a:55:66:77:00:02": true}, map[string]bool{"home": true},
		map[string]string{"40:11:22:33:44:01": "192.168.1.1", "48:a1:01:13:27:5c": ""})
	if len(rows) != 3 {
		t.Fatalf("rows: %v", rows)
	}
	if rows[0]["is_unifi"] != true || rows[0]["serialno"] != "40:11:22:33:44:01" || rows[0]["age"] != 7 || rows[0]["rssi"] != 35 {
		t.Errorf("UniFi sibling: %v", rows[0])
	}
	if rows[1]["essid"] != "other" || rows[1]["is_unifi"] != false || rows[1]["rssi"] != 0 {
		t.Errorf("neighbour: %v", rows[1])
	}
	if rows[2]["is_unifi"] != true || rows[2]["serialno"] != "48:a1:01:13:27:5c" {
		t.Errorf("AC Pro sibling: %v", rows[2])
	}
}

// BSSID/MAC pairs as a controller lists them (stat/device vap_table), with
// made-up addresses that keep each device's pattern.
func TestBSSIDOfDevice(t *testing.T) {
	devices := []struct {
		mac    string
		bssids []string
	}{
		{"48:a1:01:13:27:5c", []string{"48:a1:01:14:27:5c", "48:a1:01:15:27:5c", "52:a1:01:14:27:5c"}}, // UAP-AC-Pro
		{"94:c2:d3:2a:61:08", []string{"9a:c2:d3:2b:61:08", "9e:c2:d3:2b:61:08", "9e:c2:d3:2c:61:08"}}, // UAP-AC-Pro
		{"94:c2:d3:2a:72:77", []string{"9a:c2:d3:2b:72:77", "9a:c2:d3:2c:72:77", "9e:c2:d3:2b:72:77"}}, // UAP-AC-Pro
		{"70:e5:f6:47:9c:3a", []string{"76:e5:f6:47:9c:3b", "76:e5:f6:47:9c:3c", "7a:e5:f6:47:9c:3b"}}, // U7 Lite
		{"08:7a:6b:5c:4d:e1", []string{"08:7a:6b:5c:4d:e2", "0a:7a:6b:5c:4d:e3", "0e:7a:6b:5c:4d:e2"}}, // OpenWrt
	}
	for i, d := range devices {
		for _, b := range d.bssids {
			for j, e := range devices {
				if got := bssidOfDevice(b, e.mac); got != (i == j) {
					t.Errorf("bssidOfDevice(%s, %s) = %v", b, e.mac, got)
				}
			}
		}
	}
	for _, c := range []struct {
		bssid, mac string
		want       bool
	}{
		{"4a:a1:01:13:27:5c", "48:a1:01:13:27:5c", true},  // only byte 0 differs
		{"42:11:22:33:44:01", "40:11:22:33:44:ff", true},  // +2 mod 256
		{"48:a2:01:14:27:5c", "48:a1:01:13:27:5c", false}, // byte 1
		{"48:a1:02:14:27:5c", "48:a1:01:13:27:5c", false}, // byte 2
		{"48:a1:01:14:28:5c", "48:a1:01:13:27:5c", false}, // bytes 3 and 4
		{"48:a1:01:13:28:5d", "48:a1:01:13:27:5c", false}, // bytes 4 and 5
		{"48:a1:01:33:27:5c", "48:a1:01:13:27:5c", false}, // +0x20
		{"48:a1:01:13:27:5b", "48:a1:01:13:27:5c", false}, // -1
		{"x", "48:a1:01:13:27:5c", false},
	} {
		if got := bssidOfDevice(c.bssid, c.mac); got != c.want {
			t.Errorf("bssidOfDevice(%s, %s) = %v, want %v", c.bssid, c.mac, got, c.want)
		}
	}
}

func TestSplitBands(t *testing.T) {
	res := SplitBands([]BSS{{BSSID: "a", Freq: 2412}, {BSSID: "b", Freq: 5745}, {BSSID: "c", Freq: 5180}},
		[]ChanSurvey{{Freq: 2412, Active: 46, Busy: 23, Noise: -105}, {Freq: 5180, Active: 46, Busy: 0, Noise: -100}}, time.Unix(1, 0))
	if len(res["ng"].BSS) != 1 || len(res["na"].BSS) != 2 || len(res["ng"].Spectrum) != 1 || res["ng"].Spectrum[0].Utilization != 50 || len(res["na"].Spectrum) != 1 {
		t.Errorf("split: %+v", res)
	}
}
