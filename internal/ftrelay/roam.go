// SPDX-License-Identifier: GPL-2.0-or-later

package ftrelay

import (
	"crypto/aes"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"time"
)

// RoamGroup is where UniFi APs send roam announcements.
var RoamGroup = MAC{0x01, 0x80, 0xc2, 0x00, 0x00, 0x1c}

// RoamMaxAge: UniFi APs ignore an announcement older than this, so both
// clocks must be right.
const RoamMaxAge = 60 * time.Second

// A UniFi AP broadcasts a roam announcement whenever a client associates,
// and the AP the client left drops it at once instead of keeping it until
// its own timers expire. The payload (64 bytes):
//
//	01 f0 1a 00 | sender BSSID (6) | wlan id (2) | AES key wrap (32) | 20 zero bytes
//	wrapped:      time in seconds (LE32) | client MAC (6) | 14 zero bytes
//
// The wlan id is the WLAN's mobility domain and the key is the WLAN's
// 802.11r key, both the controller's values.
var roamMagic = []byte{0x01, 0xf0, 0x1a, 0x00}

const roamLen = 4 + 6 + 2 + 32 + 20

// Roam is one roam announcement.
type Roam struct {
	BSSID  MAC       // the BSS the client is on now
	WLAN   [2]byte   // mobility domain
	Client MAC       // the client
	Time   time.Time // when it associated, by the sender's clock
}

// RoamHeader reads the unencrypted part: who sent it and for which WLAN.
func RoamHeader(p []byte) (bssid MAC, wlan [2]byte, ok bool) {
	if len(p) < roamLen || string(p[:4]) != string(roamMagic) {
		return bssid, wlan, false
	}
	copy(bssid[:], p[4:])
	copy(wlan[:], p[10:])
	return bssid, wlan, true
}

// ParseRoam decrypts an announcement with the WLAN's 802.11r key (16 bytes).
func ParseRoam(p, key []byte) (Roam, error) {
	var r Roam
	var ok bool
	if r.BSSID, r.WLAN, ok = RoamHeader(p); !ok {
		return r, errors.New("not a roam announcement")
	}
	plain, err := keyUnwrap(key, p[12:44])
	if err != nil {
		return r, err
	}
	r.Time = time.Unix(int64(binary.LittleEndian.Uint32(plain)), 0)
	copy(r.Client[:], plain[4:])
	return r, nil
}

// Bytes builds the announcement.
func (r Roam) Bytes(key []byte) ([]byte, error) {
	plain := make([]byte, 24)
	binary.LittleEndian.PutUint32(plain, uint32(r.Time.Unix()))
	copy(plain[4:], r.Client[:])
	wrapped, err := keyWrap(key, plain)
	if err != nil {
		return nil, err
	}
	p := make([]byte, 0, roamLen)
	p = append(p, roamMagic...)
	p = append(p, r.BSSID[:]...)
	p = append(p, r.WLAN[:]...)
	p = append(p, wrapped...)
	return append(p, make([]byte, 20)...), nil
}

// AES key wrap, RFC 3394.
var kwIV = []byte{0xa6, 0xa6, 0xa6, 0xa6, 0xa6, 0xa6, 0xa6, 0xa6}

func keyWrap(key, plain []byte) ([]byte, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	n := len(plain) / 8
	if n < 2 || len(plain)%8 != 0 {
		return nil, errors.New("key wrap: the data must be a multiple of 8 bytes, 16 or more")
	}
	out := make([]byte, 8+len(plain))
	copy(out, kwIV)
	copy(out[8:], plain)
	var b [16]byte
	for j := 0; j < 6; j++ {
		for i := 1; i <= n; i++ {
			copy(b[:8], out[:8])
			copy(b[8:], out[8*i:])
			c.Encrypt(b[:], b[:])
			t := uint64(n*j + i)
			binary.BigEndian.PutUint64(out[:8], binary.BigEndian.Uint64(b[:8])^t)
			copy(out[8*i:], b[8:])
		}
	}
	return out, nil
}

func keyUnwrap(key, wrapped []byte) ([]byte, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	n := len(wrapped)/8 - 1
	if n < 2 || len(wrapped)%8 != 0 {
		return nil, errors.New("key unwrap: bad length")
	}
	out := append([]byte(nil), wrapped...)
	var b [16]byte
	for j := 5; j >= 0; j-- {
		for i := n; i >= 1; i-- {
			t := uint64(n*j + i)
			binary.BigEndian.PutUint64(b[:8], binary.BigEndian.Uint64(out[:8])^t)
			copy(b[8:], out[8*i:8*i+8])
			c.Decrypt(b[:], b[:])
			copy(out[:8], b[:8])
			copy(out[8*i:], b[8:])
		}
	}
	if subtle.ConstantTimeCompare(out[:8], kwIV) != 1 {
		return nil, errors.New("key unwrap: wrong key")
	}
	return out[8:], nil
}
