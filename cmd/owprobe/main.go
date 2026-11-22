// SPDX-License-Identifier: GPL-2.0-or-later

// owprobe reads an OpenWrt AP once (read-only) and prints the UniFi fields
// the daemon would send. Client MACs are shortened in the output. With
// -ap-platform airport it does the same for an Apple AirPort base station.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/openwrt"
)

func main() {
	addr := flag.String("addr", "192.168.1.2:22", "AP ssh address")
	user := flag.String("user", "root", "ssh user")
	key := flag.String("key", "ap_ed25519", "ssh private key for the AP")
	kh := flag.String("known-hosts", "ap_known_hosts", "known_hosts file with the AP's host key")
	platform := flag.String("ap-platform", "", `what the AP is: empty = OpenWrt; "airport" = an Apple AirPort base station`)
	pwFile := flag.String("ap-password-file", "", "log in with the password in this file instead of the key (AirPort: the admin password)")
	raw := flag.String("raw", "", "also save the raw collection output here (mode 600)")
	flag.Parse()
	t, err := openwrt.NewTarget(*addr, *user, *key, *kh, *platform, *pwFile)
	if err != nil {
		log.Fatal(err)
	}
	cl := openwrt.NewClient(t)
	defer cl.Close()
	s1, err := cl.Collect()
	if err != nil {
		log.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	s2, err := cl.Collect()
	if err != nil {
		log.Fatal(err)
	}
	if *raw != "" {
		out, err := cl.CollectRaw()
		if err != nil {
			log.Fatalf("-raw: %v", err)
		}
		os.WriteFile(*raw, out, 0o600)
	}
	fmt.Printf("model=%q host=%q release=%q mac=%s ip=%s/%s gw=%s uptime=%d radios=%d ports=%d\n",
		s2.Model, s2.Hostname, s2.Release, s2.MAC, s2.IP, s2.Netmask, s2.Gateway, s2.Uptime, len(s2.Radios), len(s2.Ports))
	b, _ := json.MarshalIndent(s2.Fields(s1, openwrt.CfgInfo{}, nil), "", "  ")
	fmt.Println(regexp.MustCompile(`"([0-9a-f]{2}):([0-9a-f]{2}):[0-9a-f:]{11}"`).ReplaceAllString(string(b), `"$1:$2:…"`))
}
