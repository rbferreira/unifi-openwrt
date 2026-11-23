// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package ftrelay

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func mac(t *testing.T, s string) MAC {
	t.Helper()
	m, err := ParseMAC(s)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func testRelay(t *testing.T) (*Relay, *[]string) {
	var logs []string
	r := &Relay{peers: map[MAC]MAC{}, seen: map[MAC]arrival{}, Verbose: true}
	r.Logf = func(f string, a ...any) { logs = append(logs, f) }
	key := unhex(t, "00112233445566778899aabbccddeeff")
	r.bss = []BSS{
		{Iface: "phy0-ap0", BSSID: mac(t, "02:aa:aa:aa:aa:01"), Bridge: "br-lan", WLAN: [2]byte{0xa1, 0xb2}, Key: key},
		{Iface: "phy1-ap0", BSSID: mac(t, "02:aa:aa:aa:aa:02"), Bridge: "br-lan", WLAN: [2]byte{0xa1, 0xb2}, Key: key},
		{Iface: "phy0-ap1", BSSID: mac(t, "02:aa:aa:aa:aa:03"), Bridge: "br-iot", WLAN: [2]byte{0xc3, 0xd4}, Key: key},
	}
	return r, &logs
}

func TestHandleRRB(t *testing.T) {
	r, _ := testRelay(t)
	brMAC := mac(t, "02:bb:bb:bb:bb:00")
	unifiEth := mac(t, "02:11:11:11:11:00")
	unifiBSS := mac(t, "02:11:11:11:11:05")
	u := unhex(t, unifiSeqReq)
	now := time.Now()

	// in: a UniFi AP's broadcast goes to each BSS on that bridge, one copy
	// each, addressed to the BSSID, from the UniFi BSSID, in upstream's layout
	outs := r.handle("br-lan", brMAC, Frame(Broadcast, unifiEth, EtherRRB, u), false, now)
	if len(outs) != 2 {
		t.Fatalf("broadcast in: %d frames", len(outs))
	}
	for _, o := range outs {
		var dst, src MAC
		copy(dst[:], o.frame)
		copy(src[:], o.frame[6:])
		if !o.inject || src != unifiBSS || (dst != r.bss[0].BSSID && dst != r.bss[1].BSSID) || !IsUpstream(o.frame[14:]) {
			t.Errorf("in: inject=%v %s -> %s", o.inject, src, dst)
		}
	}
	if r.peers[unifiBSS] != unifiEth {
		t.Error("the UniFi AP's Ethernet address was not learned")
	}

	// out: hostapd answers the UniFi BSS: translated, from the bridge's
	// address, to the AP's Ethernet address
	up := outs[0].frame[14:]
	outs = r.handle("br-lan", brMAC, Frame(unifiBSS, r.bss[0].BSSID, EtherRRB, up), true, now)
	if len(outs) != 1 || outs[0].inject {
		t.Fatalf("out: %+v", outs)
	}
	f := outs[0].frame
	if !bytes.Equal(f[:6], unifiEth[:]) || !bytes.Equal(f[6:12], brMAC[:]) {
		t.Errorf("out addresses: % x", f[:12])
	}
	_, isrc, idst, ok := FromUniFi(f[14:])
	if !ok || isrc != r.bss[0].BSSID || idst != unifiBSS {
		t.Errorf("out: %v %s -> %s", ok, isrc, idst)
	}

	// out: a broadcast, or a BSS not heard yet, goes to everyone
	other := mac(t, "02:22:22:22:22:05")
	for _, dst := range []MAC{Broadcast, other} {
		outs = r.handle("br-lan", brMAC, Frame(dst, r.bss[1].BSSID, EtherRRB, up), true, now)
		if len(outs) != 1 || !bytes.Equal(outs[0].frame[:6], Broadcast[:]) {
			t.Errorf("out to %s: %+v", dst, outs)
		}
	}

	// nothing happens for: the relay's own frames leaving, hostapd's frames
	// to another BSS on this AP, frames of a BSS on another bridge, upstream
	// frames coming in (hostapd hears those itself), the injected frame
	// coming up the bridge, and a unicast for somebody else
	for name, c := range map[string]struct {
		f   []byte
		out bool
	}{
		"own translated frame":  {Frame(unifiEth, brMAC, EtherRRB, f[14:]), true},
		"between own BSSes":     {Frame(r.bss[1].BSSID, r.bss[0].BSSID, EtherRRB, up), true},
		"BSS of another bridge": {Frame(Broadcast, r.bss[2].BSSID, EtherRRB, up), true},
		"upstream frame in":     {Frame(Broadcast, other, EtherRRB, up), false},
		"injected frame":        {Frame(r.bss[0].BSSID, unifiBSS, EtherRRB, up), false},
		"UniFi unicast for another AP": {Frame(other, unifiEth, EtherRRB,
			append(append(append([]byte{}, u[:12]...), other[:]...), u[18:]...)), false},
	} {
		if outs := r.handle("br-lan", brMAC, c.f, c.out, now); len(outs) != 0 {
			t.Errorf("%s: %d frames", name, len(outs))
		}
	}
}

func TestHandleRoam(t *testing.T) {
	r, logs := testRelay(t)
	brMAC := mac(t, "02:bb:bb:bb:bb:00")
	unifiEth := mac(t, "02:11:11:11:11:00")
	unifiBSS := mac(t, "02:11:11:11:11:05")
	client := mac(t, "02:33:33:33:33:01")
	now := time.Unix(1700000100, 0)
	kicked := make(chan string, 4)
	r.Kick = func(iface string, c MAC) { kicked <- iface + " " + c.String() }
	announce := func(wlan [2]byte, key []byte, at time.Time) []byte {
		p, err := Roam{BSSID: unifiBSS, WLAN: wlan, Client: client, Time: at}.Bytes(key)
		if err != nil {
			t.Fatal(err)
		}
		return Frame(RoamGroup, unifiEth, EtherRoam, p)
	}
	wait := func() string {
		select {
		case s := <-kicked:
			return s
		case <-time.After(200 * time.Millisecond):
			return ""
		}
	}
	key, wlan := r.bss[0].Key, r.bss[0].WLAN

	// a client that is not here: nothing
	r.handle("br-lan", brMAC, announce(wlan, key, now), false, now)
	if s := wait(); s != "" {
		t.Errorf("unknown client kicked: %s", s)
	}
	// joined here a minute ago, announced by the UniFi AP now: dropped
	r.seen[client] = arrival{"phy1-ap0", now.Add(-time.Minute)}
	r.handle("br-lan", brMAC, announce(wlan, key, now), false, now)
	if s := wait(); s != "phy1-ap0 "+client.String() {
		t.Errorf("kick: %q", s)
	}
	// came back here after the announcement: kept
	r.seen[client] = arrival{"phy0-ap0", now.Add(2 * time.Second)}
	r.handle("br-lan", brMAC, announce(wlan, key, now), false, now.Add(3*time.Second))
	if s := wait(); s != "" {
		t.Errorf("client that came back was kicked: %s", s)
	}
	// stale announcement, another WLAN, wrong key: kept
	r.seen[client] = arrival{"phy0-ap0", now.Add(-time.Hour)}
	r.handle("br-lan", brMAC, announce(wlan, key, now.Add(-2*time.Minute)), false, now)
	r.handle("br-lan", brMAC, announce([2]byte{9, 9}, key, now), false, now)
	r.handle("br-lan", brMAC, announce(wlan, unhex(t, "ff112233445566778899aabbccddeeff"), now), false, now)
	if s := wait(); s != "" {
		t.Errorf("kicked on a bad announcement: %s", s)
	}
	joined := strings.Join(*logs, "\n")
	if !strings.Contains(joined, "clock") || !strings.Contains(joined, "does not decrypt") {
		t.Errorf("no log about the clock or the key:\n%s", joined)
	}
}

func TestParseHostapd(t *testing.T) {
	conf := `interface=phy0-ap0
bssid=02:aa:aa:aa:aa:01
ssid2="home"
bridge=br-lan
r0kh=ff:ff:ff:ff:ff:ff * 00112233445566778899aabbccddeeff
r1kh=00:00:00:00:00:00 00:00:00:00:00:00 00112233445566778899aabbccddeeff
mobility_domain=a1b2
ft_iface=br-lan

bss=phy0-ap1
bssid=02:aa:aa:aa:aa:02
bridge=br-lan
wpa_key_mgmt=WPA-PSK
interface=phy1-ap0
bssid=02:aa:aa:aa:aa:03
bridge=br-iot
r0kh=02:11:11:11:11:05 021111111105 00112233445566778899aabbccddeeff
mobility_domain=c3d4
`
	got := ParseHostapd(conf)
	if len(got) != 1 {
		t.Fatalf("%d BSSes: %+v", len(got), got)
	}
	b := got[0]
	if b.Iface != "phy0-ap0" || b.Bridge != "br-lan" || b.BSSID.String() != "02:aa:aa:aa:aa:01" || b.WLAN != [2]byte{0xa1, 0xb2} || len(b.Key) != 16 {
		t.Errorf("%+v", b)
	}
}

func TestParseEvent(t *testing.T) {
	iface, c, joined, ok := ParseEvent("Sat Nov 21 01:05:31 2026 daemon.notice hostapd: phy1-ap0: AP-STA-CONNECTED 02:33:33:33:33:01 auth_alg=ft")
	if !ok || !joined || iface != "phy1-ap0" || c.String() != "02:33:33:33:33:01" {
		t.Errorf("connected: %v %v %s %s", ok, joined, iface, c)
	}
	iface, _, joined, ok = ParseEvent("daemon.notice hostapd: phy0-ap0: AP-STA-DISCONNECTED 02:33:33:33:33:01")
	if !ok || joined || iface != "phy0-ap0" {
		t.Errorf("disconnected: %v %v %s", ok, joined, iface)
	}
	for _, l := range []string{"", "hostapd: phy0-ap0: STA 02:33:33:33:33:01 IEEE 802.11: authenticated", "x: AP-STA-CONNECTED nonsense"} {
		if _, _, _, ok := ParseEvent(l); ok {
			t.Errorf("accepted %q", l)
		}
	}
}
