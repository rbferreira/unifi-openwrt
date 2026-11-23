// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// "uow wire-probe GATEWAY TRIES IFACE...": does the gateway answer an ARP
// request sent out of one of these interfaces? Exit 0 = yes.
//
// For the mesh-uplink watcher (internal/openwrt/mesh.go), which must know
// whether a cable with link leads to the gateway while the wireless uplink is
// in the same bridge. A ping cannot tell: it leaves by whichever port the
// bridge has learned the gateway on. This request leaves by the cable only (a
// packet socket on the port sends past the bridge). The answer may come back
// through either, so it is listened for on every interface.
//
// The request is an ARP probe (sender address 0.0.0.0, RFC 5227). The owner
// of an address answers it like any request, the answer names 0.0.0.0 as its
// target, which no answer to the kernel's own requests does, and no ARP
// table takes anything in from it.
func wireProbe(args []string) int {
	gw, tries, send, _, ok := wireProbeArgs(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "usage: uow wire-probe GATEWAY TRIES IFACE...")
		return 2
	}
	const ethARP = 0x0806
	proto := uint16(ethARP>>8 | ethARP&0xff<<8) // network byte order
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, int(proto))
	if err != nil {
		fmt.Fprintln(os.Stderr, "wire-probe:", err)
		return 2
	}
	defer syscall.Close(fd)
	syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &syscall.Timeval{Usec: 200000})

	type port struct {
		index int
		frame []byte
	}
	var ports []port
	var macs [][]byte
	for _, name := range send {
		ifi, err := net.InterfaceByName(name)
		if err != nil || len(ifi.HardwareAddr) != 6 {
			continue
		}
		ports = append(ports, port{ifi.Index, arpProbe(ifi.HardwareAddr, gw)})
		macs = append(macs, ifi.HardwareAddr)
	}
	if len(ports) == 0 {
		return 1
	}
	bcast := [8]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	buf := make([]byte, 1500)
	for t := 0; t < tries; t++ {
		for _, p := range ports {
			syscall.Sendto(fd, p.frame, 0, &syscall.SockaddrLinklayer{Protocol: proto, Ifindex: p.index, Halen: 6, Addr: bcast})
		}
		for end := time.Now().Add(time.Second); time.Now().Before(end); {
			n, _, err := syscall.Recvfrom(fd, buf, 0)
			if err != nil || !arpProbeAnswer(buf[:n], gw, macs) {
				continue
			}
			return 0
		}
	}
	return 1
}
