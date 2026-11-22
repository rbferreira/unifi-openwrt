// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// WLAN schedules on an AirPort. On OpenWrt a cron job on the AP flips the
// interface's `disabled`. The base station has no cron and no uci, so the daemon
// does it: a WLAN that is outside its schedule is left out of the VAP list at
// apply time, and airportKeep (once a minute) applies again when the answer for
// any WLAN has changed. The clock is the daemon's (time.Now), read in the time
// zone the controller sent (translate.ParseTZ); the unit's own clock and zone
// are not consulted. Without a usable time zone the schedules are not enforced
// and the WLANs stay on: switching a network off at the wrong hours is worse
// than not switching it off.

// AirPortSchedule removes from w the WLANs that their schedule says are off at
// now. It returns the reduced Wanted, the notes for the log, and a key that
// names the on/off answer of every scheduled WLAN: when the key of a later call
// differs, a schedule boundary has passed and the VAPs have to be applied again.
// A WLAN the controller itself disabled is not looked at.
func AirPortSchedule(w translate.Wanted, now time.Time) (translate.Wanted, []string, string) {
	var scheduled []translate.WantedWLAN
	for _, l := range w.WLANs {
		if l.Schedule != nil && !l.Disabled {
			scheduled = append(scheduled, l)
		}
	}
	if len(scheduled) == 0 {
		return w, nil, ""
	}
	var notes []string
	label := func(l translate.WantedWLAN) string {
		return fmt.Sprintf("wireless.%s (%q on %s)", l.N, l.SSID, l.Radio)
	}
	tz, err := translate.ParseTZ(w.Timezone)
	if err != nil {
		why := "the controller sent no time zone"
		if w.Timezone != "" {
			why = fmt.Sprintf("the controller's time zone %q is not readable (%v)", w.Timezone, err)
		}
		notes = append(notes, fmt.Sprintf("WLAN schedule not enforced on %d WLAN(s), which stay on: %s", len(scheduled), why))
		return w, notes, ""
	}
	local := tz.Local(now)
	out := w
	out.WLANs = nil
	var key []string
	var off []string
	for _, l := range w.WLANs {
		if l.Schedule == nil || l.Disabled {
			out.WLANs = append(out.WLANs, l)
			continue
		}
		on := translate.WLANOn(*l.Schedule, local)
		key = append(key, fmt.Sprintf("%s=%t", l.N, on))
		if on {
			out.WLANs = append(out.WLANs, l)
		} else {
			off = append(off, label(l))
		}
	}
	sort.Strings(key)
	notes = append(notes, fmt.Sprintf("WLAN schedule (%s, now %s): %d of %d scheduled WLAN(s) on the air", tz, local.Format("Mon 15:04"), len(scheduled)-len(off), len(scheduled)))
	for _, o := range off {
		notes = append(notes, o+": outside its schedule, left off until the next boundary")
	}
	return out, notes, strings.Join(key, " ")
}
