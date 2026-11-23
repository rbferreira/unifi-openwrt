// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A dump must hold nothing personal and still read like the original.
func TestScrub(t *testing.T) {
	b, err := os.ReadFile("testdata/onhub-collect.txt")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b) + `=== wstatus
{"radio0": {"up": true, "config": {"band": "2g", "channel": "6", "htmode": "HT20", "note": "kitchen"},
 "interfaces": [{"ifname": "phy1-ap0", "config": {"ssid": "HomeWiFi", "key": "hunter2hunter2", "encryption": "psk2", "macaddr": "0A:11:22:33:44:55"}}]}}
=== fdb
0a1122334455 1 0 120 0
=== lldp
not json at all, 0a:11:22:33:44:55 hunter2
=== hostapd phy1-ap0
{"freq": 2437, "clients": {"02:00:00:00:00:06": {"auth": true, "ht": true, "vht": false, "signature": "wifi4|probe:0,1,45"}}}
`
	want, err := Parse([]byte(raw), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	out := string(Scrub([]byte(raw)))
	for _, bad := range []string{"HomeWiFi", "hunter2", "kitchen", `"hostname": "onhub"`, "0a1122334455", "not json", "wifi4|probe"} {
		if strings.Contains(out, bad) {
			t.Errorf("dump still holds %q", bad)
		}
	}
	// (the test data's own addresses are made up already, in the same form)
	if strings.Contains(strings.ToLower(out), "0a:11:22:33:44:55") {
		t.Error("dump still holds the MAC")
	}
	if n := len(regexp.MustCompile(`"key": "REDACTED"`).FindAllString(out, -1)); n != 1 {
		t.Errorf("password: %d redactions, want 1", n)
	}
	got, err := Parse([]byte(out), time.Unix(0, 0))
	if err != nil {
		t.Fatalf("the dump does not parse: %v", err)
	}
	if got.Model != want.Model || got.Release != want.Release || got.Hostname != "OpenWrt" || got.MAC == "" {
		t.Errorf("identity: %q %q %q %s", got.Model, got.Release, got.Hostname, got.MAC)
	}
	if len(got.Radios) != len(want.Radios) || len(got.Ports) != len(want.Ports) || len(got.ARP) != len(want.ARP) || len(got.FDB) != len(want.FDB) {
		t.Fatalf("radios %d/%d ports %d/%d arp %d/%d fdb %d/%d", len(got.Radios), len(want.Radios),
			len(got.Ports), len(want.Ports), len(got.ARP), len(want.ARP), len(got.FDB), len(want.FDB))
	}
	for i, r := range got.Radios {
		w := want.Radios[i]
		if r.Section != w.Section || r.Band != w.Band || r.Channel != w.Channel || r.Width != w.Width || r.NSS != w.NSS ||
			r.HWName != w.HWName || len(r.VAPs) != len(w.VAPs) || len(r.VAPs[0].Stations) != len(w.VAPs[0].Stations) {
			t.Errorf("radio %d: %+v, want %+v", i, r, w)
		}
		for j, st := range r.VAPs[0].Stations {
			if w := w.VAPs[0].Stations[j]; st.HT != w.HT || st.VHT != w.VHT {
				t.Errorf("radio %d station %d: hostapd flags %v/%v, want %v/%v", i, j, st.HT, st.VHT, w.HT, w.VHT)
			}
		}
	}
	// the same address gets the same replacement everywhere
	if got.Radios[0].VAPs[0].SSID != "ssid1" {
		t.Errorf("ssid: %q", got.Radios[0].VAPs[0].SSID)
	}
	if again := string(Scrub([]byte(raw))); again != out {
		t.Error("two dumps of one reading differ")
	}
}
