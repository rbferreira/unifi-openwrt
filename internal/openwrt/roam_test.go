// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestRoamAssist(t *testing.T) {
	const weak, noWNM = "aa:00:00:00:00:01", "aa:00:00:00:00:03"
	// Roaming Assistant on "home" on the 5 GHz radio only (wifi1)
	snap := func(sig int) *Snapshot {
		return &Snapshot{Radios: []Radio{
			{Band: "ng", VAPs: []Radio{{Iface: "phy0-ap0", Mode: "Master", SSID: "home", BSSID: "08:55:66:77:00:03",
				Stations: []Station{{MAC: "aa:00:00:00:00:02", Signal: -85}}}}},
			{Band: "na", VAPs: []Radio{
				{Iface: "phy1-ap0", Mode: "Master", SSID: "home", BSSID: "08:55:66:77:00:05",
					Stations: []Station{{MAC: weak, Signal: sig}, {MAC: noWNM, Signal: -80}}},
				{Iface: "phy1-ap1", Mode: "Master", SSID: "iot", BSSID: "0a:55:66:77:00:05",
					Stations: []Station{{MAC: "aa:00:00:00:00:04", Signal: -90}}},
			}},
		}}
	}
	thr := map[string]int{"home|wifi1": -75}
	own := [][]string{
		{"08:55:66:77:00:03", "home", "085566770003af080000510607"}, // this AP's 2.4 GHz BSS
		{"08:55:66:77:00:05", "home", "085566770005af1800007d2409"}, // itself
	}
	nbrs := append([][]string{
		{"5A:11:22:33:44:04", "home", "5a1122334404af1800007d9509"}, // sibling AP
		{"5a:11:22:33:44:05", "other", "5a1122334405af1800007d9509"},
	}, own...)
	reads := 0
	info := func(iface string) (BSSInfo, error) {
		reads++
		if iface != "phy1-ap0" {
			t.Errorf("read %s: no Roaming Assistant there", iface)
		}
		return BSSInfo{WNM: map[string]bool{weak: true}, Neighbors: nbrs}, nil
	}
	ra := &RoamAssist{}
	t0 := time.Unix(1000, 0)
	step := func(sec int, s *Snapshot) ([]BTMRequest, []BTMRequest) {
		return ra.Step(thr, s, t0.Add(time.Duration(sec)*time.Second), info)
	}
	expect := func(sec int, s *Snapshot, nSend int) {
		t.Helper()
		if send, _ := step(sec, s); len(send) != nSend {
			t.Errorf("t=%d s: %d requests, want %d: %+v", sec, len(send), nSend, send)
		}
	}

	// below the threshold for less than 30 s: nothing, hostapd not asked
	expect(0, snap(-80), 0)
	expect(29, snap(-80), 0)
	if reads != 0 {
		t.Errorf("hostapd read %d times before a client was due", reads)
	}
	// 30 s: one request, only other APs of the SSID as candidates (not
	// this AP's own BSSes on any band); the client without BSS transition
	// is left alone (reported once)
	send, none := step(30, snap(-80))
	want := []BTMRequest{{Iface: "phy1-ap0", SSID: "home", MAC: weak, Signal: -80, Threshold: -75,
		Neighbors: []string{"5a1122334404af1800007d9509"}}}
	if !reflect.DeepEqual(send, want) {
		t.Errorf("t=30 s: requests %+v, want %+v", send, want)
	}
	if len(none) != 1 || none[0].MAC != noWNM {
		t.Errorf("t=30 s: without BSS transition %+v, want %s", none, noWNM)
	}
	if reads != 1 {
		t.Errorf("hostapd read %d times for one interface", reads)
	}
	// still below: no more than one request a minute; noWNM not again
	if send, none := step(31, snap(-80)); len(send) != 0 || len(none) != 0 {
		t.Errorf("t=31 s: %+v %+v", send, none)
	}
	expect(89, snap(-80), 0)
	// only this AP's own BSSes known: a request without candidates
	nbrs = own
	if send, _ := step(90, snap(-80)); len(send) != 1 || send[0].Neighbors != nil {
		t.Errorf("t=90 s with only own BSSes listed: %+v, want one request without candidates", send)
	}
	// recovered: forgotten, 30 s below again before the next request
	expect(91, snap(-70), 0)
	expect(92, snap(-80), 0)
	expect(121, snap(-80), 0)
	expect(122, snap(-80), 1)
	// left: forgotten as well
	gone := snap(-80)
	gone.Radios[1].VAPs[0].Stations = gone.Radios[1].VAPs[0].Stations[1:]
	expect(123, gone, 0)
	expect(124, snap(-80), 0)
	expect(153, snap(-80), 0)
	expect(154, snap(-80), 1)
	// Roaming Assistant switched off
	thr = nil
	expect(300, snap(-80), 0)
}

func TestParseBSSInfo(t *testing.T) {
	clients := []byte(`{"freq":5180,"clients":{
		"AA:00:00:00:00:01":{"auth":true,"assoc":true,"rrm":[115,0,0,0,0],"extended_capabilities":[4,0,8,0,0,0,0,64]},
		"aa:00:00:00:00:02":{"auth":true,"assoc":true,"extended_capabilities":[4,0,0,0,0,0,0,64]},
		"aa:00:00:00:00:03":{"auth":true,"assoc":true,"extended_capabilities":[]}}}`)
	nr := []byte(`{"list":[["5a:11:22:33:44:04","home","5a1122334404af1800007d9509"]]}`)
	bi, err := parseBSSInfo(clients, nr)
	if err != nil {
		t.Fatal(err)
	}
	// extended capabilities bit 19 (byte 2, bit 3): BSS transition
	if want := map[string]bool{"aa:00:00:00:00:01": true, "aa:00:00:00:00:02": false, "aa:00:00:00:00:03": false}; !reflect.DeepEqual(bi.WNM, want) {
		t.Errorf("WNM = %v, want %v", bi.WNM, want)
	}
	if len(bi.Neighbors) != 1 || bi.Neighbors[0][1] != "home" {
		t.Errorf("neighbours = %v", bi.Neighbors)
	}
	// 802.11k off: rrm_nr_list fails, no candidates
	if bi, err := parseBSSInfo(clients, nil); err != nil || bi.Neighbors != nil {
		t.Errorf("without a neighbour list: %+v, %v", bi, err)
	}
	// hostapd not answering: an error, not "no client supports it"
	if _, err := parseBSSInfo(nil, nil); err == nil {
		t.Error("no get_clients output accepted")
	}
}

func TestBTMRequestJSON(t *testing.T) {
	read := func(r BTMRequest) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(btmRequestJSON(r)), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := read(BTMRequest{MAC: "aa:00:00:00:00:01", Neighbors: []string{"5a1122334404af1800007d9509"}})
	want := map[string]any{"addr": "aa:00:00:00:00:01", "disassociation_imminent": true, "abridged": true,
		"neighbors": []any{"5a1122334404af1800007d9509"}, "validity_period": 30.0,
		"mbo_reason": 5.0, "cell_pref": 1.0, "reassoc_delay": 0.0}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("got %v, want %v", m, want)
	}
	// no candidates: not abridged (a client takes every BSS outside the
	// list as excluded), so it scans and picks the best AP itself
	m = read(BTMRequest{MAC: "aa:00:00:00:00:01"})
	if m["abridged"] != false || m["disassociation_imminent"] != true {
		t.Errorf("without candidates: %v, want abridged false, disassociation imminent", m)
	}
	if _, ok := m["neighbors"]; ok {
		t.Errorf("empty candidate list sent: %v", m)
	}
	if _, ok := m["disassociation_timer"]; ok {
		t.Errorf("disassociation timer sent: %v", m)
	}
}
