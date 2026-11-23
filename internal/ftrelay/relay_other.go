// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !linux

package ftrelay

import (
	"errors"
	"time"
)

// The relay works on Linux interfaces (rtnetlink, packet sockets, a veth
// pair). On any other system the daemon is built without it: Probe says so
// and nothing else is ever called.

// BSS is one hostapd BSS the relay serves (see relay.go).
type BSS struct {
	Iface  string
	BSSID  MAC
	Bridge string
	WLAN   [2]byte
	Key    []byte
}

// Relay is the relay of relay.go, which does nothing here.
type Relay struct {
	Logf    func(format string, a ...any)
	Verbose bool
	Kick    func(iface string, client MAC)
}

// Probe reports that the relay cannot run on this system.
func Probe() error { return errors.New("the relay needs Linux") }

func (r *Relay) Sync(bss []BSS) error                           { return nil }
func (r *Relay) Serving() []BSS                                 { return nil }
func (r *Relay) Close()                                         {}
func (r *Relay) Joined(iface string, client MAC, now time.Time) {}
func (r *Relay) Left(iface string, client MAC)                  {}
