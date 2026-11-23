// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"testing"
	"time"
)

func TestClockGuard(t *testing.T) {
	now := time.Now()
	var g ClockGuard
	if a := g.Next(20*time.Second, now); a != "" {
		t.Fatalf("within tolerance: %q", a)
	}
	if a := g.Next(-49*time.Minute, now); a != "restart" {
		t.Fatalf("first repair: %q", a)
	}
	if a := g.Next(-49*time.Minute, now.Add(30*time.Second)); a != "" {
		t.Fatalf("ntpd gets time to step the clock: %q", a)
	}
	if a := g.Next(-49*time.Minute, now.Add(clockRetry)); a != "set" {
		t.Fatalf("restart did not help: %q", a)
	}
	if a := g.Next(time.Second, now.Add(clockRetry+time.Minute)); a != "" {
		t.Fatalf("repaired: %q", a)
	}
	// wrong again later (next boot of the jail problem): start over
	if a := g.Next(5*time.Minute, now.Add(time.Hour)); a != "restart" {
		t.Fatalf("after a repair the next one starts with a restart: %q", a)
	}
}
