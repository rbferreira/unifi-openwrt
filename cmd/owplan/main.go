// SPDX-License-Identifier: GPL-2.0-or-later

// owplan prints the changes that would make an AP match the controller's
// system_cfg. Read-only: it never applies anything. For an OpenWrt AP these are
// the UCI changes; with -ap-platform airport they are the hostapd VAPs the daemon
// would run on an Apple AirPort base station, with the daemon's notes (schedules
// at this moment, what is left out, what is not applied). It does not choose an
// automatic channel: that needs the daemon's neighbour scan.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/openwrt"
	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

func main() {
	addr := flag.String("addr", "192.168.1.2:22", "AP ssh address")
	user := flag.String("user", "root", "ssh user")
	key := flag.String("key", "ap_ed25519", "ssh private key for the AP")
	kh := flag.String("known-hosts", "ap_known_hosts", "known_hosts file with the AP's host key")
	platform := flag.String("ap-platform", "", `what the AP is: empty = OpenWrt; "airport" = an Apple AirPort base station`)
	pwFile := flag.String("ap-password-file", "", "log in with the password in this file instead of the key (AirPort: the admin password)")
	cfgPath := flag.String("cfg", "system_cfg.txt", "system_cfg from the controller (saved by the daemon in its state directory)")
	flag.Parse()
	cfg, err := os.ReadFile(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	t, err := openwrt.NewTarget(*addr, *user, *key, *kh, *platform, *pwFile)
	if err != nil {
		log.Fatal(err)
	}
	cl := openwrt.NewClient(t)
	defer cl.Close()
	if cl.IsAirPort() {
		planAirPort(cl, translate.ParseCfg(string(cfg)))
		return
	}
	out, err := cl.Run("uci show wireless; uci show system; uci show network")
	if err != nil {
		log.Fatal(err)
	}
	f := cl.Features()
	f.APAddr, _, _ = net.SplitHostPort(*addr)
	fmt.Printf("AP hostapd features: %+v\n", f)
	r := translate.Plan(translate.ParseCfg(string(cfg)), translate.ParseUCI(string(out)), f)
	fmt.Printf("%d change(s):\n", len(r.Ops))
	for _, op := range r.Ops {
		fmt.Println("  " + op.String())
	}
	fmt.Println("notes:")
	for _, n := range r.Notes {
		fmt.Println("  " + n)
	}
	fmt.Printf("not translated (yet): %v\n", r.Skipped)
}

// planAirPort prints the VAPs the daemon would run for cfg on this base station.
// A passphrase is never printed (AirPortVAP.String has none).
func planAirPort(cl *openwrt.Client, cfg translate.Cfg) {
	snap, err := cl.Collect()
	if err != nil {
		log.Fatal(err)
	}
	p := openwrt.PlanAirPort(cfg, snap, time.Now(), nil, nil)
	fmt.Printf("AirPort %q: %d VAP(s):\n", snap.Model, len(p.VAPs))
	for _, v := range p.VAPs {
		fmt.Println("  " + v.String())
	}
	fmt.Println("notes:")
	for _, n := range p.Notes {
		fmt.Println("  " + n)
	}
}
