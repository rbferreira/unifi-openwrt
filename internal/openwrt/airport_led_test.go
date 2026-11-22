// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"sync"
	"testing"
	"time"
)

func TestAirPortLEDPlan(t *testing.T) {
	blink := func(s LEDState, b string) LEDState { s.Blink = b; return s }
	dim := LEDBlue
	dim.Brightness = 40
	for name, tc := range map[string]struct {
		in   LEDState
		want apLEDPlan
	}{
		"adopted (steady blue)":       {LEDBlue, apLEDPlan{Value: apLEDGreen}},
		"blue at low brightness":      {dim, apLEDPlan{Value: apLEDGreen}},
		"not adopted (white)":         {LEDWhite, apLEDPlan{Value: apLEDAmber}},
		"starting (flashing white)":   {blink(LEDWhite, Flash), apLEDPlan{Value: apLEDAmber}},
		"busy (alternating)":          {blink(LEDWhite, Alternate), apLEDPlan{Value: apLEDAmber}},
		"locate (fast flashing blue)": {blink(LEDBlue, FastFlash), apLEDPlan{Value: apLEDAmber, Blink: true}},
		"locate with another colour":  {blink(LEDState{R: 255, Brightness: 255}, FastFlash), apLEDPlan{Value: apLEDAmber, Blink: true}},
		"custom green":                {LEDState{G: 200, Brightness: 255}, apLEDPlan{Value: apLEDGreen}},
		"custom red":                  {LEDState{R: 255, Brightness: 255}, apLEDPlan{Value: apLEDAmber}},
		"custom yellow":               {LEDState{R: 255, G: 255, Brightness: 255}, apLEDPlan{Value: apLEDAmber}},
		"off in the console":          {LEDDark, apLEDPlan{Value: apLEDAuto, Off: true}},
		"zero brightness":             {LEDState{B: 255}, apLEDPlan{Value: apLEDAuto, Off: true}},
	} {
		if got := airportLEDPlan(tc.in); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
	if airportLEDCmd(1) != "/usr/bin/acp -q LEDc=1" || airportLEDCmd(0) != "/usr/bin/acp -q LEDc=0" {
		t.Error("command")
	}
}

func TestAPBlinkAlternatesAndStops(t *testing.T) {
	var mu sync.Mutex
	var seen []int
	b := startAPBlink(func(v int) error {
		mu.Lock()
		seen = append(seen, v)
		mu.Unlock()
		return nil
	}, 5*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	b.Stop()
	mu.Lock()
	n := len(seen)
	got := append([]int(nil), seen...)
	mu.Unlock()
	if n < 4 {
		t.Fatalf("only %d beats: %v", n, got)
	}
	for i, v := range got {
		want := apLEDAmber
		if i%2 == 1 {
			want = apLEDGreen
		}
		if v != want {
			t.Fatalf("beat %d = %d, want %d: %v", i, v, want, got)
		}
	}
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != n {
		t.Errorf("the blink went on after Stop: %d -> %d", n, len(seen))
	}
	var nilBlink *apBlink
	nilBlink.Stop() // no blink running: Stop is harmless
}

func TestAPBlinkSurvivesFailures(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	b := startAPBlink(func(int) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return errAPTest
	}, 3*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	b.Stop()
	mu.Lock()
	defer mu.Unlock()
	if calls < 3 {
		t.Errorf("gave up after %d calls", calls)
	}
}

type apTestErr string

func (e apTestErr) Error() string { return string(e) }

const errAPTest = apTestErr("acp: no")

func TestBeepOnAnAirPortIsQuiet(t *testing.T) {
	ok, err := NewClient(Target{Addr: "local", Platform: PlatformAirPort}).Beep()
	if ok || err != nil {
		t.Errorf("Beep = %v, %v: an AirPort has no speaker and that is not an error", ok, err)
	}
}
