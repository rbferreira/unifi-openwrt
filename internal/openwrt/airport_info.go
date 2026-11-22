// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// What else an AirPort tells about itself: the board's temperatures and fan
// (envstat), the firmware version (acp syVs) and the country its radio uses
// (wl country). Also what the daemon must not claim for it.

// Sensor is one envstat reading.
type Sensor struct {
	Name  string
	Value float64 // degC for a temperature, RPM for a fan
}

// Thermal is the board's temperatures and fan.
type Thermal struct {
	Temps   []Sensor
	Fans    []Sensor
	Duty    int // fan duty, percent; valid when HasDuty
	HasDuty bool
}

// Hottest is the highest board temperature in whole degrees C: the T_*
// sensors (T_internal, T_external1..3 on the A1470) when there are any, else
// every temperature read. ok is false without one.
func (t Thermal) Hottest() (deg int, ok bool) {
	best := math.Inf(-1)
	pick := func(onlyT bool) {
		for _, s := range t.Temps {
			if onlyT && !strings.HasPrefix(strings.ToLower(s.Name), "t_") {
				continue
			}
			best = math.Max(best, s.Value)
		}
	}
	pick(true)
	if math.IsInf(best, -1) {
		pick(false)
	}
	if math.IsInf(best, -1) {
		return 0, false
	}
	return int(math.Round(best)), true
}

// parseEnvstat reads `envstat`: one sensor per line, "name: value ... unit",
//
//	T_internal:      48.100                                   degC
//	fan:             1772                                     RPM
//
// A temperature has unit degC, a fan RPM; a line whose name has "duty" gives
// the fan duty. Anything else (the accelerometer axes, headers, "[device]"
// lines) is skipped, and so is a temperature outside -50..150 (a sensor that
// is not there reads as a sentinel). The exact layout on the unit is not
// recorded in gaps-device.md; this reads the NetBSD envstat shape.
func parseEnvstat(text string) Thermal {
	var t Thermal
	for _, line := range strings.Split(text, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if !ok || name == "" || len(f) == 0 || strings.HasPrefix(name, "[") {
			continue
		}
		v, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			continue
		}
		unit := strings.ToLower(f[len(f)-1])
		switch {
		case strings.Contains(strings.ToLower(name), "duty"), strings.EqualFold(name, "Fan_dcy"): // the A1470 calls it Fan_dcy
			t.Duty, t.HasDuty = int(math.Round(v)), v >= 0 && v <= 100
		case unit == "degc" && v >= -50 && v <= 150:
			t.Temps = append(t.Temps, Sensor{name, v})
		case unit == "rpm" && v >= 0:
			t.Fans = append(t.Fans, Sensor{name, v})
		}
	}
	return t
}

var apCountryRE = regexp.MustCompile(`^([A-Z]{2})(?:\s|$)`)

// parseCountry reads `wl country`: "BR (BR/0) Brazil", the upper-case code
// first. "" when it is not that shape.
func parseCountry(text string) string {
	if m := apCountryRE.FindStringSubmatch(strings.TrimSpace(text)); m != nil {
		return m[1]
	}
	return ""
}

// parseFirmware reads `acp syVs`: "syVs=7.9.1" (or "syVs = 7.9.1", or the bare
// value).
func parseFirmware(text string) string {
	v := strings.TrimSpace(text)
	if k, rest, ok := strings.Cut(v, "="); ok && strings.HasPrefix(strings.TrimSpace(k), "syVs") {
		v = strings.TrimSpace(rest)
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return ""
	}
	return v
}

// wifiCapWPA3 is the WPA3 bit of the wifi_caps word (the -wifi-caps flag help
// names it: 131072). It is the only bit of that word the code knows by name.
const wifiCapWPA3 = 1 << 17

// AirPortWifiCaps takes from a wifi_caps word what an AirPort is proven unable
// to do. The Broadcom driver does authentication itself and answers "Operation
// not supported" to sae, mfp and fbt (gaps.md, item 16), so WPA3 goes. 802.11
// k, v, r and FILS are in the same word but the code has no table of their
// bit positions, so they are left as claimed.
func AirPortWifiCaps(w int) int { return w &^ wifiCapWPA3 }

// AirPortCountryNotes says, once, that the controller asked for a country the
// unit does not use. The regulatory domain of an AirPort is fixed (sku and
// region of the unit); real is what `wl country` reads, "" when unknown.
func AirPortCountryNotes(w translate.Wanted, real string) []string {
	var asked []string
	seen := map[string]bool{}
	for _, r := range w.Radios {
		if r.Country != "" && r.Country != real && !seen[r.Country] {
			seen[r.Country] = true
			asked = append(asked, r.Country)
		}
	}
	if len(asked) == 0 {
		return nil
	}
	now := "its own"
	if real != "" {
		now = real
	}
	return []string{fmt.Sprintf("country %s requested by the controller: not applied, this unit's regulatory domain is fixed and the radios keep using %s", strings.Join(asked, ", "), now)}
}
