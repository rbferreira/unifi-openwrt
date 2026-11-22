// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Placeholder MACs only. The UniFi device in apNbrARP shares the middle four
// octets with every sibling BSSID below (parentDevice's match).
const apNbrSSID = "HomeNet"

var apNbrARP = map[string]string{"02:00:11:22:33:00": "192.0.2.50"}

func apNbrBSS(last string, ssid string, ch, sig int) BSS {
	freq := 2407 + 5*ch
	if ch > 14 {
		freq = 5000 + 5*ch
	}
	return BSS{BSSID: "02:00:11:22:33:" + last, SSID: ssid, Freq: freq, Channel: ch, Signal: sig}
}

func TestApOpClass(t *testing.T) {
	tests := []struct {
		freq, ch, want int
	}{
		{2412, 1, 81}, {2437, 6, 81}, {2472, 13, 81}, {2484, 14, 0}, {2407, 0, 0},
		{5160, 32, 0}, {5170, 34, 0}, {5180, 36, 115}, {5240, 48, 115},
		{5260, 52, 118}, {5320, 64, 118}, {5340, 68, 0}, {5500, 100, 121}, {5720, 144, 121},
		{5745, 149, 124}, {5805, 161, 124}, {5825, 165, 125}, {5845, 169, 0}, {5885, 177, 0},
		{5955, 1, 0}, // 6 GHz reads as 5xxx here: channel 1 has no class
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d_%d", tc.freq, tc.ch), func(t *testing.T) {
			if got := apOpClass(tc.freq, tc.ch); got != tc.want {
				t.Errorf("class %d, want %d", got, tc.want)
			}
		})
	}
}

// the class and the PHY type are what NeighborElement writes for the same BSS
func TestApNbrMatchesNeighborElement(t *testing.T) {
	for _, ch := range []int{1, 6, 13, 36, 44, 52, 100, 149, 157} {
		b := apNbrBSS("01", apNbrSSID, ch, -50)
		n, ok := apNbrFor(b)
		if !ok {
			t.Fatalf("ch %d: no entry", ch)
		}
		el, err := NeighborElement(Neighbor{BSSID: b.BSSID, Freq: b.Freq, Channel: b.Channel})
		if err != nil {
			t.Fatal(err)
		}
		// BSSID(6), info(4, little endian), class, channel, phy
		raw, err := hex.DecodeString(el)
		if err != nil || len(raw) != 13 {
			t.Fatalf("element %q: %v", el, err)
		}
		info, class, channel, phy := int(raw[6])|int(raw[7])<<8, int(raw[10]), int(raw[11]), int(raw[12])
		if n.Info != info || n.Channel != channel || n.Phy != phy {
			t.Errorf("ch %d: %+v, element info %#x channel %d phy %d", ch, n, info, channel, phy)
		}
		// classes differ only where the spec's list is finer (149..161 is 124)
		if ch != 149 && ch != 157 && n.Class != class {
			t.Errorf("ch %d: class %d, element %d", ch, n.Class, class)
		}
	}
}

func TestApNbrFor(t *testing.T) {
	n, ok := apNbrFor(apNbrBSS("0A", apNbrSSID, 6, -40))
	if !ok || n.Arg() != "02:00:11:22:33:0a,2223,81,6,7" {
		t.Errorf("2.4 GHz: %+v %v %q", n, ok, n.Arg())
	}
	n, ok = apNbrFor(apNbrBSS("0b", apNbrSSID, 44, -40))
	if !ok || n.Arg() != "02:00:11:22:33:0b,6319,115,44,9" {
		t.Errorf("5 GHz: %+v %v %q", n, ok, n.Arg())
	}
	for _, b := range []BSS{apNbrBSS("0c", apNbrSSID, 14, -40), {BSSID: "nonsense", Freq: 2437, Channel: 6}, {BSSID: "02:00:11:22:33:0d", Freq: 5000, Channel: 0}} {
		if _, ok := apNbrFor(b); ok {
			t.Errorf("entry for %+v", b)
		}
	}
}

func TestAirPortNeighbors(t *testing.T) {
	own := map[string]bool{"02:00:11:22:33:0f": true}
	ng := ScanResult{Band: "ng", BSS: []BSS{
		apNbrBSS("01", apNbrSSID, 6, -60),
		apNbrBSS("02", "Other", 6, -30),    // another SSID
		apNbrBSS("0f", apNbrSSID, 6, -20),  // the unit's own
		apNbrBSS("0F", apNbrSSID, 11, -20), // the unit's own, upper case in the scan
		{BSSID: "02:aa:bb:cc:dd:01", SSID: apNbrSSID, Freq: 2437, Channel: 6, Signal: -10}, // not a UniFi device's
		apNbrBSS("03", apNbrSSID, 14, -30),                                                 // no class
	}}
	na := ScanResult{Band: "na", BSS: []BSS{
		apNbrBSS("01", apNbrSSID, 6, -45), // seen again, stronger: kept once
		apNbrBSS("04", apNbrSSID, 44, -70),
		apNbrBSS("05", apNbrSSID, 165, -80),
	}}
	got := AirPortNeighbors([]ScanResult{ng, na}, apNbrSSID, own, apNbrARP)
	var args []string
	for _, n := range got {
		args = append(args, n.Arg())
	}
	want := []string{
		"02:00:11:22:33:01,2223,81,6,7",
		"02:00:11:22:33:04,6319,115,44,9",
		"02:00:11:22:33:05,6319,125,165,9",
	}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("got  %q\nwant %q", args, want)
	}
	if got := AirPortNeighbors(nil, apNbrSSID, own, apNbrARP); len(got) != 0 {
		t.Errorf("no scans: %v", got)
	}
	if got := AirPortNeighbors([]ScanResult{ng, na}, "Nobody", own, apNbrARP); len(got) != 0 {
		t.Errorf("unknown SSID: %v", got)
	}
	if got := AirPortNeighbors([]ScanResult{ng, na}, apNbrSSID, own, nil); len(got) != 0 {
		t.Errorf("no UniFi device known: %v", got)
	}
}

func TestAirPortNeighborsCapAndOrder(t *testing.T) {
	var bss []BSS
	for i := 0; i < 20; i++ {
		// signals -30 .. -68 (i = 0 the strongest), BSSIDs 20 down to 01
		bss = append(bss, apNbrBSS(fmt.Sprintf("%02x", 0x20-i), apNbrSSID, 1+i%11, -30-2*i))
	}
	got := AirPortNeighbors([]ScanResult{{Band: "ng", BSS: bss}}, apNbrSSID, nil, apNbrARP)
	if len(got) != apNbrMax {
		t.Fatalf("%d entries, want %d", len(got), apNbrMax)
	}
	seen := map[string]bool{}
	for i, n := range got {
		seen[n.BSSID] = true
		if i > 0 && got[i-1].BSSID >= n.BSSID {
			t.Errorf("not sorted by BSSID at %d: %s %s", i, got[i-1].BSSID, n.BSSID)
		}
	}
	// the four weakest (i = 16..19: BSSIDs 10, 0f, 0e, 0d... in hex 0x20-16 = 0x10) are out
	for i := 0; i < 20; i++ {
		b := fmt.Sprintf("02:00:11:22:33:%02x", 0x20-i)
		if (i < apNbrMax) != seen[b] {
			t.Errorf("entry %d (%s): in the list = %v", i, b, seen[b])
		}
	}
	// the same input, the same list
	again := AirPortNeighbors([]ScanResult{{Band: "ng", BSS: bss}}, apNbrSSID, nil, apNbrARP)
	if !reflect.DeepEqual(got, again) {
		t.Error("not deterministic")
	}
	// equal signals: the lower BSSID wins the last place
	tie := []BSS{}
	for i := 0; i < apNbrMax+1; i++ {
		tie = append(tie, apNbrBSS(fmt.Sprintf("%02x", i+1), apNbrSSID, 6, -50))
	}
	got = AirPortNeighbors([]ScanResult{{BSS: tie}}, apNbrSSID, nil, apNbrARP)
	if len(got) != apNbrMax || got[len(got)-1].BSSID != fmt.Sprintf("02:00:11:22:33:%02x", apNbrMax) {
		t.Errorf("tie: %d entries, last %v", len(got), got[len(got)-1])
	}
}

func apNbrSnap() *Snapshot {
	return &Snapshot{
		ARP: apNbrARP,
		Radios: []Radio{
			{Iface: "bwl1", Band: "ng", BSSID: "02:00:11:22:33:f0", VAPs: []Radio{
				{Iface: "wlan10", Mode: "Master", SSID: apNbrSSID, BSSID: "02:00:11:22:33:f1"},
				{Iface: "wlan11", Mode: "Master", SSID: "Guest", BSSID: "02:00:11:22:33:f2"},
				{Iface: "wlan9", Mode: "Master", SSID: "Apple Network", BSSID: "02:00:11:22:33:f3"}, // the firmware's
			}},
			{Iface: "bwl0", Band: "na", BSSID: "02:00:11:22:33:f4", VAPs: []Radio{
				{Iface: "wlan20", Mode: "Master", SSID: apNbrSSID, BSSID: "02:00:11:22:33:f5"},
				{Iface: "wlan0", Mode: "Client", SSID: apNbrSSID, BSSID: "02:00:11:22:33:f6"}, // the uplink
				{Iface: "wlan21", Mode: "Master", SSID: "", BSSID: "02:00:11:22:33:f7"},       // hidden, no SSID
			}},
		},
	}
}

func TestAirPortNbrSets(t *testing.T) {
	res := []ScanResult{
		{Band: "ng", BSS: []BSS{
			apNbrBSS("01", apNbrSSID, 6, -50),
			apNbrBSS("f1", apNbrSSID, 6, -10), // our own VAP, as the scan sees it
			apNbrBSS("f5", apNbrSSID, 6, -10), // our own, other radio
			apNbrBSS("f4", apNbrSSID, 6, -10), // our own radio's BSSID
			apNbrBSS("02", "Guest", 1, -60),
		}},
		{Band: "na", BSS: []BSS{apNbrBSS("03", apNbrSSID, 100, -55), apNbrBSS("f6", apNbrSSID, 36, -10)}},
	}
	sets := AirPortNbrSets(apNbrSnap(), res)
	got := map[string][]string{}
	var names []string
	for _, s := range sets {
		names = append(names, s.VAP)
		for _, n := range s.Nbrs {
			got[s.VAP] = append(got[s.VAP], n.Arg())
		}
	}
	if !reflect.DeepEqual(names, []string{"wlan10", "wlan11", "wlan20"}) {
		t.Errorf("VAPs %v", names)
	}
	both := []string{"02:00:11:22:33:01,2223,81,6,7", "02:00:11:22:33:03,6319,121,100,9"}
	if !reflect.DeepEqual(got["wlan10"], both) || !reflect.DeepEqual(got["wlan20"], both) {
		t.Errorf("same SSID on both bands: %q %q", got["wlan10"], got["wlan20"])
	}
	if !reflect.DeepEqual(got["wlan11"], []string{"02:00:11:22:33:02,2223,81,1,7"}) {
		t.Errorf("guest: %q", got["wlan11"])
	}
	if AirPortNbrSets(nil, res) != nil {
		t.Error("no reading")
	}
}

func TestAirPortNbrScript(t *testing.T) {
	a, _ := apNbrFor(apNbrBSS("0b", apNbrSSID, 44, -40))
	b, _ := apNbrFor(apNbrBSS("0a", apNbrSSID, 6, -40))
	s, err := AirPortNbrScript([]AirPortNbrSet{
		{VAP: "wlan11", Nbrs: []AirPortNbr{a, b}}, // given out of order: listed by BSSID
		{VAP: "wlan20"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "PATH=/bin:/sbin:/usr/bin:/usr/sbin\n") {
		t.Error("the PATH line is not first")
	}
	for _, want := range []string{
		"\nnbr wlan11 02:00:11:22:33:0a,2223,81,6,7 02:00:11:22:33:0b,6319,115,44,9\n",
		"\nnbr wlan20\n",
		"wl -i $v rrm_nbr_del_nbr $m",
		"wl -i $v rrm_nbr_add_nbr $m $i $c $h $p",
		"f=$D/$v.nbr",
		"D=" + apRunDir,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if !strings.HasSuffix(s, "exit 0\n") {
		t.Error("no exit 0")
	}
	for _, tool := range []string{"grep", "awk", "head", "cut"} {
		if strings.Contains(s, " "+tool+" ") || strings.Contains(s, "	"+tool+" ") {
			t.Errorf("the script uses %s, which the device lacks", tool)
		}
	}
	// the files the neighbour lists need are dropped whenever a VAP is stopped or started
	ap := AirPortApplyScript(nil)
	if !strings.Contains(ap, "$D/$1.acl $D/$1.pfq $D/$1.nbr") || !strings.Contains(ap, "$D/$v.acl $D/$v.pfq $D/$v.nbr") {
		t.Error("stop/start do not remove the .nbr file")
	}
}

func TestAirPortNbrScriptRefuses(t *testing.T) {
	ok := AirPortNbr{BSSID: "02:00:11:22:33:0a", Info: 2223, Class: 81, Channel: 6, Phy: 7}
	with := func(f func(n *AirPortNbr)) AirPortNbr { n := ok; f(&n); return n }
	tests := []struct {
		name string
		sets []AirPortNbrSet
	}{
		{"VAP outside the ranges", []AirPortNbrSet{{VAP: "wlan9"}}},
		{"VAP, the uplink's", []AirPortNbrSet{{VAP: "wlan0"}}},
		{"VAP with a command", []AirPortNbrSet{{VAP: "wlan10; reboot"}}},
		{"VAP twice", []AirPortNbrSet{{VAP: "wlan10"}, {VAP: "wlan10"}}},
		{"semicolon", []AirPortNbrSet{{VAP: "wlan10", Nbrs: []AirPortNbr{with(func(n *AirPortNbr) { n.BSSID = "02:00:11:22:33:0a;reboot" })}}}},
		{"command substitution", []AirPortNbrSet{{VAP: "wlan10", Nbrs: []AirPortNbr{with(func(n *AirPortNbr) { n.BSSID = "$(reboot)" })}}}},
		{"backtick", []AirPortNbrSet{{VAP: "wlan10", Nbrs: []AirPortNbr{with(func(n *AirPortNbr) { n.BSSID = "`reboot`" })}}}},
		{"newline", []AirPortNbrSet{{VAP: "wlan10", Nbrs: []AirPortNbr{with(func(n *AirPortNbr) { n.BSSID = "02:00:11:22:33:0a\nreboot" })}}}},
		{"space", []AirPortNbrSet{{VAP: "wlan10", Nbrs: []AirPortNbr{with(func(n *AirPortNbr) { n.BSSID = "02:00:11:22:33:0a 02:00:11:22:33:0b" })}}}},
		{"upper case", []AirPortNbrSet{{VAP: "wlan10", Nbrs: []AirPortNbr{with(func(n *AirPortNbr) { n.BSSID = "02:00:11:22:33:0A" })}}}},
		{"short MAC", []AirPortNbrSet{{VAP: "wlan10", Nbrs: []AirPortNbr{with(func(n *AirPortNbr) { n.BSSID = "02:00:11:22:33" })}}}},
		{"class 0", []AirPortNbrSet{{VAP: "wlan10", Nbrs: []AirPortNbr{with(func(n *AirPortNbr) { n.Class = 0 })}}}},
		{"channel 300", []AirPortNbrSet{{VAP: "wlan10", Nbrs: []AirPortNbr{with(func(n *AirPortNbr) { n.Channel = 300 })}}}},
		{"negative info", []AirPortNbrSet{{VAP: "wlan10", Nbrs: []AirPortNbr{with(func(n *AirPortNbr) { n.Info = -1 })}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if s, err := AirPortNbrScript(tc.sets); err == nil {
				t.Errorf("script made:\n%s", s)
			}
		})
	}
	if _, err := AirPortNbrScript([]AirPortNbrSet{{VAP: "wlan10", Nbrs: []AirPortNbr{ok}}}); err != nil {
		t.Errorf("a good entry: %v", err)
	}
}

func TestAirPortNbrScriptSyntax(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host")
	}
	n, _ := apNbrFor(apNbrBSS("0a", apNbrSSID, 6, -40))
	for name, sets := range map[string][]AirPortNbrSet{
		"entries": {{VAP: "wlan10", Nbrs: []AirPortNbr{n}}, {VAP: "wlan20"}},
		"none":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			s, err := AirPortNbrScript(sets)
			if err != nil {
				t.Fatal(err)
			}
			f := filepath.Join(t.TempDir(), "s.sh")
			if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
				t.Errorf("sh -n: %v\n%s", err, out)
			}
		})
	}
}

// ---- the script, run against the stub device (see airport_apply_test.go) ----

func (h *apStubHost) runNbr(sets []AirPortNbrSet) []string {
	h.t.Helper()
	s, err := AirPortNbrScript(sets)
	if err != nil {
		h.t.Fatal(err)
	}
	s = strings.ReplaceAll(s, "PATH=/bin:/sbin:/usr/bin:/usr/sbin", "PATH=$PATH")
	s = strings.ReplaceAll(s, apRunDir, h.dir+"/run")
	return h.runScript(s)
}

func apNbrSet(vap string, lasts ...string) AirPortNbrSet {
	s := AirPortNbrSet{VAP: vap}
	for _, l := range lasts {
		ch := 6
		if l >= "80" {
			ch = 44
		}
		n, _ := apNbrFor(apNbrBSS(l, apNbrSSID, ch, -50))
		s.Nbrs = append(s.Nbrs, n)
	}
	return s
}

func TestAirPortNbrScriptRunsAgainstStubs(t *testing.T) {
	h := newAPStubHost(t)
	vaps := apFourVAPs(t, apNbrSSID, apNbrSSID) // wlan10 wlan20 wlan11 wlan21
	h.apply(vaps)
	nbr := func() []string { return h.read("run/wlan10.nbr") }
	const (
		a = "02:00:11:22:33:0a"
		b = "02:00:11:22:33:0b"
		c = "02:00:11:22:33:81"
	)

	// first pass: the entries are added, one line per VAP changed; wlan12 does
	// not run and is skipped without a line or a file
	n := len(h.ctl())
	lines := h.runNbr([]AirPortNbrSet{apNbrSet("wlan10", "0a", "0b"), apNbrSet("wlan12", "0a"), apNbrSet("wlan20", "0a")})
	apWant(t, "first pass", lines, []string{"wlan10 neighbours: 2", "wlan20 neighbours: 1"})
	apWant(t, "first pass calls", h.ctl()[n:], []string{
		"wlan10 rrm_nbr_add_nbr " + a + " 2223 81 6 7",
		"wlan10 rrm_nbr_add_nbr " + b + " 2223 81 6 7",
		"wlan20 rrm_nbr_add_nbr " + a + " 2223 81 6 7",
	})
	apWant(t, "kept text", nbr(), []string{a + ",2223,81,6,7 " + b + ",2223,81,6,7"})
	if _, err := os.Stat(filepath.Join(h.dir, "run", "wlan12.nbr")); err == nil {
		t.Error("a file for a VAP that does not run")
	}

	// the same again: nothing runs
	n = len(h.ctl())
	if lines := h.runNbr([]AirPortNbrSet{apNbrSet("wlan10", "0b", "0a"), apNbrSet("wlan20", "0a")}); len(lines) != 0 {
		t.Errorf("same lists, said %q", lines)
	}
	if got := h.ctl()[n:]; len(got) != 0 {
		t.Errorf("same lists, the driver was called: %q", got)
	}

	// a change: the old entries go, the new ones come
	n = len(h.ctl())
	lines = h.runNbr([]AirPortNbrSet{apNbrSet("wlan10", "0b", "81"), apNbrSet("wlan20", "0a")})
	apWant(t, "change", lines, []string{"wlan10 neighbours: 2"})
	apWant(t, "change calls", h.ctl()[n:], []string{
		"wlan10 rrm_nbr_del_nbr " + a,
		"wlan10 rrm_nbr_del_nbr " + b,
		"wlan10 rrm_nbr_add_nbr " + b + " 2223 81 6 7",
		"wlan10 rrm_nbr_add_nbr " + c + " 6319 115 44 9",
	})

	// the list ends empty: everything is deleted, the line says 0
	n = len(h.ctl())
	lines = h.runNbr([]AirPortNbrSet{apNbrSet("wlan10"), apNbrSet("wlan20", "0a")})
	apWant(t, "empty", lines, []string{"wlan10 neighbours: 0"})
	apWant(t, "empty calls", h.ctl()[n:], []string{"wlan10 rrm_nbr_del_nbr " + b, "wlan10 rrm_nbr_del_nbr " + c})
	apWant(t, "kept text, empty", nbr(), []string{""})

	// a VAP made again (its file is gone, its list empty): the list is filled
	// again; with nothing to list there is nothing to say
	os.Remove(filepath.Join(h.dir, "run", "wlan20.nbr"))
	n = len(h.ctl())
	lines = h.runNbr([]AirPortNbrSet{apNbrSet("wlan10"), apNbrSet("wlan20", "0a")})
	apWant(t, "recreated", lines, []string{"wlan20 neighbours: 1"})
	apWant(t, "recreated calls", h.ctl()[n:], []string{"wlan20 rrm_nbr_add_nbr " + a + " 2223 81 6 7"})
	os.Remove(filepath.Join(h.dir, "run", "wlan10.nbr"))
	n = len(h.ctl())
	if lines := h.runNbr([]AirPortNbrSet{apNbrSet("wlan10")}); len(lines) != 0 || len(h.ctl()[n:]) != 0 {
		t.Errorf("recreated, empty: %q %q", lines, h.ctl()[n:])
	}
	if _, err := os.Stat(filepath.Join(h.dir, "run", "wlan10.nbr")); err != nil {
		t.Errorf("recreated, empty: no file kept: %v", err)
	}

	// the driver refuses an entry: it is reported, left out of the file and
	// tried again on the next pass
	if err := os.WriteFile(filepath.Join(h.dir, "state", "fail", "nbr"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	lines = h.runNbr([]AirPortNbrSet{apNbrSet("wlan10", "0a")})
	apWant(t, "refused", lines, []string{"wlan10 nbr: add " + a + " refused", "wlan10 neighbours: 0"})
	os.Remove(filepath.Join(h.dir, "state", "fail", "nbr"))
	lines = h.runNbr([]AirPortNbrSet{apNbrSet("wlan10", "0a")})
	apWant(t, "retried", lines, []string{"wlan10 neighbours: 1"})

	// stopping or starting a VAP drops its file, so it is filled when it comes
	// back; one that stays as it is keeps it
	h.runNbr([]AirPortNbrSet{apNbrSet("wlan11", "0a")})
	if len(h.read("run/wlan11.nbr")) == 0 || len(h.read("run/wlan20.nbr")) == 0 {
		t.Fatal("no files to lose")
	}
	apWant(t, "apply", h.apply(vaps[1:]), []string{"wlan10 removed", "wlan11 started", "wlan20 same", "wlan21 same"})
	for v, want := range map[string]bool{"wlan10": false, "wlan11": false, "wlan20": true} {
		if _, err := os.Stat(filepath.Join(h.dir, "run", v+".nbr")); (err == nil) != want {
			t.Errorf("%s.nbr present = %v, want %v", v, err == nil, want)
		}
	}
}
