// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// The mesh uplink's credentials on an AirPort. The uplink is run by the unit's
// boot script (contrib/airport/up.sh) with a wpa_supplicant config on flash,
// /mnt/Flash/mesh/wpa.conf, that used to be written by hand at install time.
// The controller hands a mesh child the backhaul SSID and passphrase
// (mesh.essid, mesh.psk); the file holds the WPA PMK of that pair, which is
// PBKDF2-HMAC-SHA1(passphrase, ssid, 4096 iterations, 32 bytes). Here the
// daemon derives it and keeps the file in step, so a key the controller
// rotates reaches the unit. The running association keeps its key: nothing
// here touches the uplink, the file is read at the next join.

const (
	apUplinkDir = "/mnt/Flash/mesh"
)

// AirPortUplinkConf is the wpa.conf the controller's mesh settings ask for. It
// returns an empty text and a note when there is nothing to do (the mesh is
// not enabled), and an error for credentials it will not write: an empty or
// unprintable SSID or passphrase, or one with a double quote, which would end
// the quoted ssid early. The error never carries the passphrase or the PMK.
func AirPortUplinkConf(cfg translate.Cfg) (text, note string, err error) {
	if cfg["mesh.status"] != "enabled" {
		return "", "mesh uplink credentials: mesh is not enabled on the controller, nothing to keep in sync", nil
	}
	ssid, psk := cfg["mesh.essid"], cfg["mesh.psk"]
	switch {
	case ssid == "":
		return "", "", errors.New("mesh.essid is empty")
	case psk == "":
		return "", "", errors.New("mesh.psk is empty")
	case !apPrintable(ssid):
		return "", "", errors.New("mesh.essid has a double quote, a newline or a character that is not printable ASCII")
	case !apPrintable(psk):
		return "", "", errors.New("mesh.psk has a double quote, a newline or a character that is not printable ASCII")
	case len(ssid) > 32:
		return "", "", errors.New("mesh.essid is longer than 32 characters")
	case len(psk) < 8 || len(psk) > 63:
		return "", "", errors.New("mesh.psk is not 8 to 63 characters")
	}
	pmk, err := pbkdf2.Key(sha1.New, psk, []byte(ssid), 4096, 32)
	if err != nil {
		return "", "", fmt.Errorf("deriving the key: %w", err)
	}
	// the same text as contrib/airport/wpa.conf.example, tabs and all
	return "ctrl_interface=/var/run/wpa_own\nap_scan=1\nnetwork={\n" +
		"\tssid=\"" + ssid + "\"\n" +
		"\tscan_ssid=1\n\tkey_mgmt=WPA-PSK\n\tproto=RSN\n\tpairwise=CCMP\n\tgroup=CCMP\n" +
		"\tpsk=" + hex.EncodeToString(pmk) + "\n}\n", "", nil
}

// apPrintable: printable ASCII, space included, and no double quote.
func apPrintable(s string) bool {
	for _, c := range []byte(s) {
		if c < 0x20 || c > 0x7e || c == '"' {
			return false
		}
	}
	return true
}

// apUplinkHead is the script's variables and its failure exit. $R is in RAM:
// the wanted text is compared there, so that the flash is written only on a
// real change.
const apUplinkHead = apPATH + `umask 077
D=` + apUplinkDir + `
R=` + apRunDir + `
F=$D/wpa.conf
T=$R/wpa.want
N=$D/wpa.conf.new
fail() { echo "mesh failed: $1"; rm -f $T $N; exit 0; }
mkdir -p $D $R || fail "no directory"
cat > $T <<'`

const apUplinkTail = `[ -s $T ] || fail "wanted text empty"
have=1
[ -f $F ] || have=0
if [ $have = 1 ] && [ "$(cat $F)" = "$(cat $T)" ]; then
	rm -f $T
	exit 0
fi
if [ $have = 1 ]; then
	cp $F $F.prev || fail "cannot keep wpa.conf.prev"
fi
cat $T > $N || fail "cannot write wpa.conf.new"
[ -s $N ] && [ "$(tail -n 1 $N)" = "}" ] || fail "wpa.conf.new is empty or cut short"
mv $N $F || fail "cannot move wpa.conf.new into place"
rm -f $T
if [ $have = 1 ]; then
	echo "mesh: uplink credentials updated from the controller (used at the next join)"
else
	echo "mesh: no wpa.conf on the unit, uplink credentials created from the controller (used at the next join)"
fi
exit 0
`

// AirPortUplinkScript is the script that brings /mnt/Flash/mesh/wpa.conf to
// text. It prints nothing and writes nothing when the file already says it;
// otherwise it keeps the old file as wpa.conf.prev (up.sh falls back to it when
// the new key does not associate), writes wpa.conf.new, checks it and moves it
// into place. It never touches the uplink. This is the one script that writes
// in /mnt/Flash/mesh, and the flash wears: hence the compare first. The text
// goes in through a quoted heredoc, so nothing in it is expanded; it is the
// caller's (AirPortUplinkConf) and never reaches a command line.
func AirPortUplinkScript(text string) (string, error) {
	switch {
	case strings.TrimSpace(text) == "":
		return "", errors.New("uplink config is empty")
	case !strings.HasSuffix(text, "}\n"):
		return "", errors.New("uplink config does not end with a closing brace")
	}
	d := delim(text, "UOW_EOF_WPA")
	return apUplinkHead + d + "'\n" + text + d + "\n" + apUplinkTail, nil
}

// SyncAirPortUplink keeps the unit's wpa.conf in step with the controller's
// mesh settings (cfg is the controller's configuration). It returns one line
// per change, none when the file was already right or the mesh is off, and a
// note for the log when there is nothing to do.
func (cl *Client) SyncAirPortUplink(cfg translate.Cfg) (lines []string, note string, err error) {
	if !cl.IsAirPort() {
		return nil, "", errors.New("not an AirPort base station")
	}
	text, note, err := AirPortUplinkConf(cfg)
	if err != nil || text == "" {
		return nil, note, err
	}
	s, err := AirPortUplinkScript(text)
	if err != nil {
		return nil, "", err
	}
	out, err := cl.runStdin("sh", []byte(s))
	lines = reportLines(out)
	if err != nil {
		return lines, "", fmt.Errorf("airport uplink: %w", err)
	}
	for _, l := range lines {
		if strings.Contains(l, " failed: ") {
			return lines, "", errors.New(l)
		}
	}
	return lines, "", nil
}
