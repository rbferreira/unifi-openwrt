// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const wantedRadios = `radio.1.phyname=wifi0
radio.1.ieee_mode=11nght20
radio.1.channel=6
radio.1.txpower=17
radio.1.countrycode=840
radio.2.phyname=wifi1
radio.2.ieee_mode=11acvht80
radio.2.channel=auto
radio.2.txpower=auto
radio.2.countrycode=840
`

func wlanBy(t *testing.T, w Wanted, n string) WantedWLAN {
	t.Helper()
	for _, l := range w.WLANs {
		if l.N == n {
			return l
		}
	}
	t.Fatalf("no WLAN with N=%s in %+v (notes %q)", n, w.WLANs, w.Notes)
	return WantedWLAN{}
}

func hasNote(w Wanted, sub string) bool {
	for _, n := range w.Notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func TestWifiDualBandWLAN(t *testing.T) {
	w := Wifi(ParseCfg(wantedRadios+`wireless.1.id=abc123
wireless.1.ssid=Casa
wireless.1.parent=wifi0
wireless.1.devname=wifi0ap0
aaa.1.wpa=2
aaa.1.wpa.key.1.mgmt=WPA-PSK
aaa.1.wpa.psk=hunter2-test
wireless.2.id=abc123
wireless.2.ssid=Casa
wireless.2.parent=wifi1
wireless.2.devname=wifi1ap1
aaa.2.wpa=2
aaa.2.wpa.key.1.mgmt=WPA-PSK
aaa.2.wpa.psk=hunter2-test
`), nil)
	if len(w.WLANs) != 2 {
		t.Fatalf("WLANs = %+v", w.WLANs)
	}
	a, b := w.WLANs[0], w.WLANs[1]
	if a.N != "1" || b.N != "2" {
		t.Errorf("order: %s, %s", a.N, b.N)
	}
	if a.SSID != b.SSID || a.ID != b.ID || a.ID != "abc123" {
		t.Errorf("same SSID and id expected: %+v %+v", a, b)
	}
	if a.Radio != "wifi0" || b.Radio != "wifi1" || a.Devname != "wifi0ap0" || b.Devname != "wifi1ap1" {
		t.Errorf("radio/devname: %+v %+v", a, b)
	}
}

func TestWifiSecurity(t *testing.T) {
	for _, tc := range []struct {
		name, aaa string
		sec, km   string
		pmf       int
		psk       string
	}{
		{"open", "", "none", "", 0, ""},
		{"wpa2", `aaa.1.wpa=2
aaa.1.wpa.key.1.mgmt=WPA-PSK
aaa.1.wpa.psk=pass-wpa2-x
`, "psk2", "WPA-PSK", 0, "pass-wpa2-x"},
		{"wpa3", `aaa.1.wpa=2
aaa.1.wpa.key.1.mgmt=SAE
aaa.1.pmf.status=enabled
aaa.1.pmf.mode=2
aaa.1.wpa.psk=pass-wpa3-x
`, "sae", "SAE", 2, "pass-wpa3-x"},
		{"transition", `aaa.1.wpa=2
aaa.1.wpa.key.1.mgmt=SAE
aaa.1.wpa3.transition=enabled
aaa.1.pmf.status=enabled
aaa.1.pmf.mode=1
aaa.1.wpa.psk=pass-mix-xx
`, "sae-mixed", "SAE WPA-PSK", 1, "pass-mix-xx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := Wifi(ParseCfg(wantedRadios+"wireless.1.ssid=Net\nwireless.1.parent=wifi0\n"+tc.aaa), nil)
			l := wlanBy(t, w, "1")
			if l.Security != tc.sec || l.KeyMgmt != tc.km || l.PMF != tc.pmf || l.Passphrase != tc.psk {
				t.Errorf("got %+v", l)
			}
		})
	}
}

func TestWifiFlags(t *testing.T) {
	w := Wifi(ParseCfg(wantedRadios+`wireless.1.ssid=Quieta
wireless.1.parent=wifi0
wireless.1.hide_ssid=true
wireless.1.l2_isolation=enabled
wireless.2.ssid=Off
wireless.2.parent=wifi0
wireless.2.status=disabled
wireless.3.ssid=Visitas
wireless.3.parent=wifi1
aaa.3.is_guest=true
wireless.4.ssid=Aaa-off
wireless.4.parent=wifi1
aaa.4.status=disabled
`), nil)
	if l := wlanBy(t, w, "1"); !l.Hidden || !l.Isolate || l.Disabled || l.Guest {
		t.Errorf("hidden+isolate: %+v", l)
	}
	if l := wlanBy(t, w, "2"); !l.Disabled || l.Hidden {
		t.Errorf("disabled must stay listed with the flag: %+v", l)
	}
	if l := wlanBy(t, w, "3"); !l.Guest || l.Hidden || l.Disabled {
		t.Errorf("guest: %+v", l)
	}
	if l := wlanBy(t, w, "4"); !l.Disabled {
		t.Errorf("aaa status disabled: %+v", l)
	}
}

func TestWifiAdopt(t *testing.T) {
	w := Wifi(ParseCfg(wantedRadios+`wireless.1.ssid=Normal
wireless.1.parent=wifi0
wireless.2.ssid=Adoption
wireless.2.parent=wifi0
wireless.2.element_adopt=enabled
wireless.3.ssid=Off
wireless.3.parent=wifi0
wireless.3.element_adopt=disabled
`), nil)
	for n, want := range map[string]bool{"1": false, "2": true, "3": false} {
		if l := wlanBy(t, w, n); l.Adopt != want {
			t.Errorf("wireless.%s: Adopt = %v, want %v", n, l.Adopt, want)
		}
	}
}

func TestWifiSkips(t *testing.T) {
	w := Wifi(ParseCfg(wantedRadios+`wireless.1.ssid=mesh-up
wireless.1.parent=wifi1
wireless.1.usage=uplink
wireless.2.ssid=vw
wireless.2.parent=wifi1
wireless.2.vwire=enabled
wireless.3.ssid=vp
wireless.3.parent=wifi1
wireless.3.vport=enabled
wireless.4.ssid=Wep
wireless.4.parent=wifi0
aaa.4.security=wep128
aaa.4.wpa.psk=
wireless.5.ssid=Corp
wireless.5.parent=wifi0
aaa.5.wpa=2
aaa.5.wpa.key.1.mgmt=WPA-EAP
aaa.5.wpa.psk=corp-secret-1
wireless.6.ssid=Owe
wireless.6.parent=wifi0
aaa.6.wpa3.support=enabled
wireless.7.ssid=Ghost
wireless.7.parent=wifi9
aaa.7.wpa=2
aaa.7.wpa.key.1.mgmt=WPA-PSK
aaa.7.wpa.psk=ghost-secret-1
wireless.8.ssid=Fine
wireless.8.parent=wifi0
`), nil)
	if len(w.WLANs) != 1 || w.WLANs[0].N != "8" {
		t.Fatalf("only wireless.8 should remain: %+v", w.WLANs)
	}
	for _, sub := range []string{"wireless.1:", "wireless.2:", "wireless.3:", "wireless.4 ", "wireless.5 ", "wireless.6 ", "wireless.7 "} {
		if !hasNote(w, sub) {
			t.Errorf("no note for %q in %q", sub, w.Notes)
		}
	}
	if !hasNote(w, "WEP") || !hasNote(w, "WPA-EAP") || !hasNote(w, "OWE") || !hasNote(w, "radio not on this AP") {
		t.Errorf("notes: %q", w.Notes)
	}
}

func TestWifiNoPassphraseInNotes(t *testing.T) {
	cfg := wantedRadios + `wireless.1.ssid=Corp
wireless.1.parent=wifi0
aaa.1.wpa=2
aaa.1.wpa.key.1.mgmt=WPA-EAP
aaa.1.wpa.psk=corp-secret-1
wireless.2.ssid=Ghost
wireless.2.parent=wifi9
aaa.2.wpa=2
aaa.2.wpa.key.1.mgmt=WPA-PSK
aaa.2.wpa.psk=ghost-secret-2
wireless.3.ssid=NoKm
wireless.3.parent=wifi0
aaa.3.wpa=2
aaa.3.wpa.psk=nokm-secret-3
aaa.3.ft.status=enabled
wireless.4.ssid=Ok
wireless.4.parent=wifi0
aaa.4.wpa=2
aaa.4.wpa.key.1.mgmt=WPA-PSK
aaa.4.wpa.psk=ok-secret-4
aaa.4.ft.status=enabled
`
	w := Wifi(ParseCfg(cfg), nil)
	if len(w.Notes) == 0 {
		t.Fatal("expected notes")
	}
	for _, n := range w.Notes {
		for _, s := range []string{"corp-secret-1", "ghost-secret-2", "nokm-secret-3", "ok-secret-4"} {
			if strings.Contains(n, s) {
				t.Errorf("passphrase in note: %q", n)
			}
		}
	}
	if l := wlanBy(t, w, "3"); l.Security != "psk2" || l.Passphrase != "nokm-secret-3" {
		t.Errorf("no key mgmt listed should read as WPA2: %+v", l)
	}
	if l := wlanBy(t, w, "4"); strings.Contains(l.KeyMgmt, "FT-") {
		t.Errorf("FT must not be asked for: %+v", l)
	}
	if !hasNote(w, "802.11r") {
		t.Errorf("802.11r request not noted: %q", w.Notes)
	}
}

func TestWifiSecuredWithoutPassphraseIsLeftOut(t *testing.T) {
	w := Wifi(ParseCfg(wantedRadios+`wireless.1.ssid=Empty
wireless.1.parent=wifi0
aaa.1.wpa=2
aaa.1.wpa.key.1.mgmt=WPA-PSK
`), nil)
	if len(w.WLANs) != 0 || !hasNote(w, "no passphrase") {
		t.Errorf("wlans %+v notes %q", w.WLANs, w.Notes)
	}
}

func TestWifiMACList(t *testing.T) {
	w := Wifi(ParseCfg(wantedRadios+`wireless.1.ssid=Casa
wireless.1.parent=wifi0
wireless.1.devname=wifi0ap0
wireless.2.ssid=Outra
wireless.2.parent=wifi1
wireless.2.devname=wifi1ap1
macacl.status=enabled
macacl.1.devname=wifi0ap0
macacl.1.status=enabled
macacl.1.acl.status=enabled
macacl.1.acl.policy=deny
macacl.1.acl.1.mac=AA:BB:CC:00:11:22
macacl.1.acl.2.mac=aa:bb:cc:00:11:33
macacl.1.acl.3.mac=aa:bb:cc:00:11:44
macacl.1.acl.3.status=disabled
`), nil)
	l := wlanBy(t, w, "1")
	if l.MACPolicy != "deny" || !reflect.DeepEqual(l.MACs, []string{"aa:bb:cc:00:11:22", "aa:bb:cc:00:11:33"}) {
		t.Errorf("deny list: %+v", l)
	}
	if o := wlanBy(t, w, "2"); o.MACPolicy != "disable" || len(o.MACs) != 0 {
		t.Errorf("other WLAN has no filter: %+v", o)
	}
}

func TestWifiVLAN(t *testing.T) {
	w := Wifi(ParseCfg(wantedRadios+`wireless.1.ssid=Casa
wireless.1.parent=wifi0
wireless.1.devname=wifi0ap0
wireless.2.ssid=Iot
wireless.2.parent=wifi0
wireless.2.devname=wifi0ap5
vlan.1.devname=eth0
vlan.1.id=77
bridge.1.devname=br0
bridge.1.port.1.devname=wifi0ap0
bridge.2.devname=br0.77
bridge.2.port.1.devname=wifi0ap5
bridge.2.port.3.devname=eth0.77
`), nil)
	if v := wlanBy(t, w, "1").VLAN; v != 0 {
		t.Errorf("untagged WLAN has VLAN %d", v)
	}
	if v := wlanBy(t, w, "2").VLAN; v != 77 {
		t.Errorf("VLAN = %d, want 77", v)
	}
}

func TestWifiRadios(t *testing.T) {
	cfg := ParseCfg(`radio.countrycode=76
radio.1.phyname=wifi0
radio.1.ieee_mode=11nght40
radio.1.channel=11
radio.1.txpower=14
radio.2.phyname=wifi1
radio.2.ieee_mode=11naht80
radio.2.channel=auto
radio.2.txpower=auto
radio.2.status=disabled
radio.3.phyname=wifi2
radio.3.ieee_mode=11axhe160
radio.3.channel=100
radio.3.countrycode=840
radio.10.phyname=wifi3
radio.10.ieee_mode=11beeht320
radio.10.countrycode=999
`)
	w := Wifi(cfg, nil)
	want := []WantedRadio{
		{Name: "wifi0", Band: "2g", Channel: 11, Width: 40, Country: "BR", TxPower: 14},
		{Name: "wifi1", Band: "5g", Disabled: true, Width: 80, Country: "BR"},
		{Name: "wifi2", Band: "5g", Channel: 100, Width: 160, Country: "US"},
		{Name: "wifi3", Band: "5g", Width: 320},
	}
	if !reflect.DeepEqual(w.Radios, want) {
		t.Errorf("radios\n got %+v\nwant %+v", w.Radios, want)
	}
	if !hasNote(w, `"999"`) {
		t.Errorf("unknown country not noted: %q", w.Notes)
	}

	if got := modeWidth("11nght20"); got != 20 {
		t.Errorf("11nght20 = %d", got)
	}
	if got := modeWidth(""); got != 0 {
		t.Errorf("no mode = %d", got)
	}
}

// Width must agree with the uci mode Plan picks for the same ieee_mode.
func TestWifiWidthAgreesWithHTMode(t *testing.T) {
	for _, m := range []string{"11nght20", "11nght40", "11naht40", "11naht80", "11acvht80", "11acvht160", "11axhe20", "11axhe80", "11axhe160", "11beeht160"} {
		ht := htmode(m, "")
		if want := strings.TrimLeft(ht, "HTVEH"); want != strconv.Itoa(modeWidth(m)) {
			t.Errorf("%s: htmode %s, width %d", m, ht, modeWidth(m))
		}
	}
}

func TestWifiPhysOverridesBand(t *testing.T) {
	cfg := ParseCfg(`radio.1.phyname=wifi0
radio.1.ieee_mode=11nght20
radio.2.phyname=wifi1
radio.2.ieee_mode=11naht80
radio.3.phyname=wifi7
radio.3.ieee_mode=11naht80
wireless.1.ssid=A
wireless.1.parent=wifi1
wireless.2.ssid=B
wireless.2.parent=wifi7
`)
	w := Wifi(cfg, map[string]string{"wifi0": "5g", "wifi1": "6g"})
	if len(w.Radios) != 2 || w.Radios[0].Band != "5g" || w.Radios[1].Band != "6g" {
		t.Errorf("radios: %+v", w.Radios)
	}
	if len(w.WLANs) != 1 || w.WLANs[0].SSID != "A" {
		t.Errorf("a WLAN on an unreported radio must be left out: %+v", w.WLANs)
	}
	if !hasNote(w, "wifi7") {
		t.Errorf("notes: %q", w.Notes)
	}
}

func TestWifiIndexOrderIsNumeric(t *testing.T) {
	w := Wifi(ParseCfg(wantedRadios+`wireless.10.ssid=Ten
wireless.10.parent=wifi0
wireless.2.ssid=Two
wireless.2.parent=wifi0
wireless.1.ssid=One
wireless.1.parent=wifi0
`), nil)
	var got []string
	for _, l := range w.WLANs {
		got = append(got, l.N)
	}
	if !reflect.DeepEqual(got, []string{"1", "2", "10"}) {
		t.Errorf("order %v", got)
	}
}

func TestWifiSSIDFallsBackToAAA(t *testing.T) {
	w := Wifi(ParseCfg(wantedRadios+`wireless.1.parent=wifi0
aaa.1.ssid=FromAAA
`), nil)
	if l := wlanBy(t, w, "1"); l.SSID != "FromAAA" {
		t.Errorf("%+v", l)
	}
}
