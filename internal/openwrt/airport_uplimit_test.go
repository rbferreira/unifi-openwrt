// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"reflect"
	"strings"
	"testing"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// The upload limit of a WLAN on a VLAN is a cap on vlanN; on the LAN it is noted.
func TestAirPortUploadLimit(t *testing.T) {
	vaps := []AirPortVAP{
		{Name: "wlan10", SSID: "Home", Devname: "ath0"},
		{Name: "wlan12", SSID: "Lab", Devname: "ath1", VLAN: 77, UpIf: "bcmeth0"},
		{Name: "wlan21", SSID: "Lab", Devname: "ath2", VLAN: 77, UpIf: "bcmeth0"},
		{Name: "wlan22", SSID: "Other", Devname: "ath3", VLAN: 30, UpIf: "bcmeth0"},
	}
	got, notes := AirPortLimits([]translate.VAPLimit{
		{Devname: "ath0", UpKbps: 500},
		{Devname: "ath1", DownKbps: 2000, UpKbps: 1000},
		{Devname: "ath2", DownKbps: 2000, UpKbps: 400},
		{Devname: "ath3", UpKbps: 2000000},
	}, vaps)
	want := []PFLimit{
		{VAP: "wlan12", DownKbps: 2000},
		{VAP: "wlan21", DownKbps: 2000},
		{VLAN: 77, UpKbps: 400}, // the lowest of the VLAN's WLANs
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("limits %+v, want %+v", got, want)
	}
	if !apNotesHave(notes, "upload limit: a cap on the VLAN's interface") || !apNotesHave(notes, `upload limit ignored on WLAN(s) "Home"`) {
		t.Errorf("notes %q", notes)
	}

	s, err := AirPortPFScript(got, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "\nulim vlan77 400Kb\n") || !strings.Contains(s, "lim wlan12 2000Kb\n") {
		t.Errorf("script calls missing:\n%s", s[len(apPFHead):])
	}
	if strings.Contains(s, "wlan9") || strings.Contains(s, "bcmeth") {
		t.Error("the script names an uplink interface")
	}

	for name, bad := range map[string][]PFLimit{
		"VLAN 1":         {{VLAN: 1, UpKbps: 100}},
		"VLAN 4095":      {{VLAN: 4095, UpKbps: 100}},
		"no rate":        {{VLAN: 77}},
		"rate too high":  {{VLAN: 77, UpKbps: apPFMaxKbps}},
		"the VLAN twice": {{VLAN: 77, UpKbps: 100}, {VLAN: 77, UpKbps: 200}},
	} {
		if _, err := AirPortPFScript(bad, nil, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
