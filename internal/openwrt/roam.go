// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Roaming Assistant (docs/internals.md): the UniFi WLAN setting that nudges
// weak clients to a better AP (wireless.N.btm_disassoc.*, read by
// translate.RoamAssist). A client below the threshold on every reading for
// RoamAfter gets an 802.11v BSS transition request, disassociation
// imminent, with the other APs of the SSID from the interface's 802.11k
// neighbour list as candidates (never this AP's own BSSes, which that list
// holds for the other band: the point is another AP); again at most every
// RoamEvery while it stays below. Only a request: the client may stay
// (minimum RSSI is the hard limit), and clients that did not announce BSS
// transition are left alone.
const (
	RoamAfter = 30 * time.Second
	RoamEvery = time.Minute
)

// BSSInfo is what hostapd tells about one AP interface (Client.BSSInfo).
type BSSInfo struct {
	WNM       map[string]bool // client MAC -> announced BSS transition
	Neighbors [][]string      // rrm_nr_list: [bssid, ssid, neighbour report element (hex)]
}

// BTMRequest is one BSS transition request (Client.BSSTransition).
type BTMRequest struct {
	Iface, SSID, MAC  string
	Signal, Threshold int      // dBm
	Neighbors         []string // candidates: neighbour report elements (hex)
}

// RoamAssist is the per-client state between readings.
type RoamAssist struct {
	below map[string]time.Time // first reading below the threshold
	sent  map[string]time.Time // last request
	noWNM map[string]bool      // no BSS transition (reported once)
}

// Step takes one reading. thr: "<ssid>|<radio name>" -> dBm
// (translate.RoamAssist). info reads an interface from hostapd; it is only
// called for an interface with a client due a request, once per Step.
// Returns the requests to send, and the clients newly found without BSS
// transition support.
func (ra *RoamAssist) Step(thr map[string]int, s *Snapshot, now time.Time, info func(iface string) (BSSInfo, error)) (send, noWNM []BTMRequest) {
	if ra.below == nil {
		ra.below, ra.sent, ra.noWNM = map[string]time.Time{}, map[string]time.Time{}, map[string]bool{}
	}
	here, low := map[string]bool{}, map[string]bool{}
	own := map[string]bool{} // this AP's BSSes, every radio
	for _, r := range s.Radios {
		for _, v := range r.VAPs {
			own[strings.ToLower(v.BSSID)] = true
		}
	}
	read := map[string]*BSSInfo{} // nil: hostapd did not answer
	for i, r := range s.Radios {
		for _, v := range r.VAPs {
			t, ok := thr[v.SSID+"|"+fmt.Sprintf("wifi%d", i)]
			if !ok || v.Mode != "Master" {
				continue
			}
			for _, st := range v.Stations {
				here[st.MAC] = true
				if st.Signal == 0 || st.Signal >= t {
					continue
				}
				low[st.MAC] = true
				if ra.below[st.MAC].IsZero() {
					ra.below[st.MAC] = now
				}
				last := ra.sent[st.MAC]
				if ra.noWNM[st.MAC] || now.Sub(ra.below[st.MAC]) < RoamAfter || !last.IsZero() && now.Sub(last) < RoamEvery {
					continue
				}
				bi, done := read[v.Iface]
				if !done {
					if b, err := info(v.Iface); err == nil {
						bi = &b
					}
					read[v.Iface] = bi
				}
				if bi == nil {
					continue // next reading
				}
				req := BTMRequest{Iface: v.Iface, SSID: v.SSID, MAC: st.MAC, Signal: st.Signal, Threshold: t}
				if !bi.WNM[st.MAC] {
					ra.noWNM[st.MAC] = true
					noWNM = append(noWNM, req)
					continue
				}
				for _, n := range bi.Neighbors {
					if len(n) == 3 && n[1] == v.SSID && !own[strings.ToLower(n[0])] {
						req.Neighbors = append(req.Neighbors, n[2])
					}
				}
				ra.sent[st.MAC] = now
				send = append(send, req)
			}
		}
	}
	// forget clients that recovered or left
	for m := range ra.below {
		if !low[m] {
			delete(ra.below, m)
			delete(ra.sent, m)
		}
	}
	for m := range ra.noWNM {
		if !here[m] {
			delete(ra.noWNM, m)
		}
	}
	return send, noWNM
}

// BSSInfo reads an AP interface's clients (get_clients) and its 802.11k
// neighbour list from hostapd.
func (cl *Client) BSSInfo(iface string) (BSSInfo, error) {
	if !ifaceSafe(iface) {
		return BSSInfo{}, fmt.Errorf("bad interface %q", iface)
	}
	out, err := cl.Run("ubus call hostapd." + iface + " get_clients; echo @@; ubus call hostapd." + iface + " rrm_nr_list 2>/dev/null; true")
	if err != nil {
		return BSSInfo{}, fmt.Errorf("hostapd %s: %v", iface, err)
	}
	cs, nr, _ := strings.Cut(string(out), "@@")
	bi, err := parseBSSInfo([]byte(cs), []byte(nr))
	if err != nil {
		return bi, fmt.Errorf("hostapd %s get_clients: %v", iface, err)
	}
	return bi, nil
}

// parseBSSInfo: a client announced BSS transition with bit 19 of its
// extended capabilities (byte 2, bit 3). No neighbour list (802.11k off):
// no candidates.
func parseBSSInfo(clients, nr []byte) (BSSInfo, error) {
	var c struct {
		Clients map[string]struct {
			ExtCapab []int `json:"extended_capabilities"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(clients, &c); err != nil {
		return BSSInfo{}, err
	}
	bi := BSSInfo{WNM: map[string]bool{}}
	for m, x := range c.Clients {
		bi.WNM[strings.ToLower(m)] = len(x.ExtCapab) > 2 && x.ExtCapab[2]&0x08 != 0
	}
	var l struct{ List [][]string }
	if json.Unmarshal(nr, &l) == nil {
		bi.Neighbors = l.List
	}
	return bi, nil
}

// BSSTransition sends a BSS transition request (ubus hostapd.<if>
// bss_transition_request).
func (cl *Client) BSSTransition(r BTMRequest) error {
	if !ifaceSafe(r.Iface) || !macOK.MatchString(r.MAC) {
		return fmt.Errorf("bss transition: bad interface %q or MAC %q", r.Iface, r.MAC)
	}
	if out, err := cl.Run("ubus call hostapd." + r.Iface + " bss_transition_request " + shQuote(btmRequestJSON(r))); err != nil {
		return fmt.Errorf("bss_transition_request %s %s: %v (%s)", r.Iface, r.MAC, err, firstLine(string(out)))
	}
	return nil
}

// btmRequestJSON: disassociation imminent, valid for 30 beacon intervals.
// No disassociation_timer: with one, hostapd disassociates the client when
// it runs out (and drops its PMKSA cache entry), which is a kick. Abridged
// only with candidates: a client takes every BSS outside the list as
// excluded; without any it scans and picks the best AP itself. The MBO
// fields (reason 5: low RSSI; cellular data: rather not) are always given:
// OpenWrt 25.12's hostapd reads them uninitialised when they are left out.
func btmRequestJSON(r BTMRequest) string {
	req := map[string]any{"addr": r.MAC, "disassociation_imminent": true, "validity_period": 30,
		"abridged": len(r.Neighbors) > 0, "mbo_reason": 5, "cell_pref": 1, "reassoc_delay": 0}
	if len(r.Neighbors) > 0 {
		req["neighbors"] = r.Neighbors
	}
	js, _ := json.Marshal(req)
	return string(js)
}
