// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/openwrt"
)

// A station below the radio's minimum RSSI for 30 s is kicked, with the signal
// the snapshot has per station (the AirPort fills it from ifconfig list sta).
func TestMinRSSIKick(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "system_cfg.txt")
	if err := os.WriteFile(cfg, []byte("stamgr.1.radio=ng\nstamgr.1.minrssi.status=true\nstamgr.1.minrssi.rssi=-75\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := ledCfgPath
	ledCfgPath = cfg
	defer func() { ledCfgPath = old; lowRSSI = map[string]time.Time{} }()
	lowRSSI = map[string]time.Time{}

	const weak, fine, uplink = "02:00:00:00:00:0a", "02:00:00:00:00:0b", "02:00:00:00:00:0c"
	snap := &openwrt.Snapshot{Radios: []openwrt.Radio{{Band: "ng", VAPs: []openwrt.Radio{
		{Mode: "Master", Stations: []openwrt.Station{{MAC: weak, Signal: -80}, {MAC: fine, Signal: -60}}},
		{Mode: "Client", Stations: []openwrt.Station{{MAC: uplink, Signal: -90}}},
	}}}}
	var kicked []string
	kick := func(m string) error { kicked = append(kicked, m); return nil }

	minRSSIKick(kick, snap)
	if len(kicked) != 0 {
		t.Fatalf("kicked at once: %v", kicked)
	}
	lowRSSI[weak] = time.Now().Add(-lowRSSIFor - time.Second) // 30 s have gone by
	minRSSIKick(kick, snap)
	if len(kicked) != 1 || kicked[0] != weak {
		t.Errorf("kicked %v, want only %s (not the strong one, not our own uplink)", kicked, weak)
	}
	if _, still := lowRSSI[weak]; still {
		t.Error("the clock was not reset after the kick")
	}
}
