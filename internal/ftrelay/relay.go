// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package ftrelay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"
)

// BSS is one hostapd BSS the relay serves: an AP interface with 802.11r
// whose key holders are the wildcard pair with the controller's key.
type BSS struct {
	Iface  string
	BSSID  MAC
	Bridge string  // hostapd's ft_iface: where it sends and hears RRB frames
	WLAN   [2]byte // mobility domain
	Key    []byte  // the WLAN's 802.11r key, 16 bytes
}

// Relay moves RRB frames between hostapd and the UniFi APs on the LAN.
//
// hostapd keeps its normal interface for these frames, the bridge of the
// WLAN's network, so the Wi-Fi configuration never names anything the relay
// makes: without the relay (daemon stopped, package gone after a firmware
// upgrade) the Wi-Fi still starts, only the key exchange with UniFi APs
// stops.
//
// Out: hostapd's frames are read from the bridge as they leave, and sent
// again in UniFi's layout from the bridge's own address. (The originals
// leave too; UniFi APs drop them as too short.)
//
// In: a frame in UniFi's layout is rebuilt in upstream's and handed to
// hostapd through a veth pair of which one end is a port of the bridge:
// what the relay sends into the other end arrives at the bridge like a
// frame from the network, addressed to the BSSID, and goes up to hostapd's
// socket. That needs the kernel's veth driver (kmod-veth).
type Relay struct {
	Logf    func(format string, a ...any)
	Verbose bool
	// Kick removes a client that a UniFi AP announced as its own.
	Kick func(iface string, client MAC)

	mu    sync.Mutex
	links map[string]*link // by bridge
	bss   []BSS
	peers map[MAC]MAC          // UniFi BSSID -> the Ethernet address its AP sends from
	seen  map[MAC]arrival      // clients that joined here
	once  map[string]time.Time // log lines said once in a while
}

type arrival struct {
	iface string
	at    time.Time
}

// link is the relay's plumbing on one bridge.
type link struct {
	bridge     string
	bridgeMAC  MAC
	port, peer string // veth: port is in the bridge, the relay writes into peer
	tap, inj   int    // sockets: on the bridge, on peer
	peerIndex  int
	stop       chan struct{}
}

const (
	packetOutgoing = 4
	ethPAll        = 0x0003
)

// vethNames are the names of the n-th pair.
func vethNames(n int) (port, peer string) {
	return fmt.Sprintf("uowft%d", n), fmt.Sprintf("uowfp%d", n)
}

// Probe reports whether this kernel can make a veth pair.
func Probe() error {
	a, b := "uowftprobe", "uowfpprobe"
	delLink(a)
	if err := addVeth(a, b); err != nil {
		if errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) {
			return errors.New("the kernel has no veth driver (package kmod-veth)")
		}
		return fmt.Errorf("making a veth pair: %w", err)
	}
	return delLink(a)
}

func (r *Relay) logf(format string, a ...any) {
	if r.Logf != nil {
		r.Logf(format, a...)
	}
}

// logOnce says a line at most once in ten minutes per key.
func (r *Relay) logOnce(key, format string, a ...any) {
	if t, ok := r.once[key]; ok && time.Since(t) < 10*time.Minute {
		return
	}
	if r.once == nil {
		r.once = map[string]time.Time{}
	}
	r.once[key] = time.Now()
	r.logf(format, a...)
}

// Sync makes the relay serve exactly these BSSes: it opens the plumbing on
// their bridges, repairs it where something took it apart (a network
// reload removes foreign bridge ports), and closes what is no longer used.
// Call it at start, after every Wi-Fi change, and every now and then.
func (r *Relay) Sync(bss []BSS) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.links == nil {
		r.links, r.peers, r.seen = map[string]*link{}, map[MAC]MAC{}, map[MAC]arrival{}
	}
	want := map[string]bool{}
	for _, b := range bss {
		want[b.Bridge] = true
	}
	for name, l := range r.links {
		if !want[name] || !l.healthy() {
			l.close()
			delete(r.links, name)
		}
	}
	var names []string
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	var firstErr error
	for _, name := range names {
		if r.links[name] != nil {
			continue
		}
		l, err := r.open(name)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("bridge %s: %w", name, err)
			}
			continue
		}
		r.links[name] = l
		go r.read(l)
	}
	r.bss = nil
	for _, b := range bss {
		if r.links[b.Bridge] != nil {
			r.bss = append(r.bss, b)
		}
	}
	return firstErr
}

// Serving returns the BSSes the relay works for right now.
func (r *Relay) Serving() []BSS {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]BSS(nil), r.bss...)
}

// Close takes everything down.
func (r *Relay) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, l := range r.links {
		l.close()
		delete(r.links, name)
	}
	r.bss = nil
}

func (l *link) healthy() bool {
	return masterOf(l.port) == l.bridge
}

func (l *link) close() {
	close(l.stop)
	syscall.Close(l.tap)
	syscall.Close(l.inj)
	delLink(l.port)
}

// open builds the plumbing on one bridge.
func (r *Relay) open(bridge string) (*link, error) {
	br, err := net.InterfaceByName(bridge)
	if err != nil {
		return nil, err
	}
	if len(br.HardwareAddr) != 6 {
		return nil, errors.New("not an Ethernet interface")
	}
	// a free pair number: one pair per bridge
	used := map[string]bool{}
	for _, l := range r.links {
		used[l.port] = true
	}
	n := 0
	for ; ; n++ {
		if p, _ := vethNames(n); !used[p] {
			break
		}
	}
	l := &link{bridge: bridge, stop: make(chan struct{}), tap: -1, inj: -1}
	copy(l.bridgeMAC[:], br.HardwareAddr)
	l.port, l.peer = vethNames(n)
	delLink(l.port) // left over from a daemon that was killed
	if err := addVeth(l.port, l.peer); err != nil {
		return nil, fmt.Errorf("veth pair: %w", err)
	}
	fail := func(err error) (*link, error) {
		if l.tap >= 0 {
			syscall.Close(l.tap)
		}
		if l.inj >= 0 {
			syscall.Close(l.inj)
		}
		delLink(l.port)
		return nil, err
	}
	// Nothing but the relay's frames belongs on the pair: no addresses, and
	// the bridge floods nothing into its port.
	for _, n := range []string{l.port, l.peer} {
		os.WriteFile("/proc/sys/net/ipv6/conf/"+n+"/disable_ipv6", []byte("1"), 0)
	}
	port, err := net.InterfaceByName(l.port)
	if err != nil {
		return fail(err)
	}
	peer, err := net.InterfaceByName(l.peer)
	if err != nil {
		return fail(err)
	}
	l.peerIndex = peer.Index
	if err := setLink(port.Index, br.Index); err != nil {
		return fail(fmt.Errorf("adding %s to %s: %w", l.port, bridge, err))
	}
	if err := setLink(peer.Index, 0); err != nil {
		return fail(err)
	}
	for _, f := range []string{"unicast_flood", "multicast_flood", "broadcast_flood"} {
		os.WriteFile("/sys/class/net/"+l.port+"/brport/"+f, []byte("0"), 0)
	}
	if l.tap, err = packetSocket(br.Index, ethPAll, true); err != nil {
		return fail(err)
	}
	if l.inj, err = packetSocket(peer.Index, 0, false); err != nil {
		return fail(err)
	}
	return l, nil
}

// packetSocket opens a raw socket on one interface. proto 0 = send only.
// With filter, it hears only the two EtherTypes the relay reads.
func packetSocket(ifindex int, proto uint16, filter bool) (int, error) {
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if filter {
		// EtherType at offset 12: accept 0x88b7 and 0x890d only
		f := []syscall.SockFilter{
			{Code: 0x28, K: 12},
			{Code: 0x15, Jt: 1, K: EtherRRB},
			{Code: 0x15, Jf: 1, K: EtherRoam},
			{Code: 0x06, K: 0xffff},
			{Code: 0x06, K: 0},
		}
		if err := syscall.AttachLsf(fd, f); err != nil {
			syscall.Close(fd)
			return -1, err
		}
	}
	tv := syscall.Timeval{Sec: 1}
	syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: htons(proto), Ifindex: ifindex}); err != nil {
		syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}

// read is the loop on a bridge's socket.
func (r *Relay) read(l *link) {
	buf := make([]byte, 2048)
	for {
		select {
		case <-l.stop:
			return
		default:
		}
		n, from, err := syscall.Recvfrom(l.tap, buf, 0)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EINTR {
				continue
			}
			select {
			case <-l.stop:
			default:
				r.logf("FT: reading %s: %v", l.bridge, err)
				time.Sleep(time.Second)
			}
			continue
		}
		ll, ok := from.(*syscall.SockaddrLinklayer)
		if !ok || n < 14 {
			continue
		}
		for _, out := range r.handle(l.bridge, l.bridgeMAC, buf[:n], ll.Pkttype == packetOutgoing, time.Now()) {
			fd, idx := l.tap, 0
			if out.inject {
				fd, idx = l.inj, l.peerIndex
			} else if ifi, err := net.InterfaceByName(l.bridge); err == nil {
				idx = ifi.Index
			}
			if err := syscall.Sendto(fd, out.frame, 0, &syscall.SockaddrLinklayer{Ifindex: idx, Halen: 6}); err != nil {
				r.logOnce("send "+l.bridge, "FT: sending on %s: %v", l.bridge, err)
			}
		}
	}
}

// output is a frame to send: into hostapd (inject) or to the LAN.
type output struct {
	frame  []byte
	inject bool
}

func (r *Relay) local(bridge string) map[MAC]BSS {
	m := map[MAC]BSS{}
	for _, b := range r.bss {
		if b.Bridge == bridge {
			m[b.BSSID] = b
		}
	}
	return m
}

// handle decides what to do with one frame seen on a bridge. It holds all
// of the relay's logic and touches no socket.
func (r *Relay) handle(bridge string, bridgeMAC MAC, f []byte, outgoing bool, now time.Time) []output {
	var dst, src MAC
	copy(dst[:], f)
	copy(src[:], f[6:])
	p := f[14:]
	r.mu.Lock()
	defer r.mu.Unlock()
	local := r.local(bridge)
	switch binary.BigEndian.Uint16(f[12:]) {
	case EtherRRB:
		if outgoing {
			// hostapd -> UniFi APs
			if _, mine := local[src]; !mine {
				return nil // the relay's own frames, or a BSS it does not serve
			}
			if _, mine := local[dst]; mine {
				return nil
			}
			u, ok := ToUniFi(p, src, dst)
			if !ok {
				return nil
			}
			to := Broadcast
			if e, ok := r.peers[dst]; ok && dst != Broadcast {
				to = e
			}
			if r.Verbose {
				r.logf("FT: type %d %s -> %s: to the LAN (%s)", p[5], src, dst, to)
			}
			return []output{{frame: Frame(to, bridgeMAC, EtherRRB, u)}}
		}
		// UniFi AP -> hostapd
		up, isrc, idst, ok := FromUniFi(p)
		if !ok {
			if _, mine := local[dst]; !mine && IsUpstream(p) && src != bridgeMAC {
				if _, known := r.peers[src]; !known {
					r.logOnce("upstream "+src.String(), "FT: %s sends key-exchange frames in upstream hostapd's layout (an OpenWrt AP without this relay): hostapd talks to it directly", src)
				}
			}
			return nil
		}
		if _, mine := local[isrc]; mine {
			return nil
		}
		if _, known := r.peers[isrc]; !known {
			r.logf("FT: UniFi BSS %s (AP %s) found", isrc, src)
		}
		r.peers[isrc] = src
		var outs []output
		for bssid := range local {
			if idst == Broadcast || idst == bssid {
				outs = append(outs, output{frame: Frame(bssid, isrc, EtherRRB, up), inject: true})
			}
		}
		if r.Verbose {
			r.logf("FT: type %d %s -> %s: to hostapd (%d BSS)", up[5], isrc, idst, len(outs))
		}
		return outs
	case EtherRoam:
		if outgoing {
			return nil
		}
		r.roamed(local, p, now)
	}
	return nil
}

// roamed handles a UniFi AP's roam announcement: the client is on that AP
// now, so it is dropped here unless it came back since.
func (r *Relay) roamed(local map[MAC]BSS, p []byte, now time.Time) {
	sender, wlan, ok := RoamHeader(p)
	if !ok {
		return
	}
	if _, mine := local[sender]; mine {
		return
	}
	for _, b := range local {
		if b.WLAN != wlan || len(b.Key) != 16 {
			continue
		}
		ro, err := ParseRoam(p, b.Key)
		if err != nil {
			r.logOnce("roamkey "+sender.String(), "ROAM: an announcement from %s for this WLAN does not decrypt with its 802.11r key", sender)
			return
		}
		if d := now.Sub(ro.Time); d > RoamMaxAge || d < -RoamMaxAge {
			r.logOnce("roamclock", "ROAM: announcement from %s ignored: its time differs from this AP's clock by %s", sender, d.Round(time.Second))
			return
		}
		a, here := r.seen[ro.Client]
		if !here || a.at.Unix() > ro.Time.Unix() {
			return // not ours, or it came back after that announcement
		}
		delete(r.seen, ro.Client)
		r.logf("ROAM: %s moved to %s: dropped from %s", ro.Client, sender, a.iface)
		if r.Kick != nil {
			go r.Kick(a.iface, ro.Client)
		}
		return
	}
}

// Joined tells the relay that hostapd authorized a client on an interface.
// On a served BSS, the UniFi APs are told, so the one it left drops it.
func (r *Relay) Joined(iface string, client MAC, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.bss {
		if b.Iface != iface {
			continue
		}
		r.seen[client] = arrival{iface: iface, at: now}
		l := r.links[b.Bridge]
		if l == nil || len(b.Key) != 16 {
			return
		}
		p, err := Roam{BSSID: b.BSSID, WLAN: b.WLAN, Client: client, Time: now}.Bytes(b.Key)
		if err != nil {
			return
		}
		idx := 0
		if ifi, err := net.InterfaceByName(l.bridge); err == nil {
			idx = ifi.Index
		}
		if err := syscall.Sendto(l.tap, Frame(RoamGroup, l.bridgeMAC, EtherRoam, p), 0, &syscall.SockaddrLinklayer{Ifindex: idx, Halen: 6}); err != nil {
			r.logOnce("roamsend", "ROAM: sending the announcement: %v", err)
		} else if r.Verbose {
			r.logf("ROAM: %s joined %s: announced", client, iface)
		}
		return
	}
}

// Left forgets a client that disconnected.
func (r *Relay) Left(iface string, client MAC) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.seen[client]; ok && a.iface == iface {
		delete(r.seen, client)
	}
}
