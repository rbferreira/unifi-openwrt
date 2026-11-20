// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"fmt"
	"strconv"
	"strings"
)

// Wanted is what the controller asks of the AP's Wi-Fi, free of any platform
// (no uci, no hostapd). Plan reads the same keys and turns them straight into
// uci; a platform without uci renders this instead. The two must agree on what
// the controller meant, so the skip rules below copy Plan's.
type Wanted struct {
	Radios []WantedRadio
	WLANs  []WantedWLAN
	Notes  []string // things left out or approximated, for the log; never a passphrase
}

// WantedRadio is one radio.N section.
type WantedRadio struct {
	Name     string // the controller's name: wifi0, wifi1
	Band     string // "2g", "5g" or "6g"
	Disabled bool
	Channel  int    // 0 = auto
	Width    int    // MHz: 20, 40, 80, 160 (320); 0 = ieee_mode not given
	Country  string // ISO alpha-2, "" = not given or unknown
	TxPower  int    // dBm, 0 = auto
}

// WantedWLAN is one wireless.N / aaa.N pair. A WLAN on two radios is two
// entries with the same SSID and ID and different N, Radio and Devname.
type WantedWLAN struct {
	N       string // index in system_cfg (wireless.N / aaa.N)
	ID      string // wireless.N.id
	Devname string // wireless.N.devname, the controller's name for this VAP
	Radio   string // wireless.N.parent

	SSID                             string
	Hidden, Isolate, Disabled, Guest bool
	Adopt                            bool // wireless.N.element_adopt: UniFi's hidden adoption SSID

	Security   string // "none", "psk2", "sae" or "sae-mixed"
	KeyMgmt    string // hostapd wpa_key_mgmt, from keyMgmt(); "" for open
	Passphrase string // aaa.N.wpa.psk; never log it
	PMF        int    // 0 off, 1 optional, 2 required

	MACPolicy string // "disable", "allow" or "deny"
	MACs      []string
	VLAN      int // 0 = untagged
}

// Wifi reads the controller's Wi-Fi settings out of cfg.
//
// phys maps the radio names the AP reports to their band ("wifi0" -> "2g"),
// like Features.Phys: when non-nil it is the truth about the band, and radios
// it does not list are left out (the controller remembers radios that no
// longer exist). With nil the band is Plan's guess from ieee_mode and the
// radio name, which cannot tell 5 GHz from 6 GHz.
func Wifi(cfg Cfg, phys map[string]string) Wanted {
	var w Wanted
	have := map[string]bool{} // radio names that made it into w.Radios

	for _, n := range indices(cfg, "radio") {
		p := "radio." + n + "."
		phy, mode := cfg[p+"phyname"], cfg[p+"ieee_mode"]
		if phy == "" {
			w.Notes = append(w.Notes, fmt.Sprintf("radio.%s: no phyname, ignored", n))
			continue
		}
		b := "5g"
		if strings.Contains(mode, "ng") || phy == "wifi0" && !strings.Contains(mode, "na") {
			b = "2g"
		}
		if phys != nil {
			pb, ok := phys[phy]
			if !ok {
				w.Notes = append(w.Notes, fmt.Sprintf("radio.%s (%s): not a radio this AP reports, ignored", n, phy))
				continue
			}
			b = pb
		}
		r := WantedRadio{
			Name:     phy,
			Band:     b,
			Disabled: cfg[p+"status"] == "disabled",
			Width:    modeWidth(mode),
		}
		switch ch := cfg[p+"channel"]; ch {
		case "", "auto":
		default:
			if v, err := strconv.Atoi(ch); err == nil && v > 0 {
				r.Channel = v
			} else {
				w.Notes = append(w.Notes, fmt.Sprintf("radio %s: unreadable channel %q, left on auto", phy, ch))
			}
		}
		if cc, raw := countryCode(cfg[p+"countrycode"], cfg["radio.countrycode"]); cc != "" {
			r.Country = cc
		} else if raw != "" {
			w.Notes = append(w.Notes, fmt.Sprintf("radio %s: unknown country code %q from the controller", phy, raw))
		}
		switch tp := cfg[p+"txpower"]; tp {
		case "", "auto":
		default:
			if v, err := strconv.Atoi(tp); err == nil && v > 0 {
				r.TxPower = v
			} else {
				w.Notes = append(w.Notes, fmt.Sprintf("radio %s: unreadable txpower %q, left on auto", phy, tp))
			}
		}
		have[phy] = true
		w.Radios = append(w.Radios, r)
	}

	for _, n := range indices(cfg, "wireless") {
		wp, a := "wireless."+n+".", "aaa."+n+"."
		if cfg[wp+"usage"] == "uplink" || cfg[wp+"vwire"] == "enabled" || cfg[wp+"vport"] == "enabled" {
			w.Notes = append(w.Notes, fmt.Sprintf("wireless.%s: mesh/uplink vap, not translated", n))
			continue
		}
		ssid := cfg[wp+"ssid"]
		if ssid == "" {
			ssid = cfg[a+"ssid"]
		}
		parent := cfg[wp+"parent"]
		if !have[parent] {
			w.Notes = append(w.Notes, fmt.Sprintf("wireless.%s (%q on %s): radio not on this AP, ignored", n, ssid, parent))
			continue
		}
		if ssid == "" {
			w.Notes = append(w.Notes, fmt.Sprintf("wireless.%s on %s: no SSID, ignored", n, parent))
			continue
		}
		enc, note := encryption(cfg, a)
		if enc == "" {
			// Never broadcast a WLAN with a different security type than the
			// one configured: leave it out.
			w.Notes = append(w.Notes, fmt.Sprintf("wireless.%s (%q on %s): %s: left off", n, ssid, parent, note))
			continue
		}
		// The "would lose WPA3" note compares against the AP's current uci;
		// there is nothing to lose here.
		if note != "" && !strings.Contains(note, "WPA3") {
			w.Notes = append(w.Notes, fmt.Sprintf("wireless.%s (%q on %s): %s", n, ssid, parent, note))
		}
		l := WantedWLAN{
			N:         n,
			ID:        cfg[wp+"id"],
			Devname:   cfg[wp+"devname"],
			Radio:     parent,
			SSID:      ssid,
			Hidden:    cfg[wp+"hide_ssid"] == "true" || cfg[a+"hide_ssid"] == "true",
			Isolate:   cfg[wp+"l2_isolation"] == "enabled",
			Disabled:  cfg[wp+"status"] == "disabled" || cfg[a+"status"] == "disabled",
			Guest:     cfg[a+"is_guest"] == "true",
			Adopt:     cfg[wp+"element_adopt"] == "enabled",
			Security:  strings.TrimSuffix(enc, "+ccmp"),
			VLAN:      vlanID(cfg, cfg[wp+"devname"]),
			MACPolicy: "disable",
		}
		if enc != "none" {
			l.Passphrase = cfg[a+"wpa.psk"]
			if l.Passphrase == "" {
				w.Notes = append(w.Notes, fmt.Sprintf("wireless.%s (%q on %s): secured but no passphrase in the config: left off", n, ssid, parent))
				continue
			}
			l.PMF, _ = strconv.Atoi(pmf(cfg[a+"pmf.status"], cfg[a+"pmf.mode"]))
			// 802.11r adds FT-* key management that needs a mobility domain
			// this model does not carry, so it is not asked for.
			l.KeyMgmt = keyMgmt(cfg, a, enc, false)
			if cfg[a+"ft.status"] == "enabled" {
				w.Notes = append(w.Notes, fmt.Sprintf("wireless.%s (%q on %s): 802.11r requested, not translated", n, ssid, parent))
			}
		}
		l.MACPolicy, l.MACs = macACL(cfg, l.Devname)
		w.WLANs = append(w.WLANs, l)
	}
	return w
}

// modeWidth is the channel width in MHz named by an ieee_mode (11nght20,
// 11naht40, 11acvht80, 11axhe80, 11beeht160). It reads the width like
// htmode() does (the suffix; 20 when there is none) but needs no current uci
// value, because htmode()'s only use of that is to pick the HT/VHT/HE/EHT
// family, which a width does not depend on. Unlike htmode() it knows 320 MHz,
// which htmode() reads as 20. 0 = no mode given.
func modeWidth(mode string) int {
	if mode == "" {
		return 0
	}
	for _, w := range []int{320, 160, 80, 40} {
		if strings.HasSuffix(mode, strconv.Itoa(w)) {
			return w
		}
	}
	return 20
}

// vlanID is the VLAN of a VAP: vlanNetworks() names the network "uow<vid>".
// 0 = untagged.
func vlanID(cfg Cfg, devname string) int {
	if devname == "" {
		return 0
	}
	nets, _ := vlanNetworks(cfg)
	id, err := strconv.Atoi(strings.TrimPrefix(nets[devname], "uow"))
	if err != nil {
		return 0
	}
	return id
}
