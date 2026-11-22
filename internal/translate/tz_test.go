// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"testing"
	"time"
)

func TestParseTZ(t *testing.T) {
	utc := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, time.UTC) }
	for _, c := range []struct {
		name, tz string
		at       time.Time
		want     string // wall clock "2006-01-02 15:04"
	}{
		{"fixed west", "<-03>3", utc(2026, 7, 1, 12, 0), "2026-07-01 09:00"},
		{"fixed with name", "BRT3", utc(2026, 1, 1, 2, 0), "2025-12-31 23:00"},
		{"fixed east, minutes", "<+0530>-5:30", utc(2026, 1, 1, 0, 0), "2026-01-01 05:30"},
		{"utc", "UTC0", utc(2026, 5, 5, 5, 5), "2026-05-05 05:05"},
		{"us summer", "EST5EDT,M3.2.0,M11.1.0", utc(2026, 7, 1, 12, 0), "2026-07-01 08:00"},
		{"us winter", "EST5EDT,M3.2.0,M11.1.0", utc(2026, 12, 1, 12, 0), "2026-12-01 07:00"},
		// 2026: summer time starts Sunday 8 March at 02:00 local standard time = 07:00 UTC
		{"us just before start", "EST5EDT,M3.2.0,M11.1.0", utc(2026, 3, 8, 6, 59), "2026-03-08 01:59"},
		{"us at start", "EST5EDT,M3.2.0,M11.1.0", utc(2026, 3, 8, 7, 0), "2026-03-08 03:00"},
		// ... and ends Sunday 1 November at 02:00 local summer time = 06:00 UTC
		{"us just before end", "EST5EDT,M3.2.0,M11.1.0", utc(2026, 11, 1, 5, 59), "2026-11-01 01:59"},
		{"us at end", "EST5EDT,M3.2.0,M11.1.0", utc(2026, 11, 1, 6, 0), "2026-11-01 01:00"},
		{"no rule = us rule", "EST5EDT", utc(2026, 7, 1, 12, 0), "2026-07-01 08:00"},
		{"eu, last week rule", "CET-1CEST,M3.5.0,M10.5.0/3", utc(2026, 7, 1, 12, 0), "2026-07-01 14:00"},
		{"eu before end", "CET-1CEST,M3.5.0,M10.5.0/3", utc(2026, 10, 25, 0, 59), "2026-10-25 02:59"},
		{"eu after end", "CET-1CEST,M3.5.0,M10.5.0/3", utc(2026, 10, 25, 1, 0), "2026-10-25 02:00"},
		// southern hemisphere: summer time spans the new year
		{"south summer in january", "NZST-12NZDT,M9.5.0,M4.1.0/3", utc(2026, 1, 15, 0, 0), "2026-01-15 13:00"},
		{"south winter in july", "NZST-12NZDT,M9.5.0,M4.1.0/3", utc(2026, 7, 15, 0, 0), "2026-07-15 12:00"},
		{"julian day rule", "AAA5BBB,J60,J300", utc(2026, 6, 1, 12, 0), "2026-06-01 08:00"},
		{"explicit summer offset", "XXX3YYY2,M3.2.0,M11.1.0", utc(2026, 7, 1, 12, 0), "2026-07-01 10:00"},
	} {
		z, err := ParseTZ(c.tz)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := z.Local(c.at).Format("2006-01-02 15:04"); got != c.want {
			t.Errorf("%s: %s at %s = %s, want %s", c.name, c.tz, c.at.Format(time.RFC3339), got, c.want)
		}
	}
}

func TestParseTZBad(t *testing.T) {
	for _, s := range []string{"", "  ", "EST", "5", "EST5EDT,M13.1.0,M11.1.0", "EST5EDT,M3.2.0", "EST5EDT,M3.9.0,M11.1.0",
		"EST5EDT,J0,J5", "<abc", "EST5:99", "Nowhere/Land"} {
		if z, err := ParseTZ(s); err == nil {
			t.Errorf("%q parsed as %q", s, z)
		}
	}
}

func TestTimezoneOf(t *testing.T) {
	if got := TimezoneOf(ParseCfg("system.timezone=CST6CDT,M3.2.0,M11.1.0\nlocale.timezone=GMT0\n")); got != "CST6CDT,M3.2.0,M11.1.0" {
		t.Errorf("system.timezone first: %q", got)
	}
	if got := TimezoneOf(ParseCfg("locale.timezone=<-03>3\n")); got != "<-03>3" {
		t.Errorf("locale.timezone: %q", got)
	}
	if got := TimezoneOf(ParseCfg("radio.1.phyname=wifi0\n")); got != "" {
		t.Errorf("none: %q", got)
	}
}
