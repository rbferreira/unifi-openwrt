// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"encoding/binary"
	"net"
	"strconv"
	"strings"
)

// wireProbeArgs reads "GATEWAY TRIES IFACE... [+IFACE...]": the interfaces to
// send the request out of, and with a + those to listen on as well (used
// where a socket hears one interface only).
func wireProbeArgs(args []string) (gw net.IP, tries int, send, listen []string, ok bool) {
	if len(args) < 3 {
		return nil, 0, nil, nil, false
	}
	gw = net.ParseIP(args[0]).To4()
	tries, err := strconv.Atoi(args[1])
	if gw == nil || err != nil || tries < 1 || tries > 10 {
		return nil, 0, nil, nil, false
	}
	for _, a := range args[2:] {
		if l, plus := strings.CutPrefix(a, "+"); plus {
			listen = append(listen, l)
		} else {
			send = append(send, a)
		}
	}
	return gw, tries, send, listen, len(send) > 0
}

// arpProbe is an Ethernet frame with an ARP request for ip from mac, sender
// address 0.0.0.0.
func arpProbe(mac net.HardwareAddr, ip net.IP) []byte {
	f := make([]byte, 42)
	copy(f[0:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(f[6:12], mac)
	binary.BigEndian.PutUint16(f[12:], 0x0806)
	binary.BigEndian.PutUint16(f[14:], 1)      // Ethernet
	binary.BigEndian.PutUint16(f[16:], 0x0800) // IPv4
	f[18], f[19] = 6, 4
	binary.BigEndian.PutUint16(f[20:], 1) // request
	copy(f[22:28], mac)
	// sender address 0.0.0.0, target hardware address zero
	copy(f[38:42], ip)
	return f
}

// arpProbeAnswer reports whether f is ip's answer to an arpProbe from one of
// macs.
func arpProbeAnswer(f []byte, ip net.IP, macs [][]byte) bool {
	if len(f) < 42 || binary.BigEndian.Uint16(f[12:]) != 0x0806 || binary.BigEndian.Uint16(f[20:]) != 2 {
		return false
	}
	if !bytes.Equal(f[28:32], ip) || !bytes.Equal(f[38:42], []byte{0, 0, 0, 0}) {
		return false
	}
	for _, m := range macs {
		if bytes.Equal(f[32:38], m) {
			return true
		}
	}
	return false
}
