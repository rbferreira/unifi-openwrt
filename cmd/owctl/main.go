// SPDX-License-Identifier: GPL-2.0-or-later

// owctl runs one AP-side action over SSH, for testing without the controller:
//
//	owctl bans | kick <mac> | ban <mac>... | unblock <mac>... | beep | led on|off|locate
//
// With -ap-platform airport (and -ap-password-file) it talks to an Apple AirPort
// base station: kick, led and beep work; bans, ban and unblock do not exist
// there as one-shot actions (the daemon keeps the base station's driver ACLs from
// the controller's lists while it runs with -apply) and are refused at once.
package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/antoniocosta/unifi-openwrt/internal/openwrt"
)

func main() {
	addr := flag.String("addr", "192.168.1.2:22", "AP ssh address")
	user := flag.String("user", "root", "ssh user")
	key := flag.String("key", "ap_ed25519", "ssh private key for the AP")
	kh := flag.String("known-hosts", "ap_known_hosts", "known_hosts file with the AP's host key")
	platform := flag.String("ap-platform", "", `what the AP is: empty = OpenWrt; "airport" = an Apple AirPort base station`)
	pwFile := flag.String("ap-password-file", "", "log in with the password in this file instead of the key (AirPort: the admin password)")
	flag.Parse()
	a := flag.Args()
	if len(a) == 0 {
		log.Fatal("usage: owctl bans | kick <mac> | ban <mac>... | unblock <mac>... | beep | led on|off|locate")
	}
	if *platform == openwrt.PlatformAirPort {
		switch a[0] {
		case "bans", "ban", "unblock":
			log.Fatalf("owctl %s: not available on an AirPort (its blocked clients are the driver ACLs the daemon keeps from the controller's lists; use kick, led or beep here)", a[0])
		}
	}
	t, err := openwrt.NewTarget(*addr, *user, *key, *kh, *platform, *pwFile)
	if err != nil {
		log.Fatal(err)
	}
	cl := openwrt.NewClient(t)
	defer cl.Close()
	switch {
	case a[0] == "bans":
		var out string
		out, err = cl.Bans()
		fmt.Print(out)
	case a[0] == "kick" && len(a) == 2:
		err = cl.Kick(a[1])
	case a[0] == "ban" && len(a) >= 2:
		err = cl.EnforceBlocked(a[1:])
	case a[0] == "unblock" && len(a) >= 2:
		err = cl.Unblock(a[1:])
	case a[0] == "beep":
		var ok bool
		ok, err = cl.Beep()
		fmt.Println("beeped:", ok)
	case a[0] == "led" && len(a) == 2:
		err = cl.SetLED(a[1])
	default:
		log.Fatal("bad arguments")
	}
	if err != nil {
		log.Fatal(err)
	}
}
