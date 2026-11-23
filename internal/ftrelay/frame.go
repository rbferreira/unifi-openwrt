// SPDX-License-Identifier: GPL-2.0-or-later

// Package ftrelay lets OpenWrt's hostapd exchange 802.11r keys with UniFi
// access points, and speaks UniFi's roam announcements.
//
// Both vendors run hostapd and use its RRB-OUI frames (EtherType 0x88b7,
// OUI 00:13:74, subtype 0x0001) with the same encryption. UniFi's hostapd
// puts two addresses after the frame type, the sending and the receiving
// BSSID, because it sends from the AP's Ethernet address and not from the
// BSSID as upstream hostapd does:
//
//	upstream: 00 13 74 00 01 | type | auth_len | auth TLVs | encrypted
//	UniFi:    00 13 74 00 01 | type | src BSSID | dst BSSID | auth_len | auth TLVs | encrypted
//
// Each side drops the other's frames as too short. The relay adds or strips
// those twelve bytes; it never needs the key for that. The difference was
// found and documented by the rrb-unifi project
// (https://github.com/d-malinowski/unifi-openwrt-roaming); the layouts here
// were checked against frames of a UniFi Express 7.
package ftrelay

import (
	"encoding/binary"
	"fmt"
	"net"
)

// EtherTypes of the two protocols.
const (
	EtherRRB  = 0x88b7 // hostapd's key exchange between APs (OUI Extended EtherType)
	EtherRoam = 0x890d // UniFi's roam announcement (the 802.11 RRB EtherType)
)

// RRB-OUI frame types (hostapd's FT_PACKET_R0KH_R1KH_*).
const (
	TypePull    = 1
	TypeResp    = 2
	TypePush    = 3
	TypeSeqReq  = 4
	TypeSeqResp = 5
)

// MAC is an Ethernet address or BSSID.
type MAC [6]byte

// Broadcast is ff:ff:ff:ff:ff:ff.
var Broadcast = MAC{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

func (m MAC) String() string { return net.HardwareAddr(m[:]).String() }

// ParseMAC reads aa:bb:cc:dd:ee:ff.
func ParseMAC(s string) (MAC, error) {
	var m MAC
	h, err := net.ParseMAC(s)
	if err != nil || len(h) != 6 {
		return m, fmt.Errorf("not a MAC address: %q", s)
	}
	copy(m[:], h)
	return m, nil
}

// rrbPrefix starts every RRB-OUI payload: hostapd's OUI and subtype.
var rrbPrefix = []byte{0x00, 0x13, 0x74, 0x00, 0x01}

const rrbHead = 6 // prefix + type byte

// sivLen is the least an encrypted part can be: the AES-SIV tag.
const sivLen = 16

// validBody checks hostapd's layout from auth_len on: a length, exactly that
// many bytes of TLVs (type and length, 16 bit little endian each), then the
// encrypted part.
func validBody(b []byte) bool {
	if len(b) < 2 {
		return false
	}
	alen := int(binary.LittleEndian.Uint16(b))
	if alen < 4 || 2+alen+sivLen > len(b) {
		return false
	}
	tlv := b[2 : 2+alen]
	for len(tlv) > 0 {
		if len(tlv) < 4 {
			return false
		}
		l := int(binary.LittleEndian.Uint16(tlv[2:]))
		if 4+l > len(tlv) {
			return false
		}
		tlv = tlv[4+l:]
	}
	return true
}

func rrbType(p []byte) (byte, bool) {
	if len(p) < rrbHead || string(p[:len(rrbPrefix)]) != string(rrbPrefix) {
		return 0, false
	}
	t := p[len(rrbPrefix)]
	return t, t >= TypePull && t <= TypeSeqResp
}

// IsUpstream reports whether p (an Ethernet payload) is an RRB-OUI frame in
// upstream hostapd's layout.
func IsUpstream(p []byte) bool {
	_, ok := rrbType(p)
	return ok && validBody(p[rrbHead:])
}

// ToUniFi turns an upstream frame into UniFi's layout. src and dst are the
// BSSIDs hostapd put into the Ethernet header.
func ToUniFi(p []byte, src, dst MAC) ([]byte, bool) {
	if !IsUpstream(p) {
		return nil, false
	}
	out := make([]byte, 0, len(p)+12)
	out = append(out, p[:rrbHead]...)
	out = append(out, src[:]...)
	out = append(out, dst[:]...)
	return append(out, p[rrbHead:]...), true
}

// FromUniFi turns a frame in UniFi's layout into upstream's, and returns the
// two BSSIDs it carried.
func FromUniFi(p []byte) (out []byte, src, dst MAC, ok bool) {
	if _, ok := rrbType(p); !ok || len(p) < rrbHead+12 || !validBody(p[rrbHead+12:]) {
		return nil, src, dst, false
	}
	copy(src[:], p[rrbHead:])
	copy(dst[:], p[rrbHead+6:])
	if src[0]&1 != 0 { // a BSSID is never a group address
		return nil, src, dst, false
	}
	out = make([]byte, 0, len(p)-12)
	out = append(out, p[:rrbHead]...)
	return append(out, p[rrbHead+12:]...), src, dst, true
}

// Frame builds an Ethernet frame.
func Frame(dst, src MAC, etherType uint16, payload []byte) []byte {
	f := make([]byte, 0, 14+len(payload))
	f = append(f, dst[:]...)
	f = append(f, src[:]...)
	f = append(f, byte(etherType>>8), byte(etherType))
	return append(f, payload...)
}
