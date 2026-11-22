// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"strings"
	"testing"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// envstat as NetBSD prints it, with the values gaps-device.md I measured. The
// column layout is the usual envstat one, not a capture: confirm on the unit.
const apEnvSample = `                       Current  CritMax  WarnMax  WarnMin  CritMin  Unit
[acpd0]
          T_internal:     48.100                                       degC
         T_external1:     42.600                                       degC
         T_external2:     53.800                                       degC
         T_external3:     55.000                                       degC
                fan0:      1772                                        RPM
            fan_duty:        57                                          %
             accel_x:         0                                          mg
        accel_temp:        28.000                                       degC
`

func TestParseEnvstat(t *testing.T) {
	th := parseEnvstat(apEnvSample)
	if len(th.Temps) != 5 || len(th.Fans) != 1 || th.Fans[0].Value != 1772 || !th.HasDuty || th.Duty != 57 {
		t.Fatalf("%+v", th)
	}
	if deg, ok := th.Hottest(); !ok || deg != 55 {
		t.Errorf("hottest %d %v (the T_ sensors only: the accelerometer's must not count)", deg, ok)
	}
	// without any T_ sensor every temperature counts
	if deg, ok := parseEnvstat("board:  31.6 degC\n").Hottest(); !ok || deg != 32 {
		t.Errorf("fallback %d %v", deg, ok)
	}
	for name, in := range map[string]string{
		"empty":           "",
		"headers only":    "Current CritMax\n[acpd0]\n",
		"not a number":    "T_internal: n/a degC\n",
		"sentinel":        "T_internal: -273.15 degC\n",
		"fan without rpm": "fan0: 12 Hz\n",
	} {
		th := parseEnvstat(in)
		if _, ok := th.Hottest(); ok || len(th.Fans) != 0 {
			t.Errorf("%s: %+v", name, th)
		}
	}
	if th := parseEnvstat("fan_duty: 140 %\n"); th.HasDuty {
		t.Errorf("a duty above 100 percent was taken: %+v", th)
	}
}

func TestParseCountryAndFirmware(t *testing.T) {
	for in, want := range map[string]string{"BR (BR/0) Brazil\n": "BR", "US (US/7)": "US", "": "", "Unsupported\n": "", "wl: error": ""} {
		if got := parseCountry(in); got != want {
			t.Errorf("country %q -> %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"syVs=7.9.1": "7.9.1", "syVs = 7.9.1\n": "7.9.1", "7.9.1": "7.9.1", "": "", "get syVs failed: no": ""} {
		if got := parseFirmware(in); got != want {
			t.Errorf("firmware %q -> %q, want %q", in, got, want)
		}
	}
}

func TestAirPortCollectReadsSensorsCountryFirmware(t *testing.T) {
	for _, want := range []string{`/usr/bin/acp syVs`, `=== env"; envstat`, `=== country"; wl -i bwl1 country`} {
		if !strings.Contains(airportScript, want) {
			t.Errorf("the collect script lacks %q", want)
		}
	}
	// none of the forbidden commands crept in
	for _, bad := range []string{"chanim", "wl -i bwl0 scan", " scan\n", "wl -i bwl1 scan", "wl dump", "wlan9"} {
		if strings.Contains(airportScript, bad) {
			t.Errorf("the collect script contains %q", bad)
		}
	}
	text := strings.Replace(airportSample, "model=syAM=TimeCapsule8,119\n", "model=syAM=TimeCapsule8,119\nfirmware=syVs=7.9.1\n", 1) +
		"=== env\n" + apEnvSample + "=== country\nBR (BR/0) Brazil\n"
	s, err := ParseAirPort([]byte(text), time.Unix(1000200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if s.Firmware != "7.9.1" || s.Country != "BR" || len(s.Thermal.Temps) != 5 {
		t.Fatalf("firmware %q country %q thermal %+v", s.Firmware, s.Country, s.Thermal)
	}
	f := s.Fields(nil, CfgInfo{}, nil)
	if f["has_temperature"] != true || f["general_temperature"] != 55 || f["has_fan"] != true || f["fan_level"] != 57 || f["airport_firmware"] != "7.9.1" {
		t.Errorf("fields: temp %v/%v fan %v/%v fw %v", f["has_temperature"], f["general_temperature"], f["has_fan"], f["fan_level"], f["airport_firmware"])
	}
	if _, has := f["version"]; has {
		t.Error("Fields must leave the version the controller reads alone")
	}
	// a unit that reads none of it sends none of it
	bare, err := ParseAirPort([]byte(airportSample), time.Unix(1000200, 0))
	if err != nil {
		t.Fatal(err)
	}
	f = bare.Fields(nil, CfgInfo{}, nil)
	for _, k := range []string{"has_temperature", "general_temperature", "has_fan", "fan_level", "airport_firmware"} {
		if _, has := f[k]; has {
			t.Errorf("%s sent without a reading", k)
		}
	}
}

func TestAirPortWifiCaps(t *testing.T) {
	const acPro = 559857373 // the U7PG2's word, WPA3 included
	if acPro&wifiCapWPA3 == 0 {
		t.Fatal("the profile word does not carry the WPA3 bit: the test is moot")
	}
	got := AirPortWifiCaps(acPro)
	if got&wifiCapWPA3 != 0 || got != acPro-wifiCapWPA3 {
		t.Errorf("%d -> %d: only the WPA3 bit may go", acPro, got)
	}
	if AirPortWifiCaps(0) != 0 || AirPortWifiCaps(got) != got {
		t.Error("not idempotent or not zero-safe")
	}
}

func TestAirPortCountryNotes(t *testing.T) {
	w := func(cc ...string) translate.Wanted {
		var x translate.Wanted
		for _, c := range cc {
			x.Radios = append(x.Radios, translate.WantedRadio{Country: c})
		}
		return x
	}
	for _, tc := range []struct {
		w    translate.Wanted
		real string
		want []string // substrings of the one note; nil = no note
	}{
		{w("BR", "BR"), "BR", nil},
		{w("", ""), "BR", nil}, // the controller said nothing
		{w(), "BR", nil},
		{w("US", "US"), "BR", []string{"country US requested", "keep using BR"}},
		{w("US", "DE"), "BR", []string{"country US, DE requested"}},
		{w("US"), "", []string{"country US requested", "using its own"}},
	} {
		got := AirPortCountryNotes(tc.w, tc.real)
		if tc.want == nil {
			if len(got) != 0 {
				t.Errorf("%+v %q: %q", tc.w, tc.real, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Errorf("%+v %q: %q", tc.w, tc.real, got)
			continue
		}
		for _, s := range tc.want {
			if !strings.Contains(got[0], s) {
				t.Errorf("%q lacks %q", got[0], s)
			}
		}
	}
}
