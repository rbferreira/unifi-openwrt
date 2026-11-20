// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// These tests check airport_apply.go against design-apply.md, not against what
// the code happens to produce. SSIDs and passphrases are placeholders.

const (
	apTestSSID = "Example Net"
	apTestPass = "correct horse 1"
)

func apRadios() []translate.WantedRadio {
	return []translate.WantedRadio{
		{Name: "wifi0", Band: "2g"},
		{Name: "wifi1", Band: "5g"},
	}
}

func apWLAN(n, radio string) translate.WantedWLAN {
	return translate.WantedWLAN{N: n, Radio: radio, SSID: apTestSSID, Security: "psk2", Passphrase: apTestPass, MACPolicy: "disable"}
}

func apNames(vaps []AirPortVAP) []string {
	var out []string
	for _, v := range vaps {
		out = append(out, v.Name)
	}
	return out
}

func apNotesHave(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// apLines splits a config into its lines.
func apLines(cfg string) []string {
	return strings.Split(strings.TrimSuffix(cfg, "\n"), "\n")
}

func apHasLine(cfg, line string) bool {
	for _, l := range apLines(cfg) {
		if l == line {
			return true
		}
	}
	return false
}

func apHasKey(cfg, key string) bool {
	for _, l := range apLines(cfg) {
		if strings.HasPrefix(l, key+"=") {
			return true
		}
	}
	return false
}

func TestAirPortConfigExact(t *testing.T) {
	// the design's list, line by line
	tests := []struct {
		name    string
		radio   translate.WantedRadio
		wlan    translate.WantedWLAN
		uplink5 int
		vap     string
		want    []string
	}{
		{
			name:  "2.4 GHz WPA2",
			radio: translate.WantedRadio{Name: "wifi0", Band: "2g", Channel: 6},
			wlan:  apWLAN("1", "wifi0"),
			vap:   "wlan10",
			want: []string{
				"interface=wlan10",
				"driver=net80211",
				"logger_syslog=0",
				"logger_syslog_level=3",
				"logger_stdout=-1",
				"logger_stdout_level=3",
				"dump_file=/mnt/Memory/uowap/wlan10.dump",
				"ap_table_max_size=512",
				"ap_table_expiration_time=-1",
				"bridge_packets=1",
				"ssid=Example Net",
				"hide_ssid=0",
				"ampdu=1",
				"stbc=1",
				"rrm=1",
				"phy_mode=6",
				"auth_algs=1",
				"ieee8021x=0",
				"eap_server=0",
				"hw_mode=ng",
				"doth=1",
				"dotd=1",
				"dtim=3",
				"mcast_rate=2",
				"wpa=2",
				"wpa_passphrase=correct horse 1",
				"wpa_key_mgmt=WPA-PSK",
				"rsn_pairwise=CCMP",
				"wpa_group_rekey=3600",
				"wpa_strict_rekey=1",
				"wpa_gmk_rekey=3600",
				"ifparent=bwl1",
				"sku=145",
				"region=76",
				"allow_ht40=0",
				"allow_vht80=0",
				"acs=0",
				"userland_dfs=0",
				"tx_power=100",
				"max_clients=64",
				"channel=6",
			},
		},
		{
			name:    "5 GHz WPA2 hidden, following the uplink",
			radio:   translate.WantedRadio{Name: "wifi1", Band: "5g", Channel: 100},
			wlan:    func() translate.WantedWLAN { l := apWLAN("2", "wifi1"); l.Hidden = true; return l }(),
			uplink5: 40,
			vap:     "wlan20",
			want: []string{
				"interface=wlan20",
				"driver=net80211",
				"logger_syslog=0",
				"logger_syslog_level=3",
				"logger_stdout=-1",
				"logger_stdout_level=3",
				"dump_file=/mnt/Memory/uowap/wlan20.dump",
				"ap_table_max_size=512",
				"ap_table_expiration_time=-1",
				"bridge_packets=1",
				"ssid=Example Net",
				"hide_ssid=1",
				"ampdu=1",
				"stbc=1",
				"rrm=1",
				"phy_mode=10",
				"auth_algs=1",
				"ieee8021x=0",
				"eap_server=0",
				"hw_mode=a",
				"doth=1",
				"dotd=1",
				"dtim=3",
				"mcast_rate=6",
				"wpa=2",
				"wpa_passphrase=correct horse 1",
				"wpa_key_mgmt=WPA-PSK",
				"rsn_pairwise=CCMP",
				"wpa_group_rekey=3600",
				"wpa_strict_rekey=1",
				"wpa_gmk_rekey=3600",
				"ifparent=bwl0",
				"sku=145",
				"region=76",
				"allow_ht40=1",
				"allow_vht80=1",
				"acs=0",
				"userland_dfs=0",
				"tx_power=100",
				"max_clients=64",
				"channel=40",
			},
		},
		{
			name:  "open network, auto channel",
			radio: translate.WantedRadio{Name: "wifi0", Band: "2g"},
			wlan:  translate.WantedWLAN{N: "3", Radio: "wifi0", SSID: apTestSSID, Security: "none", MACPolicy: "disable"},
			vap:   "wlan10",
			want: []string{
				"interface=wlan10",
				"driver=net80211",
				"logger_syslog=0",
				"logger_syslog_level=3",
				"logger_stdout=-1",
				"logger_stdout_level=3",
				"dump_file=/mnt/Memory/uowap/wlan10.dump",
				"ap_table_max_size=512",
				"ap_table_expiration_time=-1",
				"bridge_packets=1",
				"ssid=Example Net",
				"hide_ssid=0",
				"ampdu=1",
				"stbc=1",
				"rrm=1",
				"phy_mode=6",
				"auth_algs=1",
				"ieee8021x=0",
				"eap_server=0",
				"hw_mode=ng",
				"doth=1",
				"dotd=1",
				"dtim=3",
				"mcast_rate=2",
				"ifparent=bwl1",
				"sku=145",
				"region=76",
				"allow_ht40=0",
				"allow_vht80=0",
				"acs=1",
				"userland_dfs=0",
				"tx_power=100",
				"max_clients=64",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			other := translate.WantedRadio{Name: "wifi1", Band: "5g"}
			if tc.radio.Band == "5g" {
				other = translate.WantedRadio{Name: "wifi0", Band: "2g"}
			}
			w := translate.Wanted{Radios: []translate.WantedRadio{tc.radio, other}, WLANs: []translate.WantedWLAN{tc.wlan}}
			vaps, notes := AirPortVAPs(w, tc.uplink5)
			if len(vaps) != 1 {
				t.Fatalf("vaps %v, notes %q", apNames(vaps), notes)
			}
			if vaps[0].Name != tc.vap {
				t.Errorf("name %q, want %q", vaps[0].Name, tc.vap)
			}
			got := apLines(vaps[0].Config("145", "76"))
			if !reflect.DeepEqual(got, tc.want) {
				for i := 0; i < len(got) || i < len(tc.want); i++ {
					var g, w string
					if i < len(got) {
						g = got[i]
					}
					if i < len(tc.want) {
						w = tc.want[i]
					}
					if g != w {
						t.Errorf("line %d: got %q, want %q", i+1, g, w)
					}
				}
			}
			if !strings.HasSuffix(vaps[0].Config("145", "76"), "\n") {
				t.Error("config must end with a newline")
			}
		})
	}
}

func TestAirPortOpenHasNoWPALines(t *testing.T) {
	w := translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{
		{N: "1", Radio: "wifi0", SSID: apTestSSID, Security: "none"},
		{N: "2", Radio: "wifi1", SSID: apTestSSID, Security: "none"},
	}}
	vaps, notes := AirPortVAPs(w, 36)
	if len(vaps) != 2 {
		t.Fatalf("vaps %v notes %q", apNames(vaps), notes)
	}
	for _, v := range vaps {
		for _, l := range apLines(v.Config("1", "2")) {
			if strings.HasPrefix(l, "wpa") || strings.Contains(l, "rsn_") {
				t.Errorf("%s: open network has line %q", v.Name, l)
			}
		}
	}
}

func TestAirPortVAPNamingAndSlots(t *testing.T) {
	disabled := apWLAN("2", "wifi0")
	disabled.Disabled = true
	guest := apWLAN("5", "wifi1")
	guest.Guest = true
	w := translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{
		apWLAN("1", "wifi0"),
		disabled,
		apWLAN("3", "wifi1"),
		apWLAN("4", "wifi0"),
		guest,
		apWLAN("6", "wifi1"),
	}}
	vaps, notes := AirPortVAPs(w, 0)
	want := []struct{ name, parent string }{
		{"wlan10", "bwl1"}, {"wlan11", "bwl1"}, {"wlan20", "bwl0"}, {"wlan21", "bwl0"},
	}
	// order of appearance in the controller's list: wireless.1, .4, .3, .6
	order := []string{"wlan10", "wlan20", "wlan11", "wlan21"}
	if !reflect.DeepEqual(apNames(vaps), order) {
		t.Fatalf("names %v, want %v (notes %q)", apNames(vaps), order, notes)
	}
	for _, v := range vaps {
		ok := false
		for _, x := range want {
			if x.name == v.Name && x.parent == v.Parent {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%s has parent %s", v.Name, v.Parent)
		}
		if !apHasLine(v.Head, "interface="+v.Name) || !apHasLine(v.Head, "ifparent="+v.Parent) {
			t.Errorf("%s: head disagrees with name/parent", v.Name)
		}
	}
}

func TestAirPortNinthWLANOnARadioGetsNoVAP(t *testing.T) {
	for _, tc := range []struct {
		radio, prefix, parent string
	}{
		{"wifi0", "wlan1", "bwl1"},
		{"wifi1", "wlan2", "bwl0"},
	} {
		t.Run(tc.radio, func(t *testing.T) {
			w := translate.Wanted{Radios: apRadios()}
			for i := 1; i <= 9; i++ {
				w.WLANs = append(w.WLANs, apWLAN(string(rune('0'+i)), tc.radio))
			}
			vaps, notes := AirPortVAPs(w, 0)
			var want []string
			for i := 0; i < 8; i++ {
				want = append(want, tc.prefix+string(rune('0'+i)))
			}
			if !reflect.DeepEqual(apNames(vaps), want) {
				t.Errorf("names %v, want %v", apNames(vaps), want)
			}
			if !apNotesHave(notes, "wireless.9") || !apNotesHave(notes, "more than 8") {
				t.Errorf("no note for the 9th WLAN: %q", notes)
			}
			if err := checkAirPortVAPs(vaps); err != nil {
				t.Errorf("own output refused: %v", err)
			}
		})
	}
}

func TestAirPortSkipRules(t *testing.T) {
	long := strings.Repeat("a", 64)
	tests := []struct {
		name  string
		mod   func(*translate.WantedWLAN, *translate.WantedRadio)
		note  string
		keeps bool // a VAP is still made
	}{
		{"disabled WLAN", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.Disabled = true }, "disabled", false},
		{"disabled radio", func(_ *translate.WantedWLAN, r *translate.WantedRadio) { r.Disabled = true }, "radio disabled", false},
		{"adoption SSID", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.Adopt = true }, "adoption SSID", false},
		{"guest", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.Guest = true }, "guest", false},
		{"VLAN", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.VLAN = 20 }, "VLAN 20", false},
		{"sae", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.Security = "sae" }, "security sae", false},
		{"sae-mixed", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.Security = "sae-mixed" }, "security sae-mixed", false},
		{"SSID with LF", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.SSID = "Example\nNet" }, "SSID has a line break", false},
		{"SSID with CR", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.SSID = "Example\rNet" }, "SSID has a line break", false},
		{"passphrase with LF", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.Passphrase = "correct\nhorse 1" }, "line break", false},
		{"passphrase with CR", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.Passphrase = "correct\rhorse 1" }, "line break", false},
		{"passphrase of 7", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.Passphrase = "1234567" }, "8 to 63", false},
		{"passphrase of 64", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.Passphrase = long }, "8 to 63", false},
		{"passphrase of 8", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.Passphrase = "12345678" }, "", true},
		{"passphrase of 63", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.Passphrase = long[:63] }, "", true},
		{"open network keeps VAP", func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.Security, l.Passphrase = "none", "" }, "", true},
	}
	for _, band := range []string{"wifi0", "wifi1"} {
		for _, tc := range tests {
			t.Run(band+"/"+tc.name, func(t *testing.T) {
				radios := apRadios()
				l := apWLAN("7", band)
				for i := range radios {
					if radios[i].Name == band {
						tc.mod(&l, &radios[i])
					}
				}
				vaps, notes := AirPortVAPs(translate.Wanted{Radios: radios, WLANs: []translate.WantedWLAN{l}}, 0)
				if tc.keeps {
					if len(vaps) != 1 {
						t.Fatalf("expected a VAP, got none (notes %q)", notes)
					}
					return
				}
				if len(vaps) != 0 {
					t.Fatalf("expected no VAP, got %v", apNames(vaps))
				}
				if len(notes) == 0 || !apNotesHave(notes, "wireless.7") || !apNotesHave(notes, "no VAP") || !apNotesHave(notes, tc.note) {
					t.Errorf("notes %q lack %q", notes, tc.note)
				}
				for _, n := range notes {
					if strings.Contains(n, l.Passphrase) && l.Passphrase != "" {
						t.Errorf("passphrase in note %q", n)
					}
				}
			})
		}
	}
}

func TestAirPortSkippedWLANDoesNotTakeASlot(t *testing.T) {
	bad := apWLAN("1", "wifi0")
	bad.VLAN = 5
	vaps, _ := AirPortVAPs(translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{bad, apWLAN("2", "wifi0")}}, 0)
	if !reflect.DeepEqual(apNames(vaps), []string{"wlan10"}) {
		t.Errorf("names %v", apNames(vaps))
	}
}

func TestAirPortUnknownRadio(t *testing.T) {
	vaps, notes := AirPortVAPs(translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{apWLAN("1", "wifi7")}}, 0)
	if len(vaps) != 0 || !apNotesHave(notes, "radio unknown") {
		t.Errorf("vaps %v notes %q", apNames(vaps), notes)
	}
	r := []translate.WantedRadio{{Name: "wifi2", Band: "6g"}}
	vaps, notes = AirPortVAPs(translate.Wanted{Radios: r, WLANs: []translate.WantedWLAN{apWLAN("1", "wifi2")}}, 0)
	if len(vaps) != 0 || !apNotesHave(notes, "no 6g radio") {
		t.Errorf("6 GHz: vaps %v notes %q", apNames(vaps), notes)
	}
}

func TestAirPortChannelRules(t *testing.T) {
	tests := []struct {
		name    string
		band    string
		ctrl    int
		uplink5 int
		acs     string
		channel string // "" = no channel= line
	}{
		{"5 GHz with uplink, controller asks another channel", "5g", 100, 40, "0", "40"},
		{"5 GHz with uplink, controller asks auto", "5g", 0, 40, "0", "40"},
		{"5 GHz with uplink, same channel", "5g", 40, 40, "0", "40"},
		{"5 GHz without uplink, controller's channel", "5g", 100, 0, "0", "100"},
		{"5 GHz without uplink, auto", "5g", 0, 0, "1", ""},
		{"2.4 GHz, controller's channel", "2g", 11, 0, "0", "11"},
		{"2.4 GHz, auto", "2g", 0, 0, "1", ""},
		{"2.4 GHz ignores the 5 GHz uplink (channel)", "2g", 11, 40, "0", "11"},
		{"2.4 GHz ignores the 5 GHz uplink (auto)", "2g", 0, 40, "1", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			radio, name := "wifi0", "wlan10"
			if tc.band == "5g" {
				radio, name = "wifi1", "wlan20"
			}
			radios := apRadios()
			for i := range radios {
				if radios[i].Name == radio {
					radios[i].Channel = tc.ctrl
				}
			}
			vaps, notes := AirPortVAPs(translate.Wanted{Radios: radios, WLANs: []translate.WantedWLAN{apWLAN("1", radio)}}, tc.uplink5)
			if len(vaps) != 1 || vaps[0].Name != name {
				t.Fatalf("vaps %v notes %q", apNames(vaps), notes)
			}
			cfg := vaps[0].Config("145", "76")
			if !apHasLine(cfg, "acs="+tc.acs) {
				t.Errorf("want acs=%s in\n%s", tc.acs, cfg)
			}
			if tc.channel == "" {
				if apHasKey(cfg, "channel") || vaps[0].Channel != 0 {
					t.Errorf("auto channel must have no channel= line (Channel %d):\n%s", vaps[0].Channel, cfg)
				}
			} else if !apHasLine(cfg, "channel="+tc.channel) {
				t.Errorf("want channel=%s in\n%s", tc.channel, cfg)
			}
			// the radio is tuned (ifconfig, in the script) only when the channel is
			// fixed and the radio is not the mesh uplink's
			if wantTune := tc.channel != "" && !(tc.band == "5g" && tc.uplink5 > 0); vaps[0].Tune != wantTune {
				t.Errorf("Tune = %v, want %v", vaps[0].Tune, wantTune)
			}
			// channel= is the very last line, after max_clients=
			ls := apLines(cfg)
			if tc.channel != "" && ls[len(ls)-1] != "channel="+tc.channel {
				t.Errorf("channel= must be last: %q", ls[len(ls)-1])
			}
		})
	}
}

func TestAirPortUplinkDifferingChannelIsNoted(t *testing.T) {
	radios := apRadios()
	radios[1].Channel = 100
	_, notes := AirPortVAPs(translate.Wanted{Radios: radios, WLANs: []translate.WantedWLAN{apWLAN("1", "wifi1")}}, 40)
	if !apNotesHave(notes, "follows the mesh uplink on channel 40") {
		t.Errorf("notes %q", notes)
	}
}

func TestAirPortWidthAndPowerNotedOnce(t *testing.T) {
	radios := []translate.WantedRadio{
		{Name: "wifi0", Band: "2g", Width: 40, TxPower: 14},
		{Name: "wifi1", Band: "5g", Width: 80, TxPower: 20},
	}
	_, notes := AirPortVAPs(translate.Wanted{Radios: radios, WLANs: []translate.WantedWLAN{
		apWLAN("1", "wifi0"), apWLAN("2", "wifi0"), apWLAN("3", "wifi1"), apWLAN("4", "wifi1"),
	}}, 0)
	w, p := 0, 0
	for _, n := range notes {
		if strings.Contains(n, "channel width not applied") {
			w++
		}
		if strings.Contains(n, "transmit power not applied") {
			p++
		}
	}
	if w != 1 || p != 1 {
		t.Errorf("width notes %d, power notes %d, want 1 each: %q", w, p, notes)
	}
}

func TestAirPortNoPassphraseInNotesOrString(t *testing.T) {
	const secret = "s3cret-Pass-9"
	mk := func(n, radio string, mod func(*translate.WantedWLAN)) translate.WantedWLAN {
		l := apWLAN(n, radio)
		l.Passphrase = secret
		if mod != nil {
			mod(&l)
		}
		return l
	}
	radios := []translate.WantedRadio{
		{Name: "wifi0", Band: "2g", Width: 40, TxPower: 14, Channel: 6},
		{Name: "wifi1", Band: "5g", Width: 80, TxPower: 20, Channel: 100},
	}
	w := translate.Wanted{Radios: radios, WLANs: []translate.WantedWLAN{
		mk("1", "wifi0", nil),
		mk("2", "wifi1", func(l *translate.WantedWLAN) { l.Isolate = true; l.MACPolicy = "deny"; l.PMF = 2 }),
		mk("3", "wifi0", func(l *translate.WantedWLAN) { l.Disabled = true }),
		mk("4", "wifi0", func(l *translate.WantedWLAN) { l.Adopt = true }),
		mk("5", "wifi0", func(l *translate.WantedWLAN) { l.Guest = true }),
		mk("6", "wifi0", func(l *translate.WantedWLAN) { l.VLAN = 9 }),
		mk("7", "wifi0", func(l *translate.WantedWLAN) { l.Security = "sae" }),
		mk("8", "wifi0", func(l *translate.WantedWLAN) { l.Security = "sae-mixed" }),
		mk("9", "wifi0", func(l *translate.WantedWLAN) { l.Passphrase = secret + "\n" }),
		mk("10", "wifi0", func(l *translate.WantedWLAN) { l.SSID = "x\r" }),
	}}
	vaps, notes := AirPortVAPs(w, 40)
	if len(notes) < 8 {
		t.Fatalf("expected many notes, got %q", notes)
	}
	for _, n := range notes {
		if strings.Contains(n, "s3cret") {
			t.Errorf("passphrase in note %q", n)
		}
	}
	if len(vaps) != 2 {
		t.Fatalf("vaps %v", apNames(vaps))
	}
	for _, v := range vaps {
		if s := v.String(); strings.Contains(s, "s3cret") {
			t.Errorf("passphrase in String(): %q", s)
		}
	}
	// a passphrase that is too short to be used must not show up either
	vaps, notes = AirPortVAPs(translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{
		func() translate.WantedWLAN { l := apWLAN("1", "wifi0"); l.Passphrase = "short-1"; return l }(),
	}}, 0)
	if len(vaps) != 0 || apNotesHave(notes, "short-1") {
		t.Errorf("vaps %v notes %q", apNames(vaps), notes)
	}
}

func TestAirPortVAPString(t *testing.T) {
	w := translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{apWLAN("1", "wifi0"), apWLAN("2", "wifi1")}}
	w.Radios[0].Channel = 6
	vaps, _ := AirPortVAPs(w, 0)
	if len(vaps) != 2 {
		t.Fatal(apNames(vaps))
	}
	if got, want := vaps[0].String(), `wlan10 on bwl1: ssid "Example Net", psk2, channel 6`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := vaps[1].String(), `wlan20 on bwl0: ssid "Example Net", psk2, auto channel`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---- the script ----

type apHeredoc struct {
	delim string
	body  []string
}

// apHeredocs finds every `cat <<'DELIM'` in the script and its body.
func apHeredocs(t *testing.T, script string) []apHeredoc {
	t.Helper()
	var out []apHeredoc
	lines := strings.Split(script, "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if !strings.HasPrefix(l, "cat <<'") || !strings.HasSuffix(l, "'") {
			continue
		}
		h := apHeredoc{delim: strings.TrimSuffix(strings.TrimPrefix(l, "cat <<'"), "'")}
		i++
		for ; i < len(lines) && lines[i] != h.delim; i++ {
			h.body = append(h.body, lines[i])
		}
		if i == len(lines) {
			t.Fatalf("heredoc %q is never closed", h.delim)
		}
		out = append(out, h)
	}
	return out
}

func apTwoVAPs(t *testing.T, pass string) []AirPortVAP {
	t.Helper()
	l1, l2 := apWLAN("1", "wifi0"), apWLAN("2", "wifi1")
	l1.Passphrase, l2.Passphrase = pass, pass
	vaps, notes := AirPortVAPs(translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{l1, l2}}, 40)
	if len(vaps) != 2 {
		t.Fatalf("vaps %v notes %q", apNames(vaps), notes)
	}
	return vaps
}

func TestAirPortScriptNeverMentionsTheUplink(t *testing.T) {
	for name, vaps := range map[string][]AirPortVAP{
		"two":   apTwoVAPs(t, apTestPass),
		"empty": nil,
	} {
		if s := AirPortApplyScript(vaps); strings.Contains(s, "wlan9") {
			t.Errorf("%s: script mentions wlan9", name)
		}
	}
}

// apRadioCall is one `radio RADIO 'PATTERN' CHANNEL VAP...` line of the script.
type apRadioCall struct {
	radio, pattern, channel, names string
}

func rc(radio, pattern, channel, names string) apRadioCall {
	return apRadioCall{radio, pattern, channel, names}
}

var apRadioCallRE = regexp.MustCompile(`(?m)^radio (\S+) '([^']*)' (\S+)((?: \S+)*)$`)

// apRadioCalls returns the calls of the radio function (not its definition).
func apRadioCalls(t *testing.T, script string) []apRadioCall {
	t.Helper()
	var out []apRadioCall
	for _, m := range apRadioCallRE.FindAllStringSubmatch(script, -1) {
		out = append(out, apRadioCall{m[1], m[2], m[3], strings.TrimSpace(m[4])})
	}
	if n := strings.Count(script, "\nradio "); n != len(out) {
		t.Fatalf("%d lines start with \"radio \" but %d parse as calls:\n%s", n, len(out), script)
	}
	return out
}

// apWantedSlots wants n24 WLANs on the 2.4 GHz radio and n5 on the 5 GHz one,
// interleaved, so that the slot order is not the order of the list.
func apWantedSlots(ch24, ch5, n24, n5 int) translate.Wanted {
	w := translate.Wanted{Radios: []translate.WantedRadio{
		{Name: "wifi0", Band: "2g", Channel: ch24},
		{Name: "wifi1", Band: "5g", Channel: ch5},
	}}
	for i := 0; i < n24 || i < n5; i++ {
		if i < n24 {
			w.WLANs = append(w.WLANs, apWLAN(fmt.Sprint("a", i), "wifi0"))
		}
		if i < n5 {
			w.WLANs = append(w.WLANs, apWLAN(fmt.Sprint("b", i), "wifi1"))
		}
	}
	return w
}

func TestAirPortScriptOnlyMatchesItsOwnRanges(t *testing.T) {
	re := regexp.MustCompile(`wlan[0-9a-z\[\]|-]*`)
	for name, vaps := range map[string][]AirPortVAP{
		"two":   apTwoVAPs(t, apTestPass),
		"empty": nil,
	} {
		s := AirPortApplyScript(vaps)
		// outside the VAP names and the ifconfig keywords, the only wlan tokens
		// are the two radio patterns
		for _, m := range re.FindAllString(s, -1) {
			if m == "wlan1[0-7]" || m == "wlan2[0-7]" || m == "wlandev" || m == "wlanmode" {
				continue
			}
			ok := false
			for _, v := range vaps {
				if m == v.Name {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s: unexpected wlan token %q", name, m)
			}
		}
		// the radio function only stops what its pattern matches, and what it was
		// not asked to run
		for _, want := range []string{"case $v in $pat)", "for v in $(ifconfig -l); do"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: radio function lacks %q", name, want)
			}
		}
		calls := apRadioCalls(t, s)
		if len(calls) != 2 || calls[0].pattern != "wlan1[0-7]" || calls[1].pattern != "wlan2[0-7]" {
			t.Errorf("%s: patterns %+v", name, calls)
		}
		// the old shape is gone
		for _, gone := range []string{"\nwant=", "\nvap ", "\ntune ", "continue;; esac"} {
			if strings.Contains(s, gone) {
				t.Errorf("%s: old script shape %q is back", name, gone)
			}
		}
	}
}

func TestAirPortScriptRadioCallsListTheirVAPs(t *testing.T) {
	tests := []struct {
		name          string
		ch24, ch5     int
		n24, n5       int
		uplink5       int
		want24, want5 apRadioCall
	}{
		{"fixed channels, no uplink", 6, 44, 2, 2, 0,
			rc("bwl1", "wlan1[0-7]", "6", "wlan10 wlan11"), rc("bwl0", "wlan2[0-7]", "44", "wlan20 wlan21")},
		{"5 GHz with an uplink: not tuned", 6, 44, 2, 1, 40,
			rc("bwl1", "wlan1[0-7]", "6", "wlan10 wlan11"), rc("bwl0", "wlan2[0-7]", "-", "wlan20")},
		{"5 GHz auto with an uplink: not tuned", 0, 0, 1, 2, 40,
			rc("bwl1", "wlan1[0-7]", "auto", "wlan10"), rc("bwl0", "wlan2[0-7]", "-", "wlan20 wlan21")},
		{"5 GHz same channel as the uplink", 11, 40, 1, 1, 40,
			rc("bwl1", "wlan1[0-7]", "11", "wlan10"), rc("bwl0", "wlan2[0-7]", "-", "wlan20")},
		{"both automatic, no uplink", 0, 0, 2, 2, 0,
			rc("bwl1", "wlan1[0-7]", "auto", "wlan10 wlan11"), rc("bwl0", "wlan2[0-7]", "auto", "wlan20 wlan21")},
		{"2.4 GHz only", 11, 0, 1, 0, 0,
			rc("bwl1", "wlan1[0-7]", "11", "wlan10"), rc("bwl0", "wlan2[0-7]", "-", "")},
		{"5 GHz only", 0, 100, 0, 1, 0,
			rc("bwl1", "wlan1[0-7]", "-", ""), rc("bwl0", "wlan2[0-7]", "100", "wlan20")},
		{"no VAPs", 6, 44, 0, 0, 0,
			rc("bwl1", "wlan1[0-7]", "-", ""), rc("bwl0", "wlan2[0-7]", "-", "")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vaps, notes := AirPortVAPs(apWantedSlots(tc.ch24, tc.ch5, tc.n24, tc.n5), tc.uplink5)
			if len(vaps) != tc.n24+tc.n5 {
				t.Fatalf("vaps %v notes %q", apNames(vaps), notes)
			}
			got := apRadioCalls(t, AirPortApplyScript(vaps))
			want := []apRadioCall{tc.want24, tc.want5}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("radio calls:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

// hostapd's channel= does not move the radio: the script tunes it with ifconfig
// on the radio's first VAP, so the radio call carries the channel. The radio of
// the mesh uplink is never tuned.
func TestAirPortScriptTunesTheRadio(t *testing.T) {
	w := translate.Wanted{
		Radios: []translate.WantedRadio{{Name: "wifi0", Band: "2g", Channel: 6}, {Name: "wifi1", Band: "5g", Channel: 44}},
		WLANs: []translate.WantedWLAN{
			{N: "1", Radio: "wifi0", SSID: "Example Net", Security: "none"},
			{N: "2", Radio: "wifi0", SSID: "Example Two", Security: "none"},
			{N: "3", Radio: "wifi1", SSID: "Example Net", Security: "none"},
		},
	}
	vaps, _ := AirPortVAPs(w, 40) // a mesh uplink on channel 40
	calls := apRadioCalls(t, AirPortApplyScript(vaps))
	if len(calls) != 2 || calls[0].channel != "6" || calls[1].channel != "-" {
		t.Errorf("with an uplink: %+v", calls)
	}
	vaps, _ = AirPortVAPs(w, 0) // wired: the controller's 5 GHz channel counts
	calls = apRadioCalls(t, AirPortApplyScript(vaps))
	if len(calls) != 2 || calls[0].channel != "6" || calls[1].channel != "44" {
		t.Errorf("without an uplink: %+v", calls)
	}
	// the tuning lives in start: ifconfig VAP channel N, for a number only
	s := AirPortApplyScript(vaps)
	if !strings.Contains(s, "case $3 in auto|-) ;; *) ifconfig $v channel $3 >/dev/null 2>&1;; esac") {
		t.Error("start does not tune the radio for a numeric channel only")
	}
}

func TestAirPortScriptEmptyListStillRemoves(t *testing.T) {
	s := AirPortApplyScript(nil)
	calls := apRadioCalls(t, s)
	want := []apRadioCall{
		{"bwl1", "wlan1[0-7]", "-", ""},
		{"bwl0", "wlan2[0-7]", "-", ""},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("radio calls %+v, want %+v (they are what removes the daemon's VAPs)", calls, want)
	}
	if !strings.Contains(s, `echo "$v removed"`) {
		t.Error("the radio function never reports a removal")
	}
	if strings.Contains(s, ".new\n") || strings.Contains(s, "cat <<") {
		t.Error("empty VAP list writes a config")
	}
}

// Every config is on the device before the first radio call looks at any of
// them: the radio function compares them with what runs.
func TestAirPortScriptWritesEveryConfigBeforeTheRadioCalls(t *testing.T) {
	vaps := apEightEach(t)
	s := AirPortApplyScript(vaps)
	first := strings.Index(s, "\nradio bwl")
	if first < 0 {
		t.Fatal("no radio call")
	}
	for _, v := range vaps {
		w := "} > $D/" + v.Name + ".new\n"
		if n := strings.Count(s, w); n != 1 {
			t.Errorf("%s: .new written %d times", v.Name, n)
			continue
		}
		if strings.Index(s, w) > first {
			t.Errorf("%s: .new written after the first radio call", v.Name)
		}
	}
	calls := apRadioCalls(t, s)
	if len(calls) != 2 || len(strings.Fields(calls[0].names)) != 8 || len(strings.Fields(calls[1].names)) != 8 {
		t.Errorf("radio calls %+v", calls)
	}
}

func TestAirPortScriptPassphraseVerbatimInQuotedHeredoc(t *testing.T) {
	pass := "a'b\"c$d`e\\f"
	vaps := apTwoVAPs(t, pass)
	s := AirPortApplyScript(vaps)
	hd := apHeredocs(t, s)
	if len(hd) != 4 {
		t.Fatalf("heredocs: %d, want 2 per VAP", len(hd))
	}
	for i, v := range vaps {
		head := hd[2*i]
		if !reflect.DeepEqual(head.body, apLines(v.Head)) {
			t.Errorf("%s: heredoc body differs from the head", v.Name)
		}
		found := false
		for _, l := range head.body {
			if l == "wpa_passphrase="+pass {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: passphrase not verbatim in the heredoc", v.Name)
		}
		// quoted delimiter: the opener is cat <<'X', so no expansion
		if !strings.Contains(s, "cat <<'"+head.delim+"'\n") {
			t.Errorf("%s: heredoc not quoted", v.Name)
		}
		if !reflect.DeepEqual(hd[2*i+1].body, apLines(v.Tail)) {
			t.Errorf("%s: tail heredoc body differs", v.Name)
		}
	}
	// the unit's ids come from the device, between head and tail
	if !strings.Contains(s, "echo \"sku=$sku\"\necho \"region=$region\"\n") {
		t.Error("sku and region are not appended from the device")
	}
	// no passphrase outside a heredoc body (it must not reach a command line)
	inBody := 0
	for _, h := range hd {
		for _, l := range h.body {
			if strings.Contains(l, pass) {
				inBody++
			}
		}
	}
	if n := strings.Count(s, pass); n != inBody {
		t.Errorf("passphrase appears %d times, %d in heredoc bodies", n, inBody)
	}
}

func TestAirPortScriptDelimiterChangesOnClash(t *testing.T) {
	// the rendered configs cannot contain such a line (every line has "="), but
	// the script must still hold if one does
	v := AirPortVAP{Name: "wlan10", Parent: "bwl1", Head: "interface=wlan10\nUOW_EOF_0A\nifparent=bwl1\n", Tail: "UOW_EOF_0B\nacs=1\n"}
	hd := apHeredocs(t, AirPortApplyScript([]AirPortVAP{v}))
	if len(hd) != 2 {
		t.Fatalf("heredocs %d", len(hd))
	}
	if hd[0].delim == "UOW_EOF_0A" || hd[1].delim == "UOW_EOF_0B" {
		t.Errorf("delimiters did not change: %q %q", hd[0].delim, hd[1].delim)
	}
	if !reflect.DeepEqual(hd[0].body, apLines(v.Head)) || !reflect.DeepEqual(hd[1].body, apLines(v.Tail)) {
		t.Errorf("bodies cut short by the delimiter: %q %q", hd[0].body, hd[1].body)
	}
	for _, h := range hd {
		for _, l := range h.body {
			if l == h.delim {
				t.Errorf("delimiter %q inside its own body", h.delim)
			}
		}
	}
	// the helper itself
	for _, tc := range []struct{ text, base, want string }{
		{"a=b\n", "X", "X"},
		{"a=b\nX\n", "X", "X_"},
		{"X\nX_\n", "X", "X__"},
		{"", "X", "X"},
	} {
		if got := delim(tc.text, tc.base); got != tc.want {
			t.Errorf("delim(%q, %q) = %q, want %q", tc.text, tc.base, got, tc.want)
		}
	}
}

func TestAirPortScriptSyntax(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host: cannot run sh -n")
	}
	for name, vaps := range map[string][]AirPortVAP{
		"two VAPs":          apTwoVAPs(t, "a'b\"c$d`e\\f"),
		"no VAPs":           nil,
		"delimiter clash":   {{Name: "wlan10", Parent: "bwl1", Head: "a=b\nUOW_EOF_0A\n", Tail: "c=d\n"}},
		"every VAP, 8 each": apEightEach(t),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			f := filepath.Join(dir, "apply.sh")
			if err := os.WriteFile(f, []byte(AirPortApplyScript(vaps)), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(sh, "-n", f).CombinedOutput()
			if err != nil {
				t.Errorf("sh -n: %v\n%s", err, out)
			}
		})
	}
}

func apEightEach(t *testing.T) []AirPortVAP {
	t.Helper()
	w := translate.Wanted{Radios: apRadios()}
	for i := 0; i < 8; i++ {
		w.WLANs = append(w.WLANs, apWLAN(string(rune('1'+i)), "wifi0"), apWLAN(string(rune('A'+i)), "wifi1"))
	}
	vaps, _ := AirPortVAPs(w, 36)
	if len(vaps) != 16 {
		t.Fatalf("%d vaps", len(vaps))
	}
	return vaps
}

// ---- the script, run against stub commands ----

// The stubs keep a tiny device in files: S/vap/NAME (its parent radio),
// S/run/NAME (a hostapd is up on it), S/chan/RADIO, and S/log (what changed).
// The process table is faked from S/run and the pid files the script writes, so
// nothing is really started or killed.
var apStubs = map[string]string{
	"ifconfig": `#!/bin/sh
S=$STUB_STATE
if [ "$1" = "-l" ]; then
	out=
	for f in $S/vap/*; do
		[ -e "$f" ] && out="$out ${f##*/}"
	done
	echo ${out# }
	exit 0
fi
v=$1
shift
case "$1" in
create)
	if [ -e $S/vap/$v ]; then echo "ifconfig: SIOCIFCREATE: File exists" >&2; exit 1; fi
	echo $3 > $S/vap/$v
	echo "create $v" >> $S/log
	exit 0;;
destroy)
	[ -e $S/vap/$v ] || { echo "ifconfig: interface $v does not exist" >&2; exit 1; }
	rm -f $S/vap/$v $S/run/$v
	echo "destroy $v" >> $S/log
	exit 0;;
channel)
	p=$(cat $S/vap/$v 2>/dev/null) || exit 1
	echo $2 > $S/chan/$p
	echo "channel $p $2" >> $S/log
	exit 0;;
esac
if [ -e $S/vap/$v ]; then
	p=$(cat $S/vap/$v)
elif [ "$v" = bwl0 ] || [ "$v" = bwl1 ]; then
	p=$v
else
	echo "ifconfig: interface $v does not exist" >&2
	exit 1
fi
c=$(cat $S/chan/$p 2>/dev/null)
if [ -z "$c" ]; then
	if [ $p = bwl0 ]; then c=36; else c=1; fi
fi
if [ -e $S/run/$v ]; then
	echo "$v: flags=8843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST> metric 0 mtu 1500"
	echo "	ssid \"\" channel $c (2437 Mhz 11g ht/20)"
	echo "	status: running"
else
	echo "$v: flags=8822<BROADCAST,SIMPLEX,MULTICAST> metric 0 mtu 1500"
	echo "	ssid \"\" channel $c (2437 Mhz 11g ht/20)"
	echo "	status: no carrier"
fi
`,
	"brconfig": `#!/bin/sh
echo "bridge $2 $3" >> $STUB_STATE/log
`,
	// -t -F LOG CONF: takes the interface and acs= from CONF; an automatic
	// channel puts the radio on 11, so a follower has something to follow
	"hostapd": `#!/bin/sh
for a; do conf=$a; done
iface=; acs=
while read -r l || [ -n "$l" ]; do
	case "$l" in interface=*) iface=${l#interface=};; acs=*) acs=${l#acs=};; esac
done < "$conf"
[ -e $STUB_STATE/vap/$iface ] || exit 1
p=$(cat $STUB_STATE/vap/$iface)
[ "$acs" = 1 ] && echo 11 > $STUB_STATE/chan/$p
echo "hostapd $iface" >> $STUB_STATE/log
echo up > $STUB_STATE/run/$iface
`,
	"ps": `#!/bin/sh
echo "  PID COMMAND"
for f in $STUB_STATE/run/*; do
	[ -e "$f" ] || continue
	v=${f##*/}
	echo "$(cat $STUB_D/$v.pid 2>/dev/null) hostapd -t -F x"
done
`,
	"kill": `#!/bin/sh
for a; do
	case "$a" in -*) continue;; esac
	for f in $STUB_D/*.pid; do
		[ -e "$f" ] || continue
		if [ "$(cat $f)" = "$a" ]; then
			v=${f##*/}
			rm -f $STUB_STATE/run/${v%.pid}
		fi
	done
done
`,
	// a short real sleep: the script waits for a backgrounded hostapd
	"sleep": `#!/bin/sh
exec "$STUB_REAL_SLEEP" 0.2
`,
}

type apStubHost struct {
	t      *testing.T
	sh     string
	dir    string // the fake device's root, with forward slashes
	script func(vaps []AirPortVAP) string
	env    []string
}

// newAPStubHost builds the fake device, or skips the test when this host cannot
// run it: no sh, no real sleep, a path the script would split, or stubs that do
// not answer under this sh.
func newAPStubHost(t *testing.T) *apStubHost {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: the script runs many processes (about a minute under Windows)")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host: cannot run the script")
	}
	realSleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary on this host: the stub sleep needs one")
	}
	dir := t.TempDir()
	root := filepath.ToSlash(dir)
	if strings.ContainsAny(root, " \t'\"$`\\") {
		t.Skipf("temporary directory %q has characters the script would not survive unquoted", root)
	}
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{bin, filepath.Join(dir, "state", "vap"), filepath.Join(dir, "state", "run"), filepath.Join(dir, "state", "chan")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range apStubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// units already there that the script must never touch: the uplink and a
	// firmware VAP
	for _, v := range []struct{ name, parent string }{{"wlan0", "bwl1"}, {"wlan9", "bwl0"}} {
		if err := os.WriteFile(filepath.Join(dir, "state", "vap", v.name), []byte(v.parent+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "hostap.seed"), []byte("sku=145\nregion=76\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &apStubHost{t: t, sh: sh, dir: root}
	h.env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_STATE="+root+"/state",
		"STUB_D="+root+"/run",
		"STUB_REAL_SLEEP="+filepath.ToSlash(realSleep),
	)
	h.script = func(vaps []AirPortVAP) string {
		s := AirPortApplyScript(vaps)
		// the device's own paths and PATH line become the temporary ones; every
		// replaced string must be there, so a change in the script is noticed
		for _, r := range [][2]string{
			{"PATH=/bin:/sbin:/usr/bin:/usr/sbin", "PATH=$PATH"},
			{apRunDir, root + "/run"},
			{apSeedFile, root + "/hostap.seed"},
			{"/sbin/hostapd", root + "/bin/hostapd"},
		} {
			if !strings.Contains(s, r[0]) {
				t.Fatalf("the script no longer contains %q: update the stub test", r[0])
			}
			s = strings.ReplaceAll(s, r[0], r[1])
		}
		return s
	}
	// does the fake device answer at all under this sh?
	out, err := h.exec("ifconfig -l")
	if err != nil || strings.TrimSpace(out) != "wlan0 wlan9" {
		t.Skipf("stub commands do not run reliably under %s on %s (ifconfig -l: %q, %v)", sh, runtime.GOOS, out, err)
	}
	return h
}

func (h *apStubHost) exec(cmdline string) (string, error) {
	cmd := exec.Command(h.sh, "-c", cmdline)
	cmd.Dir = h.dir
	cmd.Env = h.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// apply runs the script for these VAPs and returns its report lines.
func (h *apStubHost) apply(vaps []AirPortVAP) []string {
	h.t.Helper()
	file := filepath.Join(h.dir, "apply.sh")
	if err := os.WriteFile(file, []byte(h.script(vaps)), 0o600); err != nil {
		h.t.Fatal(err)
	}
	cmd := exec.Command(h.sh, "apply.sh")
	cmd.Dir = h.dir
	cmd.Env = h.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("script: %v\n%s", err, out)
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func (h *apStubHost) read(rel string) []string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.dir, filepath.FromSlash(rel)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		h.t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n")
}

// logFrom is what the stubs recorded after the first n entries.
func (h *apStubHost) logFrom(n int) []string {
	l := h.read("state/log")
	if len(l) < n {
		return nil
	}
	return l[n:]
}

// vapsOnDevice is the fake `ifconfig -l`.
func (h *apStubHost) vapsOnDevice() string {
	h.t.Helper()
	out, err := h.exec("ifconfig -l")
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// apFourVAPs: two WLANs per radio, 2.4 GHz on channel 6, 5 GHz on 44 (no uplink).
func apFourVAPs(t *testing.T, ssid24, ssid5 string) []AirPortVAP {
	t.Helper()
	w := apWantedSlots(6, 44, 2, 2)
	w.WLANs[0].SSID = ssid24 // a0
	w.WLANs[1].SSID = ssid5  // b0
	vaps, notes := AirPortVAPs(w, 0)
	if !reflect.DeepEqual(apNames(vaps), []string{"wlan10", "wlan20", "wlan11", "wlan21"}) {
		t.Fatalf("vaps %v notes %q", apNames(vaps), notes)
	}
	return vaps
}

func apWant(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

func TestAirPortScriptRunsAgainstStubs(t *testing.T) {
	t.Run("start, same, one radio changes, removal", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps := apFourVAPs(t, apTestSSID, apTestSSID)
		started := []string{"wlan10 started", "wlan11 started", "wlan20 started", "wlan21 started"}
		same := []string{"wlan10 same", "wlan11 same", "wlan20 same", "wlan21 same"}

		apWant(t, "first run", h.apply(vaps), started)
		if got := h.vapsOnDevice(); got != "wlan0 wlan10 wlan11 wlan20 wlan21 wlan9" {
			t.Errorf("VAPs after the first run: %q", got)
		}
		// the radios were put on the controller's channels, and the config on the
		// device has the unit's ids
		apWant(t, "radio channels", []string{h.read("state/chan/bwl1")[0], h.read("state/chan/bwl0")[0]}, []string{"6", "44"})
		conf := h.read("run/wlan10.conf")
		if !contains(conf, "sku=145") || !contains(conf, "region=76") || !contains(conf, "channel=6") {
			t.Errorf("wlan10.conf: %q", conf)
		}

		n := len(h.read("state/log"))
		apWant(t, "identical second run", h.apply(vaps), same)
		apWant(t, "nothing touched by the second run", h.logFrom(n), nil)

		// one VAP of the 2.4 GHz radio changes: both of its VAPs restart, the
		// other radio is left alone
		changed := apFourVAPs(t, "Example Net 2", apTestSSID)
		n = len(h.read("state/log"))
		apWant(t, "2.4 GHz changed", h.apply(changed), []string{"wlan10 started", "wlan11 started", "wlan20 same", "wlan21 same"})
		for _, l := range h.logFrom(n) {
			if strings.Contains(l, "wlan2") || strings.Contains(l, "bwl0") {
				t.Errorf("the 5 GHz radio was touched: %q", l)
			}
		}
		if got := h.logFrom(n); !contains(got, "destroy wlan10") || !contains(got, "destroy wlan11") {
			t.Errorf("both 2.4 GHz VAPs must go down: %q", got)
		}
		apWant(t, "after the change", h.apply(changed), same)

		// and the other way round
		changed = apFourVAPs(t, "Example Net 2", "Example Net 5")
		n = len(h.read("state/log"))
		apWant(t, "5 GHz changed", h.apply(changed), []string{"wlan10 same", "wlan11 same", "wlan20 started", "wlan21 started"})
		for _, l := range h.logFrom(n) {
			if strings.Contains(l, "wlan1") || strings.Contains(l, "bwl1") {
				t.Errorf("the 2.4 GHz radio was touched: %q", l)
			}
		}

		// no VAPs wanted: everything of the daemon's goes, nothing else
		apWant(t, "empty list", h.apply(nil), []string{"wlan10 removed", "wlan11 removed", "wlan20 removed", "wlan21 removed"})
		if got := h.vapsOnDevice(); got != "wlan0 wlan9" {
			t.Errorf("VAPs after the removal: %q", got)
		}
		apWant(t, "empty list again", h.apply(nil), nil)
	})

	t.Run("a radio that is on another channel restarts", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps := apFourVAPs(t, apTestSSID, apTestSSID)
		h.apply(vaps)
		if err := os.WriteFile(filepath.Join(h.dir, "state", "chan", "bwl1"), []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		apWant(t, "radio moved away", h.apply(vaps), []string{"wlan10 started", "wlan11 started", "wlan20 same", "wlan21 same"})
		apWant(t, "back on its channel", h.read("state/chan/bwl1"), []string{"6"})
	})

	t.Run("a VAP that is down restarts its radio", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps := apFourVAPs(t, apTestSSID, apTestSSID)
		h.apply(vaps)
		if err := os.Remove(filepath.Join(h.dir, "state", "run", "wlan21")); err != nil {
			t.Fatal(err)
		}
		apWant(t, "wlan21 down", h.apply(vaps), []string{"wlan10 same", "wlan11 same", "wlan20 started", "wlan21 started"})
	})

	t.Run("a VAP nobody wants any more", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps := apFourVAPs(t, apTestSSID, apTestSSID)
		h.apply(vaps)
		// wlan11 is dropped: the rest of its radio restarts, 5 GHz is left alone
		apWant(t, "one VAP fewer", h.apply([]AirPortVAP{vaps[0], vaps[1], vaps[3]}),
			[]string{"wlan11 removed", "wlan10 started", "wlan20 same", "wlan21 same"})
		if got := h.vapsOnDevice(); got != "wlan0 wlan10 wlan20 wlan21 wlan9" {
			t.Errorf("VAPs after dropping wlan11: %q", got)
		}
	})

	t.Run("automatic channel: the others follow the first VAP", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps, _ := AirPortVAPs(apWantedSlots(0, 0, 2, 0), 0)
		if len(vaps) != 2 {
			t.Fatal(apNames(vaps))
		}
		apWant(t, "first run", h.apply(vaps), []string{"wlan10 started", "wlan11 started"})
		// the stub's hostapd chose 11 for wlan10; the script appended it to wlan11
		if c := h.read("run/wlan11.conf"); !contains(c, "acs=0") || !contains(c, "channel=11") {
			t.Errorf("wlan11.conf: %q", c)
		}
		if c := h.read("run/wlan10.conf"); !contains(c, "acs=1") || containsPrefix(c, "channel=") {
			t.Errorf("wlan10.conf: %q", c)
		}
		for _, l := range h.read("state/log") {
			if strings.HasPrefix(l, "channel ") {
				t.Errorf("an automatic channel must not be tuned: %q", l)
			}
		}
		apWant(t, "second run", h.apply(vaps), []string{"wlan10 same", "wlan11 same"})
	})

	t.Run("missing seed file", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps := apFourVAPs(t, apTestSSID, apTestSSID)
		h.apply(vaps)
		if err := os.Remove(filepath.Join(h.dir, "hostap.seed")); err != nil {
			t.Fatal(err)
		}
		before, n := h.vapsOnDevice(), len(h.read("state/log"))
		got := h.apply(apFourVAPs(t, "Example Net 2", apTestSSID))
		if len(got) != 1 || !strings.HasPrefix(got[0], "failed:") {
			t.Errorf("report %q, want one line starting with failed:", got)
		}
		if after := h.vapsOnDevice(); after != before {
			t.Errorf("VAPs changed: %q -> %q", before, after)
		}
		apWant(t, "nothing touched", h.logFrom(n), nil)
		got = h.apply(nil)
		if len(got) != 1 || !strings.HasPrefix(got[0], "failed:") {
			t.Errorf("empty list without a seed: %q", got)
		}
		if h.vapsOnDevice() != before {
			t.Error("an empty list without a seed removed VAPs")
		}
	})
}

func contains(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func containsPrefix(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// ---- checkAirPortVAPs ----

func TestCheckAirPortVAPs(t *testing.T) {
	ok := func(name, parent string) AirPortVAP {
		return AirPortVAP{Name: name, Parent: parent, Head: "a=b\n", Tail: "c=d\n"}
	}
	tests := []struct {
		name string
		vaps []AirPortVAP
		bad  bool
	}{
		{"empty list", nil, false},
		{"first and last of both ranges", []AirPortVAP{ok("wlan10", "bwl1"), ok("wlan17", "bwl1"), ok("wlan20", "bwl0"), ok("wlan27", "bwl0")}, false},
		{"the uplink, wlan9", []AirPortVAP{ok("wlan9", "bwl0")}, true},
		{"wlan18", []AirPortVAP{ok("wlan18", "bwl1")}, true},
		{"wlan28", []AirPortVAP{ok("wlan28", "bwl0")}, true},
		{"wlan0, the firmware's", []AirPortVAP{ok("wlan0", "bwl1")}, true},
		{"wlan1", []AirPortVAP{ok("wlan1", "bwl1")}, true},
		{"wlan100", []AirPortVAP{ok("wlan100", "bwl1")}, true},
		{"shell in the name", []AirPortVAP{ok("wlan10; reboot", "bwl1")}, true},
		{"empty name", []AirPortVAP{ok("", "bwl1")}, true},
		{"duplicate name", []AirPortVAP{ok("wlan10", "bwl1"), ok("wlan10", "bwl1")}, true},
		{"parent bwl2", []AirPortVAP{ok("wlan10", "bwl2")}, true},
		{"parent bcmeth0", []AirPortVAP{ok("wlan10", "bcmeth0")}, true},
		{"empty parent", []AirPortVAP{ok("wlan10", "")}, true},
		{"CR in the head", []AirPortVAP{{Name: "wlan10", Parent: "bwl1", Head: "a=b\r\n", Tail: "c=d\n"}}, true},
		{"CR in the tail", []AirPortVAP{{Name: "wlan10", Parent: "bwl1", Head: "a=b\n", Tail: "c=d\r\n"}}, true},
		{"a later VAP is bad", []AirPortVAP{ok("wlan10", "bwl1"), ok("wlan9", "bwl0")}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkAirPortVAPs(tc.vaps); (err != nil) != tc.bad {
				t.Errorf("err = %v, want error: %v", err, tc.bad)
			}
		})
	}
}

func TestApplyAirPortRefusesBeforeRunning(t *testing.T) {
	// neither call may reach a shell: both fail before it
	if _, err := NewClient(Target{Addr: "local"}).ApplyAirPort(nil); err == nil {
		t.Error("an OpenWrt client must refuse")
	}
	cl := NewClient(Target{Addr: "local", Platform: PlatformAirPort})
	if _, err := cl.ApplyAirPort([]AirPortVAP{{Name: "wlan9", Parent: "bwl0", Head: "a=b\n", Tail: "c=d\n"}}); err == nil {
		t.Error("wlan9 must be refused")
	}
}

func TestRunStdinRefusesAnAirPort(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "ran")
	cl := NewClient(Target{Addr: "local", Platform: PlatformAirPort})
	out, err := cl.RunStdin("echo hi > '"+filepath.ToSlash(mark)+"'", []byte("data"))
	if !errors.Is(err, ErrAirPort) {
		t.Errorf("err = %v, want ErrAirPort", err)
	}
	if out != nil {
		t.Errorf("out = %q", out)
	}
	if _, serr := os.Stat(mark); serr == nil {
		t.Error("the command ran")
	}
}

// ---- reading the unit ----

const apParseBase = `=== sys
release=6.0
hostname=airport-test
now=1000154
boottime=1000000
physmem=268435456
loadavg=0.23 0.08 0.03
cp_time=user = 89, nice = 0, sys = 211, intr = 368, idle = 14863
model=syAM=TimeCapsule8,119
`

const apRadioSections = `=== radio bwl1
bwl1: flags=8843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST> metric 0 mtu 1500
	ssid "" channel 6 (2437 Mhz 11g ht/20)
=== radio bwl0
bwl0: flags=8843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST> metric 0 mtu 1500
	ssid "" channel 36 (5180 Mhz 11a vht/80)
`

const apUplink = `=== iface wlan9
wlan9: flags=ffff8963<UP,BROADCAST,NOTRAILERS,RUNNING,PROMISC,SIMPLEX,MULTICAST> metric 0 mtu 1500
	ether 02:aa:00:00:00:03
	ssid vwire-0123456789abcdef channel 40 (5200 Mhz 11a vht/80) bssid 02:bb:00:00:00:02
	authmode WPA2/802.11i privacy OFF txpower 31.5 dotd rtsthreshold 2347
	media: manual
	status: associated
`

const apAP24 = `=== iface wlan10
wlan10: flags=ffffe963<UP,BROADCAST,NOTRAILERS,RUNNING,PROMISC,SIMPLEX,LINK1,LINK2,MULTICAST> metric 0 mtu 1500
	ether 02:aa:00:00:00:12
	ssid "Example Net" channel 6 (2437 Mhz 11g ht/20) bssid 02:aa:00:00:00:12
	authmode WPA2/802.11i privacy OFF deftxkey 2 txpower 24 dotd
	media: autoselect hostap
	status: running
`

const apAP5 = `=== iface wlan20
wlan20: flags=ffffe963<UP,BROADCAST,NOTRAILERS,RUNNING,PROMISC,SIMPLEX,LINK1,LINK2,MULTICAST> metric 0 mtu 1500
	ether 02:aa:00:00:00:22
	ssid "Example Net" channel 40 (5200 Mhz 11a vht/80) bssid 02:aa:00:00:00:22
	authmode WPA2/802.11i privacy OFF deftxkey 2 txpower 24 dotd
	media: autoselect hostap
	status: running
`

// an AP VAP that was created but is not running (no RUNNING flag)
const apAP21Down = `=== iface wlan21
wlan21: flags=ffffe822<BROADCAST,NOTRAILERS,SIMPLEX,LINK1,LINK2,MULTICAST> metric 0 mtu 1500
	ssid "Example Net" channel 40 (5200 Mhz 11a vht/80)
	media: autoselect hostap
	status: no carrier
`

func apParse(t *testing.T, parts ...string) *Snapshot {
	t.Helper()
	s, err := ParseAirPort([]byte(strings.Join(parts, "")), time.Unix(1000200, 0))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func apCheckBothRadios(t *testing.T, s *Snapshot) {
	t.Helper()
	if len(s.Radios) != 2 {
		t.Fatalf("radios: %d, want 2: %+v", len(s.Radios), s.Radios)
	}
	if s.Radios[0].Band != "ng" || s.Radios[0].Phy != "bwl1" || s.Radios[1].Band != "na" || s.Radios[1].Phy != "bwl0" {
		t.Errorf("order: %s/%s then %s/%s, want ng/bwl1 then na/bwl0",
			s.Radios[0].Band, s.Radios[0].Phy, s.Radios[1].Band, s.Radios[1].Phy)
	}
}

func TestParseAirPortReportsBothRadios(t *testing.T) {
	t.Run("no AP VAP at all", func(t *testing.T) {
		s := apParse(t, apParseBase, apRadioSections)
		apCheckBothRadios(t, s)
		for _, r := range s.Radios {
			if len(r.VAPs) != 0 {
				t.Errorf("%s has VAPs: %+v", r.Phy, r.VAPs)
			}
		}
		if s.Radios[0].Channel != 6 || s.Radios[1].Channel != 36 || s.Radios[1].Width != 80 {
			t.Errorf("idle radios: %d/%d (%d MHz)", s.Radios[0].Channel, s.Radios[1].Channel, s.Radios[1].Width)
		}
		if s.UplinkChannel("na") != 0 || s.UplinkChannel("ng") != 0 {
			t.Error("no uplink expected")
		}
	})
	t.Run("no AP VAP and the radios show nothing", func(t *testing.T) {
		apCheckBothRadios(t, apParse(t, apParseBase))
	})
	t.Run("only the 5 GHz uplink", func(t *testing.T) {
		s := apParse(t, apParseBase, apRadioSections, apUplink)
		apCheckBothRadios(t, s)
		if len(s.Radios[0].VAPs) != 0 || len(s.Radios[1].VAPs) != 1 || s.Radios[1].VAPs[0].Mode != "Client" {
			t.Errorf("VAPs: %+v / %+v", s.Radios[0].VAPs, s.Radios[1].VAPs)
		}
		if got := s.UplinkChannel("na"); got != 40 {
			t.Errorf("UplinkChannel(na) = %d, want 40", got)
		}
		if got := s.UplinkChannel("ng"); got != 0 {
			t.Errorf("UplinkChannel(ng) = %d, want 0", got)
		}
		if s.APVAP("wlan9") != nil {
			t.Error("APVAP must not return the uplink VAP")
		}
	})
	t.Run("only the 5 GHz uplink, listed before the radio sections", func(t *testing.T) {
		apCheckBothRadios(t, apParse(t, apParseBase, apUplink, apRadioSections))
	})
	t.Run("AP VAPs wlan10 and wlan20 plus the uplink", func(t *testing.T) {
		s := apParse(t, apParseBase, apRadioSections, apAP24, apAP5, apUplink)
		apCheckBothRadios(t, s)
		if len(s.Radios[0].VAPs) != 1 || s.Radios[0].VAPs[0].Iface != "wlan10" {
			t.Errorf("2.4 GHz VAPs: %+v", s.Radios[0].VAPs)
		}
		v5 := s.Radios[1].VAPs
		if len(v5) != 2 || v5[0].Iface != "wlan20" || v5[0].Mode != "Master" || v5[1].Iface != "wlan9" || v5[1].Mode != "Client" {
			t.Errorf("5 GHz VAPs (AP before uplink): %+v", v5)
		}
		if got := s.UplinkChannel("na"); got != 40 {
			t.Errorf("UplinkChannel(na) = %d", got)
		}
		if got := s.UplinkChannel("ng"); got != 0 {
			t.Errorf("UplinkChannel(ng) = %d", got)
		}
	})
	t.Run("AP VAPs listed 5 GHz first still give 2.4 GHz first", func(t *testing.T) {
		apCheckBothRadios(t, apParse(t, apParseBase, apAP5, apUplink, apAP24, apRadioSections))
	})
	t.Run("only a 2.4 GHz AP VAP", func(t *testing.T) {
		apCheckBothRadios(t, apParse(t, apParseBase, apRadioSections, apAP24))
	})
}

func TestSnapshotAPVAP(t *testing.T) {
	s := apParse(t, apParseBase, apRadioSections, apAP24, apAP5, apAP21Down, apUplink)
	for _, name := range []string{"wlan10", "wlan20"} {
		v := s.APVAP(name)
		if v == nil || v.Iface != name || v.Mode != "Master" {
			t.Errorf("APVAP(%s) = %+v", name, v)
		}
	}
	if v := s.APVAP("wlan10"); v != nil && v.Channel != 6 {
		t.Errorf("wlan10 channel %d", v.Channel)
	}
	for _, name := range []string{"wlan9", "wlan21", "wlan11", "wlan0", ""} {
		if v := s.APVAP(name); v != nil {
			t.Errorf("APVAP(%q) = %+v, want nil (uplink, not running or absent)", name, v)
		}
	}
	if len(s.Radios[1].VAPs) != 2 {
		t.Errorf("a VAP that is not running must be left out: %+v", s.Radios[1].VAPs)
	}
}

// With an automatic channel only the radio's first VAP chooses; the others take
// that VAP's channel on the device.
func TestAirPortAutoChannelFollowsTheFirstVAP(t *testing.T) {
	w := translate.Wanted{
		Radios: []translate.WantedRadio{{Name: "wifi0", Band: "2g"}},
		WLANs: []translate.WantedWLAN{
			{N: "1", Radio: "wifi0", SSID: "Example Net", Security: "none"},
			{N: "2", Radio: "wifi0", SSID: "Example Two", Security: "none"},
		},
	}
	vaps, _ := AirPortVAPs(w, 0)
	if len(vaps) != 2 {
		t.Fatalf("%d VAPs, want 2", len(vaps))
	}
	if vaps[0].ChannelOf != "" || !strings.Contains(vaps[0].Tail, "acs=1\n") {
		t.Errorf("first VAP: ChannelOf %q, tail %q", vaps[0].ChannelOf, vaps[0].Tail)
	}
	if vaps[1].ChannelOf != "wlan10" || !strings.Contains(vaps[1].Tail, "acs=0\n") || strings.Contains(vaps[1].Tail, "channel=") {
		t.Errorf("second VAP: ChannelOf %q, tail %q", vaps[1].ChannelOf, vaps[1].Tail)
	}
	if vaps[0].Tune || vaps[1].Tune {
		t.Error("an automatic channel is never tuned")
	}
	// the script gets the channel of the first VAP from the device, never from
	// the rendered config
	s := AirPortApplyScript(vaps)
	if !strings.Contains(s, `echo "channel=$c" >> $D/$v.conf`) {
		t.Error("the script does not append the first VAP's channel on the device")
	}
	calls := apRadioCalls(t, s)
	if len(calls) != 2 || calls[0].channel != "auto" || calls[0].names != "wlan10 wlan11" || calls[1].channel != "-" {
		t.Errorf("radio calls %+v", calls)
	}
	if err := checkAirPortVAPs([]AirPortVAP{{Name: "wlan11", Parent: "bwl1", ChannelOf: "wlan9"}}); err == nil {
		t.Error("following the uplink VAP was accepted")
	}
}
