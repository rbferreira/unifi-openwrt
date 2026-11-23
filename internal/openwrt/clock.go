// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ClockTolerance is how far the AP's clock may be from the controller's
// before the daemon repairs it. UniFi's roam announcements are dropped when
// the clocks differ by more than a minute, so this stays well below.
const ClockTolerance = 30 * time.Second

// clockRetry is the pause between two repairs: OpenWrt's ntpd needs a few
// seconds to step the clock after a restart.
const clockRetry = 2 * time.Minute

// ClockGuard decides how to repair a wrong clock on the AP.
//
// Why it exists: on an AP that gets its address by DHCP, OpenWrt starts the
// jailed ntpd before the lease arrives. The jail then holds the empty
// resolv.conf of that moment for good, ntpd never resolves its servers, and
// the clock stays where the last shutdown left it. Restarting the service
// gives it a jail with the real resolv.conf. If that does not help (no NTP
// server reachable from the AP's network), the controller's own time is
// set, which is what the AP is compared against anyway.
type ClockGuard struct {
	last  time.Time
	tries int
}

// Next returns "" (nothing to do), "restart" (restart the time service) or
// "set" (set the clock from the controller's time). offset is the AP's
// clock minus the controller's; now only measures the pause between repairs
// and must come from time.Now, whose monotonic part survives a clock step.
func (g *ClockGuard) Next(offset time.Duration, now time.Time) string {
	if offset.Abs() <= ClockTolerance {
		g.tries = 0
		return ""
	}
	if g.tries > 0 && now.Sub(g.last) < clockRetry {
		return ""
	}
	g.last = now
	g.tries++
	if g.tries == 1 {
		return "restart"
	}
	return "set"
}

// Clock returns the AP's own time. A daemon running on the AP reads its own
// clock; only a daemon on another machine asks the AP.
func (cl *Client) Clock() (time.Time, error) {
	if cl.t.Addr == "local" {
		return time.Now(), nil
	}
	out, err := cl.Run("date +%s")
	if err != nil {
		return time.Time{}, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("date +%%s: %q", out)
	}
	return time.Unix(n, 0), nil
}

// RestartTimeService restarts OpenWrt's ntpd.
func (cl *Client) RestartTimeService() error {
	_, err := cl.Run("/etc/init.d/sysntpd restart")
	return err
}

// SetClock sets the AP's clock.
func (cl *Client) SetClock(t time.Time) error {
	_, err := cl.Run(fmt.Sprintf("date -u -s @%d >/dev/null", t.Unix()))
	return err
}
