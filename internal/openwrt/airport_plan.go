// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// AirPortPlan is what the daemon would run on an AirPort for a system_cfg.
type AirPortPlan struct {
	// Wanted is the controller's Wi-Fi as asked, before the daemon's own choices
	// (schedules, automatic channels): what a later pass starts from.
	Wanted translate.Wanted
	VAPs   []AirPortVAP
	Notes  []string
	// SchedKey names the on/off answer of every scheduled WLAN (AirPortSchedule).
	SchedKey string
	Uplink5  int    // the mesh uplink's channel on the 5 GHz radio, 0 = none
	UpIf     string // the mesh uplink's interface (the parent of the VLANs), "" = none
}

// PlanAirPort works out the VAPs for cfg on the unit snap describes, at now.
// chans (may be nil) holds the automatic channels chosen so far and src (may be
// nil) the neighbour scan: without them an automatic channel stays automatic.
// It is what syncAirPort applies and what owplan prints.
func PlanAirPort(cfg translate.Cfg, snap *Snapshot, now time.Time, chans *AutoChannels, src ScanSource) AirPortPlan {
	w := translate.Wifi(cfg, snap.RadioNames())
	p := AirPortPlan{Wanted: w, Uplink5: snap.UplinkChannel("na"), UpIf: snap.UplinkIface()}
	asked := w
	asked.WLANs = append([]translate.WantedWLAN(nil), w.WLANs...)
	var notes []string
	asked, notes, p.SchedKey = AirPortSchedule(asked, now)
	p.Notes = append(p.Notes, notes...)
	if chans != nil && src != nil {
		asked, notes = chans.Apply(asked, snap, src, p.Uplink5, now)
		p.Notes = append(p.Notes, notes...)
	}
	vaps, notes := AirPortVAPsNet(asked, p.Uplink5, APNet{UpIf: p.UpIf, Portal: translate.GuestPolicy(cfg).Portal})
	p.VAPs = vaps
	p.Notes = append(p.Notes, notes...)
	p.Notes = append(p.Notes, AirPortCountryNotes(w, snap.Country)...)
	p.Notes = append(p.Notes, AirPortSystemNotes(cfg)...)
	p.Notes = append(append([]string(nil), w.Notes...), p.Notes...)
	return p
}

// NewTarget is the Target of a command-line tool from its flags: the platform
// ("" = OpenWrt, PlatformAirPort) and the file with the AirPort's admin password
// (empty = log in with the key). The password file is read here.
func NewTarget(addr, user, key, knownHosts, platform, pwFile string) (Target, error) {
	t := Target{Addr: addr, User: user, KeyFile: key, KnownHosts: knownHosts, Platform: platform}
	if platform != "" && platform != PlatformAirPort {
		return Target{}, fmt.Errorf("unknown platform %q (empty or %q)", platform, PlatformAirPort)
	}
	if pwFile != "" {
		b, err := os.ReadFile(pwFile)
		if err != nil {
			return Target{}, fmt.Errorf("password file: %w", err)
		}
		t.Password = strings.TrimRight(string(b), "\r\n")
		if t.Password == "" {
			return Target{}, errors.New("password file is empty")
		}
	}
	return t, nil
}

// CollectRaw is the unparsed reading of the AP, of whatever platform it is.
func (cl *Client) CollectRaw() ([]byte, error) {
	if cl.IsAirPort() {
		return cl.run(airportScript)
	}
	return cl.Run(collectScript)
}
