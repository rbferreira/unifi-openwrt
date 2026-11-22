// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"regexp"
	"sort"
	"strconv"
	"time"
)

// Schedule is a WLAN's broadcast schedule on one AP interface: the AP turns
// the interface on during the windows (Invert: the controller's
// schedule_invert=enabled, "on during") or off during them.
type Schedule struct {
	Section  string              // UCI wifi-iface section
	OnDuring bool                // true: up only in the windows
	Windows  map[string][]string // day (mon..sun) -> "HHMM-HHMM"
}

var window = regexp.MustCompile(`^[0-2][0-9][0-5][0-9]-[0-2][0-9][0-5][0-9]$`)

var days = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// wlanSchedule reads wireless.N.schedule_* (nil when not scheduled).
func wlanSchedule(cfg Cfg, w, section string) *Schedule {
	if cfg[w+"schedule_enabled"] != "enabled" {
		return nil
	}
	s := &Schedule{Section: section, OnDuring: cfg[w+"schedule_invert"] == "enabled", Windows: map[string][]string{}}
	for _, d := range days {
		for i := 0; i < 32; i++ { // schedule_<day>.<i> (leaf keys)
			if v := cfg[w+"schedule_"+d+"."+strconv.Itoa(i)]; window.MatchString(v) {
				s.Windows[d] = append(s.Windows[d], v)
			}
		}
		sort.Strings(s.Windows[d])
	}
	return s
}

// hhmm is "HHMM" in minutes after midnight; 2400 is the end of the day.
func hhmm(s string) int {
	h, _ := strconv.Atoi(s[:2])
	m, _ := strconv.Atoi(s[2:])
	return h*60 + m
}

// inWindow reports whether minute tt of a day is inside the window "HHMM-HHMM"
// that starts on that day. A window that ends before it starts (2200-0600) runs
// past midnight: only its first part (from the start to the end of the day)
// belongs to the day it starts on; the rest is the next day's morning (see
// WLANOn). A window with equal ends is empty.
func inWindow(w string, tt int) bool {
	a, b := hhmm(w[:4]), hhmm(w[5:])
	if a <= b {
		return tt >= a && tt < b
	}
	return tt >= a
}

// carried reports whether minute tt of a day falls in the part of a window that
// started the day before and runs past midnight.
func carried(w string, tt int) bool {
	a, b := hhmm(w[:4]), hhmm(w[5:])
	return a > b && tt < b
}

// WLANOn says whether the WLAN is on the air at t, which must already be the
// wall clock of the controller's time zone (TZ.Local). OnDuring: on only inside
// the windows; otherwise (the controller's default) on except inside them. A
// scheduled WLAN with no window at all is therefore always on, or, with
// OnDuring, never.
//
// A window that wraps past midnight belongs to the day it starts on: "mon
// 2200-0600" is Monday 22:00 to Tuesday 06:00. (The OpenWrt script reads it as
// Monday 00:00-06:00 plus Monday 22:00-24:00; the controller's own windows
// do not wrap, so the two readings only differ for one the console never sends.)
func WLANOn(s Schedule, t time.Time) bool {
	tt := t.Hour()*60 + t.Minute()
	today := days[(int(t.Weekday())+6)%7] // days starts on Monday
	yesterday := days[(int(t.Weekday())+5)%7]
	in := false
	for _, w := range s.Windows[today] {
		if inWindow(w, tt) {
			in = true
		}
	}
	for _, w := range s.Windows[yesterday] {
		if carried(w, tt) {
			in = true
		}
	}
	if s.OnDuring {
		return in
	}
	return !in
}
