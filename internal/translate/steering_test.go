// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"reflect"
	"testing"
)

func TestBandSteering(t *testing.T) {
	c := ParseCfg("bandsteering.status=enabled\nbandsteering.mode=prefer_5g\nbandsteering.1.status=enabled\nbandsteering.1.vap.1.devname=wifi0ap0\nbandsteering.1.vap.2.devname=wifi1ap2\n")
	s := BandSteering(c)
	if s.Mode != "prefer_5g" || len(s.Devnames) != 2 {
		t.Fatalf("%+v", s)
	}
	if s := BandSteering(ParseCfg("bandsteering.status=disabled\n")); s.Mode != "" {
		t.Fatalf("off: %+v", s)
	}
}

func TestRoamAssist(t *testing.T) {
	c := ParseCfg(`wireless.1.ssid=home
wireless.1.parent=wifi0
wireless.5.ssid=home
wireless.5.parent=wifi1
wireless.5.btm_disassoc.status=enabled
wireless.5.btm_disassoc.threshold=-75
wireless.6.ssid=iot
wireless.6.parent=wifi1
wireless.6.btm_disassoc.status=enabled
wireless.6.btm_disassoc.threshold=20
wireless.7.ssid=guest
wireless.7.parent=wifi1
wireless.7.btm_disassoc.status=disabled
wireless.7.btm_disassoc.threshold=-70
`)
	// threshold 20 is RSSI above the noise floor: -75 dBm
	want := map[string]int{"home|wifi1": -75, "iot|wifi1": -75}
	if got := RoamAssist(c); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
