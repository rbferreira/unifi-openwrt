// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The controller sends the site's time zone as a POSIX TZ string (what OpenWrt's
// system.timezone takes: "EST5EDT,M3.2.0,M11.1.0", "<-03>3"), in system.timezone
// or locale.timezone. Go reads zoneinfo names, not these, and the zoneinfo
// database may be missing on the machine the daemon runs on, so the rules are
// read here. An IANA name ("America/Sao_Paulo") is also accepted, through the
// zoneinfo database when the machine has one.

// TZ is a parsed time zone: a fixed offset, or a standard/summer pair with
// the rules that switch between them.
type TZ struct {
	raw        string
	loc        *time.Location // set for an IANA name; the rest is unused then
	stdName    string
	std        int // seconds WEST of UTC, as POSIX writes it
	dstName    string
	dst        int // seconds west of UTC while summer time is on
	hasDST     bool
	start, end tzRule
}

// tzRule is the day summer time starts or ends: Mm.w.d (kind 'M'), Jn (kind
// 'J', 1..365, no Feb 29) or n (kind 'N', 0..365), at secs after local midnight.
type tzRule struct {
	kind    byte
	m, w, d int
	n       int
	secs    int
}

// String is the zone as the controller gave it.
func (z TZ) String() string { return z.raw }

// TimezoneOf is the controller's time zone text from a system_cfg ("" = not
// given): system.timezone, else locale.timezone.
func TimezoneOf(cfg Cfg) string {
	if tz := strings.TrimSpace(cfg["system.timezone"]); tz != "" {
		return tz
	}
	return strings.TrimSpace(cfg["locale.timezone"])
}

// ParseTZ reads a POSIX TZ string or an IANA zone name.
func ParseTZ(s string) (TZ, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return TZ{}, errors.New("empty time zone")
	}
	z, err := parsePOSIXTZ(s)
	if err == nil {
		return z, nil
	}
	if loc, lerr := time.LoadLocation(s); lerr == nil && strings.ContainsAny(s, "/") {
		return TZ{raw: s, loc: loc}, nil
	}
	return TZ{}, err
}

// Local is t as the wall clock of the zone shows it.
func (z TZ) Local(t time.Time) time.Time {
	if z.loc != nil {
		return t.In(z.loc)
	}
	off, name := -z.std, z.stdName
	if z.hasDST && z.summer(t) {
		off, name = -z.dst, z.dstName
	}
	return t.In(time.FixedZone(name, off))
}

// summer reports whether summer time is on at the instant t.
func (z TZ) summer(t time.Time) bool {
	u := t.UTC()
	y := u.Year()
	// the start is read on the wall clock of standard time, the end on the one of
	// summer time; both become instants
	start := z.start.at(y).Add(time.Duration(z.std) * time.Second)
	end := z.end.at(y).Add(time.Duration(z.dst) * time.Second)
	if start.Before(end) {
		return !u.Before(start) && u.Before(end)
	}
	return !u.Before(start) || u.Before(end) // southern hemisphere: the year starts in summer
}

// at is the rule's moment in year y as a wall-clock time, kept in UTC for the
// arithmetic (the zone offset is added by the caller).
func (r tzRule) at(y int) time.Time {
	var day time.Time
	switch r.kind {
	case 'J': // 1..365, Feb 29 is never counted
		n := r.n
		day = time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
		leap := y%4 == 0 && (y%100 != 0 || y%400 == 0)
		if leap && n >= 60 {
			n++
		}
		day = day.AddDate(0, 0, n-1)
	case 'N': // 0..365, Feb 29 counted
		day = time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, r.n)
	default: // Mm.w.d: day d (0 = Sunday) of week w (5 = the last) of month m
		first := time.Date(y, time.Month(r.m), 1, 0, 0, 0, 0, time.UTC)
		delta := (r.d - int(first.Weekday()) + 7) % 7
		day = first.AddDate(0, 0, delta+7*(r.w-1))
		for day.Month() != time.Month(r.m) { // week 5 that does not exist: the last one
			day = day.AddDate(0, 0, -7)
		}
	}
	return day.Add(time.Duration(r.secs) * time.Second)
}

func parsePOSIXTZ(s string) (TZ, error) {
	z := TZ{raw: s}
	rest := s
	var err error
	if z.stdName, rest, err = tzName(rest); err != nil {
		return TZ{}, err
	}
	if z.std, rest, err = tzOffset(rest, false); err != nil {
		return TZ{}, fmt.Errorf("time zone %q: %v", s, err)
	}
	if rest == "" {
		return z, nil
	}
	if z.dstName, rest, err = tzName(rest); err != nil {
		return TZ{}, fmt.Errorf("time zone %q: %v", s, err)
	}
	z.hasDST = true
	z.dst = z.std - 3600 // summer time is one hour ahead unless told
	if rest != "" && rest[0] != ',' {
		if z.dst, rest, err = tzOffset(rest, false); err != nil {
			return TZ{}, fmt.Errorf("time zone %q: %v", s, err)
		}
	}
	if rest == "" {
		rest = ",M3.2.0,M11.1.0" // no rule given: the US one, as libc does
	}
	parts := strings.Split(rest, ",")
	if len(parts) != 3 || parts[0] != "" {
		return TZ{}, fmt.Errorf("time zone %q: bad summer time rule", s)
	}
	if z.start, err = parseTZRule(parts[1]); err != nil {
		return TZ{}, fmt.Errorf("time zone %q: %v", s, err)
	}
	if z.end, err = parseTZRule(parts[2]); err != nil {
		return TZ{}, fmt.Errorf("time zone %q: %v", s, err)
	}
	return z, nil
}

// tzName takes a zone abbreviation: three or more letters, or anything in <>.
func tzName(s string) (name, rest string, err error) {
	if strings.HasPrefix(s, "<") {
		i := strings.IndexByte(s, '>')
		if i < 2 {
			return "", "", errors.New("bad <name>")
		}
		return s[1:i], s[i+1:], nil
	}
	i := 0
	for i < len(s) && (s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z') {
		i++
	}
	if i < 3 {
		return "", "", errors.New("no zone name")
	}
	return s[:i], s[i:], nil
}

// tzOffset reads [+-]hh[:mm[:ss]] and returns it in seconds. Zone offsets keep
// POSIX's sign (positive = west); a rule time (signed true) keeps its own.
func tzOffset(s string, signed bool) (secs int, rest string, err error) {
	sign := 1
	if s != "" && (s[0] == '+' || s[0] == '-') {
		if s[0] == '-' {
			sign = -1
		}
		s = s[1:]
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == ':') {
		i++
	}
	if i == 0 {
		return 0, "", errors.New("no offset")
	}
	f := strings.Split(s[:i], ":")
	if len(f) > 3 {
		return 0, "", errors.New("bad offset")
	}
	mul := []int{3600, 60, 1}
	for k, p := range f {
		n, e := strconv.Atoi(p)
		if e != nil || n < 0 || n > 167 || (k > 0 && n > 59) {
			return 0, "", errors.New("bad offset")
		}
		secs += n * mul[k]
	}
	return sign * secs, s[i:], nil
}

func parseTZRule(s string) (tzRule, error) {
	r := tzRule{secs: 7200}
	date, at, hasTime := strings.Cut(s, "/")
	if hasTime {
		v, rest, err := tzOffset(at, true)
		if err != nil || rest != "" {
			return r, errors.New("bad rule time")
		}
		r.secs = v
	}
	switch {
	case strings.HasPrefix(date, "M"):
		r.kind = 'M'
		if n, _ := fmt.Sscanf(date, "M%d.%d.%d", &r.m, &r.w, &r.d); n != 3 ||
			r.m < 1 || r.m > 12 || r.w < 1 || r.w > 5 || r.d < 0 || r.d > 6 || strings.Count(date, ".") != 2 {
			return r, errors.New("bad Mm.w.d rule")
		}
	case strings.HasPrefix(date, "J"):
		n, err := strconv.Atoi(date[1:])
		if err != nil || n < 1 || n > 365 {
			return r, errors.New("bad Jn rule")
		}
		r.kind, r.n = 'J', n
	default:
		n, err := strconv.Atoi(date)
		if err != nil || n < 0 || n > 365 {
			return r, errors.New("bad day rule")
		}
		r.kind, r.n = 'N', n
	}
	return r, nil
}
