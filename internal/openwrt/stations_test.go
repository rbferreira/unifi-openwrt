// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// iwinfo assoclist rates as OpenWrt 25.12 prints them (mcs only for HT and
// later, nss only for VHT and later).
const (
	rateLegacy = `"ht":false,"vht":false,"he":false,"eht":false,"mhz":20,"rate":1000`
	rateHT7    = `"ht":true,"vht":false,"he":false,"eht":false,"mhz":20,"rate":72200,"mcs":7,"40mhz":false,"short_gi":true`
	rateHT15   = `"ht":true,"vht":false,"he":false,"eht":false,"mhz":20,"rate":144400,"mcs":15,"40mhz":false,"short_gi":true`
	rateVHT2   = `"ht":false,"vht":true,"he":false,"eht":false,"mhz":80,"rate":866700,"mcs":9,"nss":2,"short_gi":true`
)

// staEntry is one assoclist result for the client MAC.
func staEntry(rx, tx string, txPk, txRetries, txFailed int) string {
	return fmt.Sprintf(`{"mac":"MAC","signal":-50,"noise":-95,"authorized":true,"connected_time":60,
 "rx":{"packets":500,"bytes":50000,%s},"tx":{"packets":%d,"bytes":90000,"retries":%d,"failed":%d,%s}}`, rx, txPk, txRetries, txFailed, tx)
}

// hostapdHT: get_clients with the client associated as an 802.11n station.
const hostapdHT = `{"freq":2437,"clients":{"MAC":{"auth":true,"assoc":true,"authorized":true,"preauth":false,"wds":false,"wmm":true,"ht":true,"vht":false,"he":false,"wps":false,"mfp":true,"aid":1}}}`

// staRow reads one 2.4 GHz AP (17 dBm) with the given assoclist entry and
// hostapd get_clients output and returns the client's sta_table row.
func staRow(t *testing.T, mac, entry, hostapd string, prev *Snapshot) (map[string]any, *Snapshot) {
	t.Helper()
	out := `=== board
{"hostname":"ap","model":"m","release":{"description":"OpenWrt"}}
=== iface phy0-ap0 phy0
{"ssid":"x","bssid":"02:00:00:00:00:01","mode":"Master","channel":6,"frequency":2437,"txpower":17}
=== assoc phy0-ap0
{"results":[` + entry + `]}
=== hostapd phy0-ap0
` + hostapd + "\n"
	s, err := Parse([]byte(strings.ReplaceAll(out, "MAC", mac)), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	vaps := s.Fields(prev, CfgInfo{}, nil)["vap_table"].([]map[string]any)
	if len(vaps) != 1 {
		t.Fatalf("vap_table = %v", vaps)
	}
	rows := vaps[0]["sta_table"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("sta_table = %v", rows)
	}
	return rows[0], s
}

// The per-client fields a UAP reports: the stream count and MCS the UniFi
// UI shows as "Wi-Fi 4" and "1x1", tx power in half dB, and the tx
// counters with their movement since the last inform, from which the
// controller computes the retry percentage. No ccq (a UAP's is not a retry
// ratio).
func TestStaTableFields(t *testing.T) {
	absent := struct{}{}
	for i, c := range []struct {
		name    string
		prev    string // assoclist entry of the reading before (none if "")
		cur     string
		hostapd string
		want    map[string]any // absent = no such key
	}{
		{name: "HT client on a legacy rate keeps 802.11n from hostapd",
			cur: staEntry(rateLegacy, rateLegacy, 100, 0, 0), hostapd: hostapdHT,
			want: map[string]any{"is_11n": true, "state_ht": true, "is_11ac": false, "is_11ax": false, "tx_mcs": -1, "nss": absent}},
		{name: "no hostapd entry: generation from the rates",
			cur:  staEntry(rateLegacy, rateHT7, 100, 0, 0),
			want: map[string]any{"is_11n": true, "is_11ac": false}},
		{name: "a hostapd without the vht flag does not take 802.11ac away",
			cur: staEntry(rateHT7, rateVHT2, 100, 0, 0), hostapd: hostapdHT,
			want: map[string]any{"is_11n": true, "is_11ac": true}},
		{name: "HT MCS 7 is one stream",
			cur: staEntry(rateHT7, rateHT7, 100, 0, 0), hostapd: hostapdHT,
			want: map[string]any{"nss": 1, "tx_mcs": 7}},
		{name: "HT MCS 15 is two streams",
			cur: staEntry(rateHT7, rateHT15, 100, 0, 0), hostapd: hostapdHT,
			want: map[string]any{"nss": 2, "tx_mcs": 15}},
		{name: "VHT rate with nss 2",
			cur:  staEntry(rateHT7, rateVHT2, 100, 0, 0),
			want: map[string]any{"nss": 2, "tx_mcs": 9, "is_11ac": true}},
		{name: "nss survives a legacy-only reading",
			prev: staEntry(rateHT15, rateHT15, 100, 0, 0), cur: staEntry(rateLegacy, rateLegacy, 200, 0, 0), hostapd: hostapdHT,
			want: map[string]any{"nss": 2, "tx_mcs": -1}},
		{name: "first reading: counters, no movement",
			cur: staEntry(rateHT7, rateHT7, 750, 250, 30), hostapd: hostapdHT,
			want: map[string]any{"wifi_tx_attempts": int64(1000), "wifi_tx_attempts_mov": int64(0), "tx_retries_mov": int64(0),
				"wifi_tx_dropped": int64(30), "wifi_tx_dropped_mov": int64(0)}},
		{name: "movement since the last reading",
			prev: staEntry(rateHT7, rateHT7, 1000, 100, 10), cur: staEntry(rateHT7, rateHT7, 1900, 200, 15), hostapd: hostapdHT,
			want: map[string]any{"wifi_tx_attempts": int64(2100), "wifi_tx_attempts_mov": int64(1000), "tx_retries_mov": int64(100),
				"wifi_tx_dropped": int64(15), "wifi_tx_dropped_mov": int64(5)}},
		{name: "counters restarted by a re-association: no movement",
			prev: staEntry(rateHT7, rateHT7, 5000, 400, 50), cur: staEntry(rateHT7, rateHT7, 100, 10, 1), hostapd: hostapdHT,
			want: map[string]any{"wifi_tx_attempts": int64(110), "wifi_tx_attempts_mov": int64(0), "tx_retries_mov": int64(0),
				"wifi_tx_dropped": int64(1), "wifi_tx_dropped_mov": int64(0)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			mac := fmt.Sprintf("02:00:00:00:30:%02x", i) // nss is remembered per client
			var prev *Snapshot
			if c.prev != "" {
				_, prev = staRow(t, mac, c.prev, c.hostapd, nil)
			}
			row, _ := staRow(t, mac, c.cur, c.hostapd, prev)
			if row["tx_power"] != 34 || row["tx_retry_burst_count"] != 0 {
				t.Errorf("tx_power = %v (want 34: 17 dBm in half dB), tx_retry_burst_count = %v", row["tx_power"], row["tx_retry_burst_count"])
			}
			if v, ok := row["ccq"]; ok {
				t.Errorf("ccq = %v, want none", v)
			}
			for k, w := range c.want {
				got, ok := row[k]
				if w == absent {
					if ok {
						t.Errorf("%s = %v, want no %s", k, got, k)
					}
				} else if !ok || got != w {
					t.Errorf("%s = %v (%T), want %v (%T)", k, got, got, w, w)
				}
			}
		})
	}
}
