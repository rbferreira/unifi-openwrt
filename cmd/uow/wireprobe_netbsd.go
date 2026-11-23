// SPDX-License-Identifier: GPL-2.0-or-later

//go:build netbsd

package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// "uow wire-probe GATEWAY TRIES IFACE... [+IFACE...]" on NetBSD (the AirPort):
// see wireprobe_linux.go for what it is for. Here the request is written to
// a BPF device bound to the wired interface, which sends it past the bridge,
// and the answer is read from BPF devices on every interface named: it can
// come back through the wireless uplink (+IFACE).
func wireProbe(args []string) int {
	gw, tries, send, listen, ok := wireProbeArgs(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "usage: uow wire-probe GATEWAY TRIES IFACE... [+IFACE...]")
		return 2
	}
	type port struct {
		fd    int
		frame []byte
	}
	var ports []port
	var macs [][]byte
	found := make(chan bool, 8)
	for i, name := range append(append([]string{}, send...), listen...) {
		// IFACE@MAC: Go's net package cannot list the interfaces of the
		// AirPort's NetBSD 6, so its boot script passes the address
		name, mac, _ := strings.Cut(name, "@")
		fd, buflen, err := bpfOpen(name)
		if err != nil {
			if i < len(send) {
				fmt.Fprintf(os.Stderr, "wire-probe: %s: %v\n", name, err)
			}
			continue
		}
		if i < len(send) {
			hw, err := net.ParseMAC(mac)
			if err != nil {
				if ifi, e := net.InterfaceByName(name); e == nil {
					hw, err = ifi.HardwareAddr, nil
				}
			}
			if err != nil || len(hw) != 6 {
				fmt.Fprintf(os.Stderr, "wire-probe: %s: no hardware address (give IFACE@MAC)\n", name)
				continue
			}
			ports = append(ports, port{fd, arpProbe(hw, gw)})
			macs = append(macs, hw)
		}
		go func(fd, buflen int) {
			buf := make([]byte, buflen)
			for {
				n, err := syscall.Read(fd, buf)
				if err != nil && err != syscall.EINTR && err != syscall.EAGAIN {
					return
				}
				for off := 0; n > 0 && off+int(unsafe.Sizeof(syscall.BpfHdr{})) <= n; {
					h := (*syscall.BpfHdr)(unsafe.Pointer(&buf[off]))
					start, end := off+int(h.Hdrlen), off+int(h.Hdrlen)+int(h.Caplen)
					if h.Hdrlen == 0 || end > n {
						break
					}
					if arpProbeAnswer(buf[start:end], gw, macs) {
						found <- true
						return
					}
					off = (end + syscall.BPF_ALIGNMENT - 1) &^ (syscall.BPF_ALIGNMENT - 1)
				}
			}
		}(fd, buflen)
	}
	if len(ports) == 0 {
		return 2
	}
	for t := 0; t < tries; t++ {
		for _, p := range ports {
			syscall.Write(p.fd, p.frame)
		}
		select {
		case <-found:
			return 0
		case <-time.After(time.Second):
		}
	}
	return 1
}

// bpfOpen binds a BPF device to an interface: packets at once, our own
// source address kept. (No read timeout: the ioctl number for it differs
// between NetBSD releases, and the process ends with the last try anyway.)
func bpfOpen(ifname string) (fd, buflen int, err error) {
	fd, err = syscall.Open("/dev/bpf", syscall.O_RDWR, 0)
	if err != nil {
		return 0, 0, err
	}
	ioctl := func(req uintptr, p unsafe.Pointer) error {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(p)); e != 0 {
			return e
		}
		return nil
	}
	var ifreq [144]byte // struct ifreq: the name, then a sockaddr_storage
	copy(ifreq[:15], ifname)
	one := uint32(1)
	var blen uint32
	for _, c := range []struct {
		req uintptr
		p   unsafe.Pointer
	}{
		{syscall.BIOCSETIF, unsafe.Pointer(&ifreq[0])},
		{syscall.BIOCIMMEDIATE, unsafe.Pointer(&one)},
		{syscall.BIOCSHDRCMPLT, unsafe.Pointer(&one)},
		{syscall.BIOCGBLEN, unsafe.Pointer(&blen)},
	} {
		if err := ioctl(c.req, c.p); err != nil {
			syscall.Close(fd)
			return 0, 0, err
		}
	}
	return fd, int(blen), nil
}
