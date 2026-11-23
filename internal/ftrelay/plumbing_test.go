// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package ftrelay

import (
	"bytes"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

// TestPlumbing runs the relay on real interfaces: a bridge with a "LAN"
// port and a port that has a BSSID as its address, as hostapd's AP interface
// does. It needs root and changes the network of the namespace it runs in,
// so it only runs when asked, in a throwaway namespace:
//
//	go test -c -o /tmp/ftrelay.test ./internal/ftrelay
//	sudo env UOW_FT_NETTEST=1 unshare -n -m sh -c \
//	    'mount -t sysfs sys /sys && exec /tmp/ftrelay.test -test.run Plumbing -test.v'
func TestPlumbing(t *testing.T) {
	if os.Getenv("UOW_FT_NETTEST") != "1" || os.Geteuid() != 0 {
		t.Skip("needs root in a throwaway network namespace (see the comment)")
	}
	if err := Probe(); err != nil {
		t.Fatal(err)
	}
	bssid := mac(t, "02:aa:aa:aa:aa:01")
	unifiEth := mac(t, "02:11:11:11:11:00")
	unifiBSS := mac(t, "02:11:11:11:11:05")
	client := mac(t, "02:33:33:33:33:01")
	key := unhex(t, "00112233445566778899aabbccddeeff")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	link := func(kind, name string, attrs ...[]byte) {
		t.Helper()
		body := append(ifinfo(0, 0, 0), nlAttr(iflaIfname, append([]byte(name), 0))...)
		for _, a := range attrs {
			body = append(body, a...)
		}
		body = append(body, nlAttr(iflaLinkinfo|nlaNested, nlAttr(infoKind, []byte(kind)))...)
		must(nlRequest(syscall.RTM_NEWLINK, syscall.NLM_F_CREATE|syscall.NLM_F_EXCL, body))
	}
	idx := func(name string) int {
		t.Helper()
		i, err := net.InterfaceByName(name)
		must(err)
		return i.Index
	}
	// br0 with: lan0 (its other end lan1 is "the LAN" where the UniFi AP
	// sits) and ap0, whose address is the BSSID (other end unused)
	link("bridge", "br0")
	must(addVeth("lan0", "lan1"))
	must(addVeth("ap0", "ap1"))
	must(nlRequest(syscall.RTM_NEWLINK, 0, append(ifinfo(idx("ap0"), 0, 0), nlAttr(1, bssid[:])...))) // IFLA_ADDRESS
	must(setLink(idx("br0"), 0))
	for _, p := range []string{"lan0", "ap0"} {
		must(setLink(idx(p), idx("br0")))
	}
	for _, p := range []string{"lan1", "ap1"} {
		must(setLink(idx(p), 0))
	}
	open := func(name string) int {
		t.Helper()
		fd, err := packetSocket(idx(name), EtherRRB, false)
		must(err)
		return fd
	}
	hostapd := open("br0") // like hostapd's socket: this EtherType only, on the bridge
	lan := open("lan1")
	roamFD, err := packetSocket(idx("lan1"), EtherRoam, false)
	must(err)
	send := func(fd int, ifname string, f []byte) {
		t.Helper()
		must(syscall.Sendto(fd, f, 0, &syscall.SockaddrLinklayer{Ifindex: idx(ifname), Halen: 6}))
	}
	// recv returns the first frame for which want is true, within 2 s
	recv := func(fd int, want func(f []byte) bool) []byte {
		buf := make([]byte, 2048)
		for end := time.Now().Add(2 * time.Second); time.Now().Before(end); {
			n, _, err := syscall.Recvfrom(fd, buf, 0)
			if err == nil && n >= 14 && want(buf[:n]) {
				return append([]byte(nil), buf[:n]...)
			}
		}
		return nil
	}

	r := &Relay{Logf: t.Logf, Verbose: true}
	must(r.Sync([]BSS{{Iface: "ap0", BSSID: bssid, Bridge: "br0", WLAN: [2]byte{0xa1, 0xb2}, Key: key}}))
	defer r.Close()
	if masterOf("uowft0") != "br0" {
		t.Fatalf("the relay's port is not in the bridge: %q", masterOf("uowft0"))
	}
	time.Sleep(300 * time.Millisecond)

	// 1. the UniFi AP broadcasts a sequence request: hostapd's socket gets
	// it in upstream's layout, from the UniFi BSSID, addressed to the BSSID
	u := unhex(t, unifiSeqReq)
	send(lan, "lan1", Frame(Broadcast, unifiEth, EtherRRB, u))
	got := recv(hostapd, func(f []byte) bool { return bytes.Equal(f[6:12], unifiBSS[:]) })
	if got == nil {
		t.Fatal("in: hostapd's socket did not get the translated frame")
	}
	if !bytes.Equal(got[:6], bssid[:]) || !IsUpstream(got[14:]) {
		t.Errorf("in: % x", got[:20])
	}
	up := got[14:]

	// 2. hostapd answers the UniFi BSSID: the LAN gets it in UniFi's
	// layout, from the bridge's address, to the AP's Ethernet address
	send(hostapd, "br0", Frame(unifiBSS, bssid, EtherRRB, up))
	got = recv(lan, func(f []byte) bool { _, _, _, ok := FromUniFi(f[14:]); return ok })
	if got == nil {
		t.Fatal("out: the LAN did not get the translated frame")
	}
	br, _ := net.InterfaceByName("br0")
	if !bytes.Equal(got[:6], unifiEth[:]) || !bytes.Equal(got[6:12], br.HardwareAddr) {
		t.Errorf("out addresses: % x", got[:12])
	}
	if _, isrc, idst, _ := FromUniFi(got[14:]); isrc != bssid || idst != unifiBSS {
		t.Errorf("out: %s -> %s", isrc, idst)
	}

	// 3. a client joins: the LAN gets the roam announcement
	now := time.Now()
	r.Joined("ap0", client, now)
	got = recv(roamFD, func(f []byte) bool { return bytes.Equal(f[:6], RoamGroup[:]) })
	if got == nil {
		t.Fatal("no roam announcement on the LAN")
	}
	if ro, err := ParseRoam(got[14:], key); err != nil || ro.Client != client || ro.BSSID != bssid || ro.Time.Unix() != now.Unix() {
		t.Errorf("announcement: %+v, %v", ro, err)
	}

	// 4. the UniFi AP announces that client: it is dropped here
	kicked := make(chan string, 1)
	r.Kick = func(iface string, c MAC) { kicked <- iface + " " + c.String() }
	p, _ := Roam{BSSID: unifiBSS, WLAN: [2]byte{0xa1, 0xb2}, Client: client, Time: now.Add(time.Second)}.Bytes(key)
	send(roamFD, "lan1", Frame(RoamGroup, unifiEth, EtherRoam, p))
	select {
	case s := <-kicked:
		if s != "ap0 "+client.String() {
			t.Errorf("kick: %s", s)
		}
	case <-time.After(2 * time.Second):
		t.Error("the announced client was not dropped")
	}

	// 5. something takes the port out of the bridge: Sync repairs it
	must(setLinkNoMaster(idx("uowft0")))
	must(r.Sync(r.Serving()))
	if masterOf("uowft0") != "br0" {
		t.Error("the plumbing was not repaired")
	}
}

// setLinkNoMaster takes an interface out of its bridge.
func setLinkNoMaster(index int) error {
	return nlRequest(syscall.RTM_NEWLINK, 0, append(ifinfo(index, 0, 0), nlAttr(iflaMaster, []byte{0, 0, 0, 0})...))
}
