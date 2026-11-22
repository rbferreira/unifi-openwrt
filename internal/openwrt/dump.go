// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Dump reads the AP once and returns the reading with everything personal
// replaced (see Scrub): a file a user can attach to a bug report, and that
// Parse reads like the original.
func (cl *Client) Dump() ([]byte, error) {
	if cl.IsAirPort() {
		out, err := cl.run(airportScript)
		if err != nil {
			return nil, err
		}
		return AirPortScrub(out), nil // read it back with ParseAirPort
	}
	out, err := cl.Run(collectScript)
	if err != nil {
		return nil, err
	}
	return Scrub(out), nil
}

// dumpKeep lists, per section, the JSON keys whose text is kept. A key is
// named alone or with its parents ("port.id.value"). Text under any other
// key becomes "REDACTED", so a field this list does not know (a Wi-Fi
// password, a note, a name) never leaves the AP. Numbers and booleans stay.
var dumpKeep = map[string][]string{
	"board": {"kernel", "system", "model", "board_name", "rootfs_type", "distribution", "version",
		"firmware_url", "revision", "target", "description", "builddate"},
	"iface": {"phy", "mode", "country", "htmode", "htmodes", "hwmode", "hwmodes", "hwmodes_text",
		"hardware.name", "authentication", "ciphers", "wpa"},
	"assoc": {"preamble", "mesh plink", "mesh local PS", "mesh peer PS", "mesh non-peer PS"},
	"wstatus": {"band", "channel", "country", "device", "encryption", "htmode", "ifname", "mode",
		"network", "path", "section", "type"},
	"lldp": {"interface.name", "via", "rid", "age", "id.type", "port.id.value", "capability.type",
		"vlan-id", "ttl.value", "device-type.value"},
}

// dumpJSON: the sections that hold JSON. One that does not parse is dropped
// whole, because free text cannot be scrubbed safely.
var dumpJSON = map[string]bool{"board": true, "info": true, "iface": true, "assoc": true,
	"txpw": true, "wstatus": true, "lldp": true}

var (
	macRE  = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(:[0-9a-f]{2}){5}\b`)
	ipv4RE = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	hex12  = regexp.MustCompile(`^[0-9a-f]{12}$`)
)

// scrubber replaces each MAC, IPv4 address and SSID by a made-up one, the
// same one every time, so what belongs together in the reading (a station
// in the ARP table and in the forwarding table) still does.
type scrubber struct {
	mac, ip, ssid map[string]string
}

// Scrub removes what is personal from collectScript output: MAC and IP
// addresses and SSIDs are replaced, the hostname becomes "OpenWrt", and
// every other text that is not on the dumpKeep list is redacted.
func Scrub(out []byte) []byte {
	s := &scrubber{mac: map[string]string{}, ip: map[string]string{}, ssid: map[string]string{}}
	secs := sections(out)
	var b bytes.Buffer
	for _, name := range secs.order {
		// the header: "iface <interface> <phy>", "port <name>", ...
		fmt.Fprintf(&b, "=== %s\n", s.text(name))
		kind := strings.Fields(name + " x")[0]
		body := secs.get(name)
		switch {
		case dumpJSON[kind]:
			b.Write(s.json(kind, body))
		case kind == "fdb":
			b.WriteString(s.fdb(string(body)))
		default:
			b.WriteString(s.text(string(body)))
		}
	}
	return b.Bytes()
}

func (s *scrubber) newMAC(m string) string {
	key := strings.ToLower(m)
	if key == "00:00:00:00:00:00" || key == "ff:ff:ff:ff:ff:ff" {
		return m
	}
	v, ok := s.mac[key]
	if !ok {
		n := len(s.mac) + 1
		v = fmt.Sprintf("02:00:00:00:%02x:%02x", n>>8&0xff, n&0xff)
		s.mac[key] = v
	}
	if m != key {
		return strings.ToUpper(v)
	}
	return v
}

func (s *scrubber) newIP(ip string) string {
	if strings.HasPrefix(ip, "0.") || strings.HasPrefix(ip, "127.") || strings.HasPrefix(ip, "255.") {
		return ip // any-address, loopback, netmask
	}
	v, ok := s.ip[ip]
	if !ok {
		n := len(s.ip)
		v = fmt.Sprintf("192.168.%d.%d", 1+n/254, 1+n%254)
		s.ip[ip] = v
	}
	return v
}

// text replaces the MAC and IPv4 addresses in free text.
func (s *scrubber) text(t string) string {
	t = macRE.ReplaceAllStringFunc(t, s.newMAC)
	return ipv4RE.ReplaceAllStringFunc(t, s.newIP)
}

// fdb: "aabbccddeeff port local age hi" per line, the MAC without colons.
func (s *scrubber) fdb(t string) string {
	lines := strings.Split(t, "\n")
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) == 0 || !hex12.MatchString(f[0]) {
			if line != "" {
				lines[i] = "REDACTED"
			}
			continue
		}
		var m []string
		for j := 0; j < 12; j += 2 {
			m = append(m, f[0][j:j+2])
		}
		f[0] = strings.ReplaceAll(s.newMAC(strings.Join(m, ":")), ":", "")
		lines[i] = strings.Join(f, " ")
	}
	return strings.Join(lines, "\n")
}

func (s *scrubber) json(kind string, body []byte) []byte {
	if len(bytes.TrimSpace(body)) == 0 {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return []byte("REDACTED (not JSON)\n")
	}
	out, err := json.MarshalIndent(s.walk(kind, "", v), "", "\t")
	if err != nil {
		return []byte("REDACTED (not JSON)\n")
	}
	return append(out, '\n')
}

func (s *scrubber) walk(kind, path string, v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[s.text(k)] = s.walk(kind, path+"."+k, e)
		}
		return out
	case []any:
		for i, e := range x {
			x[i] = s.walk(kind, path, e)
		}
		return x
	case string:
		return s.str(kind, path, x)
	}
	return v // numbers, booleans, null
}

func (s *scrubber) str(kind, path, v string) string {
	if v == "" || macRE.FindString(v) == v {
		return s.text(v)
	}
	switch {
	case strings.HasSuffix(path, ".ssid"):
		n, ok := s.ssid[v]
		if !ok {
			n = fmt.Sprintf("ssid%d", len(s.ssid)+1)
			s.ssid[v] = n
		}
		return n
	case kind == "board" && path == ".hostname":
		return "OpenWrt"
	}
	for _, k := range dumpKeep[kind] {
		if strings.HasSuffix(path, "."+k) {
			return s.text(v)
		}
	}
	return "REDACTED"
}
