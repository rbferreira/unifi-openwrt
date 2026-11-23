// SPDX-License-Identifier: GPL-2.0-or-later

package ftrelay

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A sequence request in UniFi's layout, as a UniFi AP sends it (addresses
// and bytes made up, lengths as captured): type 4, sender BSSID, broadcast,
// auth_len 0x2e, three TLVs (16, 12 and 6 bytes), a 16 byte SIV tag.
const unifiSeqReq = "0013740001" + "04" + "021111111105" + "ffffffffffff" + "2e00" +
	"02001000" + "000102030405060708090a0b0c0d0e0f" +
	"04000c00" + "303231313131313131313035" +
	"05000600" + "021111111105" +
	"a0a1a2a3a4a5a6a7a8a9aaabacadaeaf"

func TestTranslate(t *testing.T) {
	u := unhex(t, unifiSeqReq)
	up, src, dst, ok := FromUniFi(u)
	if !ok {
		t.Fatal("UniFi frame not recognised")
	}
	if src.String() != "02:11:11:11:11:05" || dst != Broadcast {
		t.Errorf("addresses: %s -> %s", src, dst)
	}
	if len(up) != len(u)-12 || !IsUpstream(up) || up[5] != TypeSeqReq {
		t.Errorf("upstream frame: % x", up)
	}
	if IsUpstream(u) {
		t.Error("a UniFi frame passes as upstream")
	}
	if _, _, _, ok := FromUniFi(up); ok {
		t.Error("an upstream frame passes as UniFi")
	}
	back, ok := ToUniFi(up, src, dst)
	if !ok || !bytes.Equal(back, u) {
		t.Errorf("round trip:\n% x\n% x", back, u)
	}
}

func TestTranslateRejects(t *testing.T) {
	u := unhex(t, unifiSeqReq)
	for name, p := range map[string][]byte{
		"empty":             nil,
		"other OUI":         append([]byte{0x00, 0x13, 0x75}, u[3:]...),
		"type 0":            append(append([]byte{}, u[:5]...), append([]byte{0}, u[6:]...)...),
		"type 6":            append(append([]byte{}, u[:5]...), append([]byte{6}, u[6:]...)...),
		"cut in the TLVs":   u[:40],
		"no encrypted part": u[:len(u)-16],
		"group sender":      append(append([]byte{}, u[:6]...), append([]byte{0x03}, u[7:]...)...),
	} {
		if _, _, _, ok := FromUniFi(p); ok {
			t.Errorf("%s: accepted", name)
		}
	}
	// a TLV that runs past auth_len
	bad := append([]byte{}, u...)
	bad[23] = 0xff
	if _, _, _, ok := FromUniFi(bad); ok {
		t.Error("TLV longer than the authenticated part: accepted")
	}
	if _, ok := ToUniFi(u, MAC{}, MAC{}); ok {
		t.Error("ToUniFi took a frame that is not upstream's")
	}
}

func TestKeyWrapRFC3394(t *testing.T) {
	// RFC 3394 4.1: 128 bit data, 128 bit key.
	key := unhex(t, "000102030405060708090A0B0C0D0E0F")
	plain := unhex(t, "00112233445566778899AABBCCDDEEFF")
	want := unhex(t, "1FA68B0A8112B447AEF34BD8FB5A7B829D3E862371D2CFE5")
	got, err := keyWrap(key, plain)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("wrap: % x, %v", got, err)
	}
	back, err := keyUnwrap(key, got)
	if err != nil || !bytes.Equal(back, plain) {
		t.Fatalf("unwrap: % x, %v", back, err)
	}
	got[3] ^= 1
	if _, err := keyUnwrap(key, got); err == nil {
		t.Error("a damaged wrap unwrapped")
	}
}

func TestRoam(t *testing.T) {
	key := unhex(t, "00112233445566778899aabbccddeeff")
	r := Roam{BSSID: MAC{2, 0x11, 0x11, 0x11, 0x11, 5}, WLAN: [2]byte{0xa1, 0xb2},
		Client: MAC{2, 0x33, 0x33, 0x33, 0x33, 1}, Time: time.Unix(1700000000, 0)}
	p, err := r.Bytes(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 64 || !bytes.Equal(p[:12], unhex(t, "01f01a00"+"021111111105"+"a1b2")) || !bytes.Equal(p[44:], make([]byte, 20)) {
		t.Fatalf("layout: % x", p)
	}
	got, err := ParseRoam(p, key)
	if err != nil || got != r {
		t.Fatalf("parsed %+v, %v", got, err)
	}
	if _, err := ParseRoam(p, unhex(t, "ff112233445566778899aabbccddeeff")); err == nil {
		t.Error("another WLAN's key decrypted it")
	}
	if _, _, ok := RoamHeader(p[:63]); ok {
		t.Error("short frame accepted")
	}
}
