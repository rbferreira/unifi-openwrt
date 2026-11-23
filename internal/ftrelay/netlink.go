// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package ftrelay

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"syscall"
)

// The few rtnetlink requests the relay needs, written out by hand: OpenWrt's
// busybox ip cannot make a veth pair on every release, and the daemon should
// not need the full ip package.

var ne = binary.NativeEndian

const (
	iflaIfname   = 3
	iflaMaster   = 10
	iflaLinkinfo = 18
	infoKind     = 1
	infoData     = 2
	vethInfoPeer = 1
	nlaNested    = 0x8000
)

func nlAttr(typ uint16, data []byte) []byte {
	l := 4 + len(data)
	b := make([]byte, (l+3)&^3)
	ne.PutUint16(b, uint16(l))
	ne.PutUint16(b[2:], typ)
	copy(b[4:], data)
	return b
}

// ifinfo is struct ifinfomsg.
func ifinfo(index int, flags, change uint32) []byte {
	b := make([]byte, 16)
	ne.PutUint32(b[4:], uint32(index))
	ne.PutUint32(b[8:], flags)
	ne.PutUint32(b[12:], change)
	return b
}

// nlRequest sends one rtnetlink request and waits for its acknowledgement.
func nlRequest(typ uint16, flags uint16, body []byte) error {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	msg := make([]byte, 16+len(body))
	ne.PutUint32(msg, uint32(len(msg)))
	ne.PutUint16(msg[4:], typ)
	ne.PutUint16(msg[6:], syscall.NLM_F_REQUEST|syscall.NLM_F_ACK|flags)
	ne.PutUint32(msg[8:], 1)
	copy(msg[16:], body)
	if err := syscall.Sendto(fd, msg, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	n, _, err := syscall.Recvfrom(fd, buf, 0)
	if err != nil {
		return err
	}
	if n < 20 || ne.Uint16(buf[4:]) != syscall.NLMSG_ERROR {
		return fmt.Errorf("netlink: unexpected answer")
	}
	if e := int32(ne.Uint32(buf[16:])); e != 0 {
		return syscall.Errno(-e)
	}
	return nil
}

// addVeth makes the pair a <-> b.
func addVeth(a, b string) error {
	peer := append(ifinfo(0, 0, 0), nlAttr(iflaIfname, append([]byte(b), 0))...)
	data := nlAttr(vethInfoPeer|nlaNested, peer)
	info := append(nlAttr(infoKind, []byte("veth")), nlAttr(infoData|nlaNested, data)...)
	body := append(ifinfo(0, 0, 0), nlAttr(iflaIfname, append([]byte(a), 0))...)
	body = append(body, nlAttr(iflaLinkinfo|nlaNested, info)...)
	return nlRequest(syscall.RTM_NEWLINK, syscall.NLM_F_CREATE|syscall.NLM_F_EXCL, body)
}

// delLink removes an interface (a veth takes its peer along).
func delLink(name string) error {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	return nlRequest(syscall.RTM_DELLINK, 0, ifinfo(ifi.Index, 0, 0))
}

// setLink brings an interface up and, with master > 0, makes it a port of
// that bridge.
func setLink(index, master int) error {
	body := ifinfo(index, syscall.IFF_UP, syscall.IFF_UP)
	if master > 0 {
		m := make([]byte, 4)
		ne.PutUint32(m, uint32(master))
		body = append(body, nlAttr(iflaMaster, m)...)
	}
	return nlRequest(syscall.RTM_NEWLINK, 0, body)
}

// masterOf returns the bridge an interface is a port of ("" = none).
func masterOf(name string) string {
	t, err := os.Readlink("/sys/class/net/" + name + "/master")
	if err != nil {
		return ""
	}
	for i := len(t) - 1; i >= 0; i-- {
		if t[i] == '/' {
			return t[i+1:]
		}
	}
	return t
}

func htons(v uint16) uint16 { return ne.Uint16([]byte{byte(v >> 8), byte(v)}) }
