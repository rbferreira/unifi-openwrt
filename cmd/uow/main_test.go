// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"net"
	"testing"

	"github.com/antoniocosta/unifi-openwrt/internal/inform"
)

// After a restart the daemon must start even when the AP cannot resolve
// the inform host (.invalid never resolves): it uses the learned address.
func TestLocalIPTowardLearnedAddr(t *testing.T) {
	inform.SetInformAddr("unifi.invalid", "127.0.0.1")
	ip, err := localIPToward("http://unifi.invalid:8080/inform")
	if err != nil || ip != "127.0.0.1" {
		t.Fatalf("%q %v", ip, err)
	}
}

// The check after an apply dials the controller like the inform POST does,
// so a name the AP cannot resolve does not roll every apply back.
func TestReachableLearnedAddr(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	inform.SetInformAddr("ctrl.invalid", "127.0.0.1")
	if err := reachable("http://ctrl.invalid:" + port + "/inform"); err != nil {
		t.Errorf("learned address: %v", err)
	}
}
