// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"log"
	"strconv"
	"sync"
	"time"
)

// The status LED of an AirPort has three states, set with ACP's LEDc property
// (`acp -q LEDc=N`): 0 = the firmware's own (green, amber when it sees trouble),
// 1 = amber, 2 = green. There is no off and no blue. UniFi's states map as:
//
//	steady blue (adopted, running)   green
//	any other blue or green colour   green
//	white (not adopted, starting)    amber, steady; flashing white too (only Locate blinks)
//	red, orange, yellow              amber
//	fast flashing blue (Locate)      amber and green alternating, once a second
//	dark (LED off in the console)    the firmware's own (0): it cannot be dark; noted once
const (
	apLEDAuto  = 0
	apLEDAmber = 1
	apLEDGreen = 2

	// apBlinkEvery is how long each colour of the Locate blink stays.
	apBlinkEvery = time.Second
)

// apLEDPlan is what to do for one UniFi LED state.
type apLEDPlan struct {
	Value int  // the LEDc value (steady), or the first colour of a blink
	Blink bool // alternate amber and green until the state changes
	Off   bool // the console wants the LED dark: not possible here
}

// airportLEDPlan maps a UniFi LED state onto the three the unit has.
func airportLEDPlan(s LEDState) apLEDPlan {
	switch {
	case s.R == 0 && s.G == 0 && s.B == 0 || s.Brightness == 0:
		return apLEDPlan{Value: apLEDAuto, Off: true}
	case s.Blink == FastFlash:
		return apLEDPlan{Value: apLEDAmber, Blink: true}
	case s.R == 0: // blue, green or cyan: the "all well" family
		return apLEDPlan{Value: apLEDGreen}
	}
	return apLEDPlan{Value: apLEDAmber}
}

// airportLEDCmd is the command that sets LEDc.
func airportLEDCmd(v int) string { return "/usr/bin/acp -q LEDc=" + strconv.Itoa(v) }

// apBlink is a running Locate blink: a goroutine that alternates amber and
// green, so the daemon's loop never waits for it.
type apBlink struct {
	stop chan struct{}
	done sync.WaitGroup
}

// startAPBlink alternates set(amber) and set(green) every period until Stop.
// A failing set is logged once (and again when the error changes) and tried
// again on the next beat.
func startAPBlink(set func(int) error, period time.Duration) *apBlink {
	b := &apBlink{stop: make(chan struct{})}
	b.done.Add(1)
	go func() {
		defer b.done.Done()
		tick := time.NewTicker(period)
		defer tick.Stop()
		v, last := apLEDAmber, ""
		for {
			if err := set(v); err != nil {
				if err.Error() != last {
					log.Printf("LED blink: %v", err)
					last = err.Error()
				}
			} else {
				last = ""
			}
			v = apLEDAmber + apLEDGreen - v
			select {
			case <-b.stop:
				return
			case <-tick.C:
			}
		}
	}()
	return b
}

// Stop ends the blink; the caller then sets the next state.
func (b *apBlink) Stop() {
	if b == nil {
		return
	}
	close(b.stop)
	b.done.Wait()
}

// airportLED shows a UniFi LED state on the base station's LED (the mapping is
// at the top of this file). A blinking state starts the blink and returns at
// once; any other state stops it first, which is how Locate ends.
func (cl *Client) airportLED(s LEDState) error {
	p := airportLEDPlan(s)
	cl.ledMu.Lock()
	defer cl.ledMu.Unlock()
	set := func(v int) error {
		_, err := cl.run(airportLEDCmd(v))
		return err
	}
	if p.Blink {
		if cl.blink == nil {
			cl.blink = startAPBlink(set, apBlinkEvery)
		}
		return nil
	}
	cl.blink.Stop()
	cl.blink = nil
	if p.Off {
		cl.ledOff.Do(func() {
			log.Printf("note: the LED cannot be turned off on an AirPort base station (it has amber, green and the firmware's own); leaving it on its own setting")
		})
	}
	return set(p.Value)
}
