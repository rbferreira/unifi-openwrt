// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"strings"
	"testing"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

func TestAirPortScrub(t *testing.T) {
	raw := airportSample + "=== hostapd wlan10\nwpa_passphrase=hunter2hunter2\nssid=Home Net\n" +
		"=== env\n" + apEnvSample + "=== country\nBR (BR/0) Brazil\n"
	// a secret inside a section that is kept
	raw = strings.Replace(raw, "release=6.0\n", "release=6.0\nwpa_passphrase=hunter2hunter2\n", 1)
	want, err := ParseAirPort([]byte(raw), time.Unix(1000200, 0))
	if err != nil {
		t.Fatal(err)
	}
	out := string(AirPortScrub([]byte(raw)))
	for _, bad := range []string{"hunter2", "Home Net", "airport-test", "02:aa:00:00:00:01", "2:bb:0:0:0:1", "192.0.2.", "fe80::aa:ff:fe00:1", "=== hostapd", "02:dd:00:00:00:01"} {
		if strings.Contains(out, bad) {
			t.Errorf("dump still holds %q", bad)
		}
	}
	if !strings.Contains(out, "REDACTED") || !strings.Contains(out, "hostname=airport") || !strings.Contains(out, "fe80::1%bridge0") {
		t.Errorf("dump: %s", out)
	}
	got, err := ParseAirPort([]byte(out), time.Unix(1000200, 0))
	if err != nil {
		t.Fatalf("the dump cannot be read back: %v", err)
	}
	if len(got.Radios) != len(want.Radios) || len(got.Ports) != len(want.Ports) || got.Country != "BR" ||
		len(got.Thermal.Temps) != len(want.Thermal.Temps) || len(got.Radios[0].VAPs) != len(want.Radios[0].VAPs) {
		t.Errorf("read back: %d radios %d ports, was %d and %d", len(got.Radios), len(got.Ports), len(want.Radios), len(want.Ports))
	}
	if got.Radios[0].VAPs[0].SSID == "Home Net" || got.Radios[0].VAPs[0].SSID == "" {
		t.Errorf("SSID %q", got.Radios[0].VAPs[0].SSID)
	}
	// the same address is replaced the same way everywhere, so it still matches
	if len(got.Radios[0].VAPs[0].Stations) != 1 || got.ARP["02:00:00:00:00:01"] == "" && len(got.ARP) == 0 {
		t.Errorf("stations %+v arp %v", got.Radios[0].VAPs[0].Stations, got.ARP)
	}
}

func TestAirPortScrubSameSSIDSameName(t *testing.T) {
	out := string(AirPortScrub([]byte("=== iface wlan10\n\tssid \"My Wi-Fi\" channel 6\n=== iface wlan20\n\tssid \"My Wi-Fi\" channel 36\n=== iface wlan11\n\tssid Other channel 1\n")))
	if strings.Count(out, `ssid "ssid1"`) != 2 || !strings.Contains(out, "ssid ssid2 channel 1") {
		t.Errorf("%s", out)
	}
}

func TestPlanAirPort(t *testing.T) {
	snap, err := ParseAirPort([]byte(airportSample), time.Unix(1000200, 0))
	if err != nil {
		t.Fatal(err)
	}
	cfg := translate.ParseCfg(`radio.1.phyname=wifi0
radio.1.ieee_mode=11nght20
radio.1.channel=auto
radio.2.phyname=wifi1
radio.2.ieee_mode=11acvht80
radio.2.channel=auto
wireless.1.ssid=Example Net
wireless.1.parent=wifi0
wireless.1.devname=ath0
wireless.1.schedule_enabled=enabled
wireless.1.schedule_mon.0=0000-2400
wireless.2.ssid=Example Net
wireless.2.parent=wifi0
wireless.2.devname=ath1
aaa.1.wpa=2
aaa.1.wpa.psk=correct horse 1
aaa.2.wpa=2
aaa.2.wpa.psk=correct horse 1
system.timezone=<-03>3
ntpclient.status=enabled
ntpclient.1.server=0.ntp.example
`)
	// Monday 2026-11-16 12:00 UTC-3 = 15:00 UTC: WLAN 1 is inside its (off) window
	now := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	p := PlanAirPort(cfg, snap, now, nil, nil)
	if len(p.VAPs) != 1 || p.VAPs[0].SSID != "Example Net" || len(p.Wanted.WLANs) != 2 || p.SchedKey != "1=false" {
		t.Fatalf("plan: %d VAPs, %d WLANs asked, key %q", len(p.VAPs), len(p.Wanted.WLANs), p.SchedKey)
	}
	joined := strings.Join(p.Notes, "\n")
	for _, want := range []string{"outside its schedule", "time zone", "NTP server(s) 0.ntp.example"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes lack %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "correct horse") {
		t.Error("a passphrase is in the notes")
	}
	// Tuesday: both are on
	p = PlanAirPort(cfg, snap, now.Add(24*time.Hour), nil, nil)
	if len(p.VAPs) != 2 || p.SchedKey != "1=true" {
		t.Errorf("tuesday: %d VAPs key %q", len(p.VAPs), p.SchedKey)
	}
	// with a scan, the automatic 2.4 GHz channel is chosen and the VAPs tune to it
	var chans AutoChannels
	src := func(band string) ([]BSS, time.Time) {
		if band == "ng" {
			return []BSS{nbr(1, -40), nbr(6, -50)}, now.Add(24 * time.Hour)
		}
		return nil, time.Time{}
	}
	p = PlanAirPort(cfg, snap, now.Add(24*time.Hour), &chans, src)
	if len(p.VAPs) != 2 || p.VAPs[0].Channel != 11 || !p.VAPs[0].Tune || p.VAPs[0].Spec != "11" {
		t.Errorf("auto channel: %+v", p.VAPs)
	}
}

func TestNewTarget(t *testing.T) {
	if _, err := NewTarget("h:22", "root", "k", "kh", "openwrt", ""); err == nil {
		t.Error("unknown platform accepted")
	}
	tg, err := NewTarget("h:22", "root", "k", "kh", PlatformAirPort, "")
	if err != nil || tg.Platform != PlatformAirPort || tg.Password != "" {
		t.Errorf("%+v %v", tg, err)
	}
	if _, err := NewTarget("h:22", "root", "k", "kh", "", "does-not-exist.pw"); err == nil {
		t.Error("missing password file accepted")
	}
}
