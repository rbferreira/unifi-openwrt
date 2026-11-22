// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestWLANSchedule(t *testing.T) {
	c := ParseCfg("wireless.6.schedule_enabled=enabled\nwireless.6.schedule_invert=enabled\nwireless.6.schedule_mon.0=0800-0900\nwireless.6.schedule_mon.1=1800-1900\nwireless.6.schedule_sun.0=bad\n")
	s := wlanSchedule(c, "wireless.6.", "sec")
	if s == nil || !s.OnDuring || !reflect.DeepEqual(s.Windows["mon"], []string{"0800-0900", "1800-1900"}) || len(s.Windows["sun"]) != 0 {
		t.Fatalf("%+v", s)
	}
	if wlanSchedule(ParseCfg("wireless.1.schedule_enabled=disabled\n"), "wireless.1.", "x") != nil {
		t.Fatal("disabled schedule")
	}
}

func TestWLANOn(t *testing.T) {
	at := func(day, hhmm string) time.Time {
		// 2026-11-16 is a Monday
		d := map[string]int{"mon": 16, "tue": 17, "wed": 18, "thu": 19, "fri": 20, "sat": 21, "sun": 22}[day]
		h, _ := strconv.Atoi(hhmm[:2])
		m, _ := strconv.Atoi(hhmm[2:])
		return time.Date(2026, 11, d, h, m, 0, 0, time.UTC)
	}
	off := Schedule{Windows: map[string][]string{"mon": {"0000-0600", "2200-2400"}, "sat": {"0100-0300"}}}                // off during
	on := Schedule{OnDuring: true, Windows: map[string][]string{"mon": {"0800-1700"}, "tue": {"0800-1700", "1900-2000"}}} // on during
	wrap := Schedule{Windows: map[string][]string{"fri": {"2200-0600"}}}                                                  // off during, runs into Saturday
	wrapOn := Schedule{OnDuring: true, Windows: map[string][]string{"sun": {"2300-0200"}}}                                // on Sunday night into Monday
	for _, c := range []struct {
		name string
		s    Schedule
		day  string
		hhmm string
		want bool
	}{
		{"off-during: before the window", off, "mon", "0000", false},
		{"off-during: inside", off, "mon", "0559", false},
		{"off-during: end is exclusive", off, "mon", "0600", true},
		{"off-during: other day untouched", off, "tue", "0300", true},
		{"off-during: 2400 reaches the end of the day", off, "mon", "2359", false},
		{"off-during: next day is free", off, "tue", "0000", true},
		{"off-during: saturday window", off, "sat", "0200", false},
		{"off-during: saturday after", off, "sat", "0300", true},
		{"on-during: before", on, "mon", "0759", false},
		{"on-during: start inclusive", on, "mon", "0800", true},
		{"on-during: end exclusive", on, "mon", "1700", false},
		{"on-during: second window", on, "tue", "1930", true},
		{"on-during: a day with no window is off", on, "wed", "1000", false},
		{"wrap: evening part", wrap, "fri", "2230", false},
		{"wrap: before the evening", wrap, "fri", "2159", true},
		{"wrap: carried into saturday", wrap, "sat", "0559", false},
		{"wrap: saturday after the end", wrap, "sat", "0600", true},
		{"wrap: friday morning is not part of it", wrap, "fri", "0300", true},
		{"wrap: sunday morning not part of it", wrap, "sun", "0300", true},
		{"wrap on-during: sunday night", wrapOn, "sun", "2330", true},
		{"wrap on-during: monday small hours", wrapOn, "mon", "0130", true},
		{"wrap on-during: monday after", wrapOn, "mon", "0200", false},
		{"wrap on-during: sunday before", wrapOn, "sun", "2259", false},
		{"week wraps: sunday window carries into monday", wrapOn, "mon", "0001", true},
		{"no windows, off-during: always on", Schedule{Windows: map[string][]string{}}, "wed", "1200", true},
		{"no windows, on-during: never on", Schedule{OnDuring: true, Windows: map[string][]string{}}, "wed", "1200", false},
		{"empty window (equal ends)", Schedule{Windows: map[string][]string{"mon": {"1000-1000"}}}, "mon", "1000", true},
	} {
		if got := WLANOn(c.s, at(c.day, c.hhmm)); got != c.want {
			t.Errorf("%s: WLANOn at %s %s = %v, want %v", c.name, c.day, c.hhmm, got, c.want)
		}
	}
}
