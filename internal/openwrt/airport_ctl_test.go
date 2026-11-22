// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// Placeholder MACs only.
const (
	macA = "02:00:00:00:00:0a"
	macB = "02:00:00:00:00:0b"
	macC = "02:00:00:00:00:0c"
)

func TestWantedACL(t *testing.T) {
	tests := []struct {
		name          string
		policy        string
		macs, blocked []string
		mode          string
		want          []string
	}{
		{"disable, nothing blocked", "disable", nil, nil, "open", nil},
		{"disable ignores a stray list", "disable", []string{macA}, nil, "open", nil},
		{"disable, blocked", "disable", nil, []string{macB}, "deny", []string{macB}},
		{"empty policy is disable", "", nil, []string{macB}, "deny", []string{macB}},
		{"deny list alone", "deny", []string{macA}, nil, "deny", []string{macA}},
		{"deny list plus blocked, merged and sorted", "deny", []string{macB, macA}, []string{macC, macA}, "deny", []string{macA, macB, macC}},
		{"deny, upper case and spaces", "deny", []string{" 02:00:00:00:00:0A "}, nil, "deny", []string{macA}},
		{"deny with nothing valid is open", "deny", []string{"nonsense"}, nil, "open", nil},
		{"allow list alone", "allow", []string{macB, macA}, nil, "allow", []string{macA, macB}},
		{"allow minus blocked", "allow", []string{macA, macB}, []string{macB}, "allow", []string{macA}},
		{"allow, all blocked: nobody", "allow", []string{macA}, []string{macA}, "allow", nil},
		{"allow, empty list: nobody", "allow", nil, nil, "allow", nil},
		{"allow, blocked outside the list changes nothing", "allow", []string{macA}, []string{macC}, "allow", []string{macA}},
		{"blocked junk is ignored", "disable", nil, []string{"x; reboot"}, "open", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := WantedACL(tc.policy, tc.macs, tc.blocked)
			if got.Mode != tc.mode || !reflect.DeepEqual(got.MACs, tc.want) && !(len(got.MACs) == 0 && len(tc.want) == 0) {
				t.Errorf("got %+v, want %s %v", got, tc.mode, tc.want)
			}
		})
	}
	if got := WantedACL("deny", []string{macB, macA}, nil).Text(); got != "deny "+macA+" "+macB {
		t.Errorf("Text %q", got)
	}
	if got := WantedACL("disable", nil, nil).Text(); got != "open" {
		t.Errorf("Text %q", got)
	}
}

func TestCleanMACRefusesShellMetacharacters(t *testing.T) {
	for _, m := range []string{
		"", "02:00:00:00:00", "02:00:00:00:00:0a:0b", "02:00:00:00:00:0g",
		"02:00:00:00:00:0a; reboot", "02:00:00:00:00:0a\nreboot", "$(reboot)", "`reboot`",
		"02:00:00:00:00:0a && x", "02:00:00:00:00:0a|x", "02:00:00:00:00:0a'", "02-00-00-00-00-0a", "0200.0000.000a",
	} {
		if c, ok := cleanMAC(m); ok {
			t.Errorf("accepted %q as %q", m, c)
		}
		if _, err := airPortKickScript(m); err == nil {
			t.Errorf("kick script built for %q", m)
		}
	}
	if c, ok := cleanMAC(" 02:00:00:00:00:0A\n"); !ok || c != macA {
		t.Errorf("clean MAC: %q %v", c, ok)
	}
}

func TestAirPortACLScript(t *testing.T) {
	vaps := []AirPortVAP{
		{Name: "wlan10", Parent: "bwl1", Head: "a=b\n", Tail: "c=d\n", MACPolicy: "deny", MACs: []string{macA}},
		{Name: "wlan11", Parent: "bwl1", Head: "a=b\n", Tail: "c=d\n", MACPolicy: "allow", MACs: []string{macB, macC}},
		{Name: "wlan20", Parent: "bwl0", Head: "a=b\n", Tail: "c=d\n", MACPolicy: "disable"},
	}
	s, err := AirPortACLScript(vaps, []string{macC})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\nacl wlan10 deny " + macA + " " + macC + "\n",
		"\nacl wlan11 allow " + macB + "\n",
		"\nacl wlan20 deny " + macC + "\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	s2, _ := AirPortACLScript(vaps, nil)
	if !strings.Contains(s2, "\nacl wlan20 open\n") {
		t.Error("an empty list must be open mode")
	}
	for _, x := range []string{s, s2} {
		if strings.Contains(x, "wlan9") {
			t.Error("script mentions wlan9")
		}
		if !strings.HasPrefix(x, "PATH=/bin:/sbin:/usr/bin:/usr/sbin\n") {
			t.Error("PATH not set first")
		}
	}
	// nothing but /mnt/Memory/uowap is written
	if strings.Count(s, "/mnt/") != 1 || !strings.Contains(s, "D="+apRunDir+"\n") {
		t.Error("script writes outside the run directory")
	}
	// refusals
	for _, bad := range []string{"wlan9", "wlan18", "wlan28", "wlan10; reboot", ""} {
		if _, err := AirPortACLScript([]AirPortVAP{{Name: bad, Parent: "bwl1"}}, nil); err == nil {
			t.Errorf("accepted VAP %q", bad)
		}
	}
}

func TestAirPortScriptsSyntax(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host")
	}
	acl, _ := AirPortACLScript([]AirPortVAP{
		{Name: "wlan10", Parent: "bwl1", MACPolicy: "deny", MACs: []string{macA}},
		{Name: "wlan20", Parent: "bwl0", MACPolicy: "allow"},
	}, []string{macB})
	kick, _ := airPortKickScript(macA)
	w := apWantedSlots(6, 44, 2, 2)
	w.Radios[0].TxPower, w.Radios[1].TxPower = 14, 20
	w.WLANs[0].Isolate = true
	vaps, _ := AirPortVAPs(w, 40)
	for name, s := range map[string]string{
		"acl":     acl,
		"kick":    kick,
		"apply":   AirPortApplyScript(vaps),
		"collect": airportScript,
	} {
		t.Run(name, func(t *testing.T) {
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

func TestAirPortKickScript(t *testing.T) {
	s, err := airPortKickScript("02:00:00:00:00:0A")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "wl -i $v deauthenticate "+macA+" ") || !strings.Contains(s, "case $v in wlan1[0-7]|wlan2[0-7])") {
		t.Errorf("script:\n%s", s)
	}
	if strings.Contains(s, "wlan9") || !strings.HasPrefix(s, "PATH=/bin:/sbin:/usr/bin:/usr/sbin\n") {
		t.Errorf("script:\n%s", s)
	}
}

func TestKickOnAnAirPortUsesTheKickScript(t *testing.T) {
	// a non-MAC must be refused before any shell runs
	cl := NewClient(Target{Addr: "local", Platform: PlatformAirPort})
	if err := cl.Kick("x; reboot"); err == nil {
		t.Error("a bad MAC reached the shell")
	}
	if _, err := NewClient(Target{Addr: "local"}).SyncAirPortACL([]AirPortVAP{{Name: "wlan10", Parent: "bwl1"}}, nil); err == nil {
		t.Error("an OpenWrt client must refuse the ACL sync")
	}
}

func apOne(mod func(*translate.WantedWLAN, *translate.WantedRadio)) ([]AirPortVAP, []string) {
	radios := apRadios()
	l := apWLAN("1", "wifi0")
	mod(&l, &radios[0])
	return AirPortVAPs(translate.Wanted{Radios: radios, WLANs: []translate.WantedWLAN{l}}, 0)
}

func TestAirPortVAPCarriesMACFilterIsolationAndPower(t *testing.T) {
	vaps, notes := apOne(func(l *translate.WantedWLAN, r *translate.WantedRadio) {
		l.MACPolicy, l.MACs = "allow", []string{"02:00:00:00:00:0B", macA, macA, "bogus"}
		l.Isolate = true
		r.TxPower = 14
	})
	if len(vaps) != 1 {
		t.Fatal(notes)
	}
	v := vaps[0]
	if v.MACPolicy != "allow" || !reflect.DeepEqual(v.MACs, []string{macA, macB}) || !v.Isolate || v.TxPower != 14 {
		t.Errorf("VAP %+v", v)
	}
	if !apNotesHave(notes, "not a MAC address") {
		t.Errorf("no note for the bad MAC: %q", notes)
	}
	for _, n := range notes {
		if strings.Contains(n, "not supported on this platform") && (strings.Contains(n, "isolation") || strings.Contains(n, "MAC filter")) {
			t.Errorf("old note: %q", n)
		}
	}
	// none of it is in the hostapd config: changing it never restarts a radio
	plain, _ := apOne(func(*translate.WantedWLAN, *translate.WantedRadio) {})
	if v.Head != plain[0].Head || v.Tail != plain[0].Tail {
		t.Error("MAC filter, isolation or power changed the hostapd config")
	}
	vaps, notes = apOne(func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.MACPolicy = "allow" })
	if !apNotesHave(notes, "allows nobody") {
		t.Errorf("empty allow list not noted: %q", notes)
	}
}

func TestAirPortDTIMAndMaxClients(t *testing.T) {
	tests := []struct {
		name      string
		dtim, max int
		wantDTIM  string
		wantMax   string
		wantNote  bool
	}{
		{"defaults", 0, 0, "dtim=3", "max_clients=64", false},
		{"values", 5, 20, "dtim=5", "max_clients=20", false},
		{"dtim 255", 255, 1, "dtim=255", "max_clients=1", false},
		{"dtim out of range", 300, 0, "dtim=3", "max_clients=64", false},
		{"limit as large as the driver's", 1, 64, "dtim=1", "max_clients=64", false},
		{"limit above the driver's", 1, 200, "dtim=1", "max_clients=64", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vaps, notes := apOne(func(l *translate.WantedWLAN, _ *translate.WantedRadio) { l.DTIM, l.MaxClients = tc.dtim, tc.max })
			if len(vaps) != 1 {
				t.Fatal(notes)
			}
			cfg := vaps[0].Config("145", "76")
			if !apHasLine(cfg, tc.wantDTIM) || !apHasLine(cfg, tc.wantMax) {
				t.Errorf("want %s and %s in\n%s", tc.wantDTIM, tc.wantMax, cfg)
			}
			if apNotesHave(notes, "client limit above") != tc.wantNote {
				t.Errorf("notes %q", notes)
			}
			// the key order of the design is kept
			ls := apLines(cfg)
			pos := func(k string) int {
				for i, l := range ls {
					if strings.HasPrefix(l, k+"=") {
						return i
					}
				}
				return -1
			}
			if !(pos("doth") < pos("dtim") && pos("dtim") < pos("mcast_rate") && pos("tx_power") < pos("max_clients")) {
				t.Errorf("key order changed:\n%s", cfg)
			}
		})
	}
}

func TestAirPortApplyScriptIsolationAndPower(t *testing.T) {
	w := apWantedSlots(6, 44, 2, 2)
	w.Radios[0].TxPower = 14 // 2.4 GHz
	// 5 GHz left on auto; wlan11 is isolated
	w.WLANs[2].Isolate = true // a1 = wlan11
	vaps, _ := AirPortVAPs(w, 40)
	s := AirPortApplyScript(vaps)
	i := strings.Index(s, "\nradio bwl0")
	for _, want := range []string{"\niso wlan10 apbridge\n", "\niso wlan11 -apbridge\n", "\niso wlan20 apbridge\n", "\niso wlan21 apbridge\n",
		"\ntxpow bwl1 wlan10 14\n", "\ntxpow bwl0 wlan20 auto\n"} {
		j := strings.Index(s, want)
		if j < 0 {
			t.Errorf("script lacks %q", want)
		} else if j < i {
			t.Errorf("%q comes before the radio calls", want)
		}
	}
	if strings.Count(s, "\ntxpow ") != 2 {
		t.Error("one txpow per radio")
	}
	// the uplink's radio takes a power too
	w.Radios[1].TxPower = 20
	vaps, notes := AirPortVAPs(w, 40)
	if !strings.Contains(AirPortApplyScript(vaps), "\ntxpow bwl0 wlan20 20\n") {
		t.Error("5 GHz power with an uplink not applied")
	}
	if apNotesHave(notes, "transmit power not applied") {
		t.Errorf("notes %q", notes)
	}
	// the files the ACL needs are dropped whenever a VAP is stopped or started
	if !strings.Contains(s, "rm -f $D/$1.pid $D/$1.acl") || !strings.Contains(s, "rm -f $D/$v.acl") {
		t.Error("stop/start do not remove the .acl file")
	}
	if strings.Contains(s, "wlan9") {
		t.Error("wlan9 in the script")
	}
}

func TestParseStaInfo(t *testing.T) {
	// as printed one item per line (the shape of airport_test.go)
	multi := "\t idle 2 seconds\n\t in network 61 seconds\n\t state: AUTHENTICATED ASSOCIATED AUTHORIZED\n" +
		"\t flags 0x1e03b: BRCM WME N_CAP AMPDU\n\t tx data pkts: 40\n\t tx data bytes: 5000\n\t tx failures: 1\n" +
		"\t rx data pkts: 50\n\t rx data bytes: 7000\n\t rate of last tx pkt: 72222 kbps\n\t rate of last rx pkt: 65000 kbps\n"
	// as gaps-device.md section J shows it, several items on a line
	joined := ` aid:1 / rateset [ 1 2 5.5 6 9 11 12 18 24 36 48 54 ] / idle 0 seconds / in network 15 seconds
 state: AUTHENTICATED ASSOCIATED AUTHORIZED / flags 0xe03a: WME N_CAP AMPDU / HT caps 0x9ad: LDPC SGI20 STBC-Tx STBC-Rx
 tx data pkts: 2708   tx data bytes: 1174264   tx ucast pkts/bytes: 2196 / 1077429   tx mcast/bcast pkts/bytes: 512 / 96835
 tx failures: 3
 rx data pkts: 484    rx data bytes: 167275    rx ucast pkts/bytes: 328 / 122976    rx mcast/bcast pkts/bytes: 156 / 44299
 rate of last tx pkt: 144444 kbps   rate of last rx pkt: 6500 kbps
 rx decrypt succeeds: 2047   rx decrypt failures: 0
`
	tests := []struct {
		name, body string
		want       Station
	}{
		{"one item per line", multi, Station{Inactive: 2000, Connected: 61, Authorized: true, HT: true,
			TxPackets: 40, TxBytes: 5000, TxFailed: 1, RxPackets: 50, RxBytes: 7000, TxRate: 72222, RxRate: 65000}},
		{"several items per line", joined, Station{Inactive: 0, Connected: 15, Authorized: true, HT: true,
			TxPackets: 2708, TxBytes: 1174264, TxFailed: 3, RxPackets: 484, RxBytes: 167275, TxRate: 144444, RxRate: 6500}},
		{"not authorized", "\t state: AUTHENTICATED ASSOCIATED\n", Station{}},
		{"empty", "", Station{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var st Station
			parseStaInfo(&st, tc.body, false)
			if st != tc.want {
				t.Errorf("got  %+v\nwant %+v", st, tc.want)
			}
		})
	}
	var st Station
	parseStaInfo(&st, "\t state: AUTHENTICATED ASSOCIATED\n", true)
	if !st.Authorized {
		t.Error("the uplink's parent is authorized by definition")
	}
}

func TestAirportScriptBoundsStaInfo(t *testing.T) {
	if !strings.Contains(airportScript, "[ $n -ge 64 ] && continue") || !strings.Contains(airportScript, "case $i in wlan1[0-7]|wlan2[0-7])") {
		t.Error("the collect script does not bound sta_info for the daemon's VAPs")
	}
	// a station past the bound is listed without a body: still associated
	s := apParse(t, apParseBase, apRadioSections, apAP24, "=== sta wlan10\n=== assoc wlan10\n--- 02:00:00:00:00:0A\n")
	if len(s.Radios[0].VAPs) != 1 || len(s.Radios[0].VAPs[0].Stations) != 1 || !s.Radios[0].VAPs[0].Stations[0].Authorized {
		t.Errorf("stations: %+v", s.Radios[0].VAPs)
	}
}

// ---- the scripts, run against the stub device (see airport_apply_test.go) ----

func (h *apStubHost) runACL(vaps []AirPortVAP, blocked []string) []string {
	h.t.Helper()
	s, err := AirPortACLScript(vaps, blocked)
	if err != nil {
		h.t.Fatal(err)
	}
	s = strings.ReplaceAll(s, "PATH=/bin:/sbin:/usr/bin:/usr/sbin", "PATH=$PATH")
	s = strings.ReplaceAll(s, apRunDir, h.dir+"/run")
	file := filepath.Join(h.dir, "acl.sh")
	if err := os.WriteFile(file, []byte(s), 0o600); err != nil {
		h.t.Fatal(err)
	}
	cmd := exec.Command(h.sh, "acl.sh")
	cmd.Dir, cmd.Env = h.dir, h.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("acl script: %v\n%s", err, out)
	}
	return reportLines(out)
}

// ctl is what the stubs recorded of the calls that are not create/destroy.
func (h *apStubHost) ctl() []string {
	var out []string
	for _, l := range h.read("state/ctl") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestAirPortScriptsRunAgainstStubs(t *testing.T) {
	t.Run("isolation and transmit power", func(t *testing.T) {
		h := newAPStubHost(t)
		mk := func(power int, isolate bool) []AirPortVAP {
			w := apWantedSlots(6, 44, 1, 1)
			w.Radios[0].TxPower = power
			w.WLANs[0].Isolate = isolate
			vaps, _ := AirPortVAPs(w, 0)
			return vaps
		}
		h.apply(mk(14, true))
		ctl := h.ctl()
		for _, want := range []string{"wlan10 -apbridge", "wlan20 apbridge", "txpower bwl1 14"} {
			if !contains(ctl, want) {
				t.Errorf("ctl lacks %q: %q", want, ctl)
			}
		}
		if got := h.read("run/bwl1.txpower0"); len(got) != 1 || got[0] != "18.5" {
			t.Errorf("original power recorded as %q", got)
		}
		if contains(ctl, "txpower bwl0 0") || containsPrefix(ctl, "txpower bwl0") {
			t.Errorf("a radio on auto that was never changed was touched: %q", ctl)
		}
		// same again: the isolation is set again, the power is not
		n := len(h.ctl())
		h.apply(mk(14, true))
		for _, l := range h.ctl()[n:] {
			if strings.HasPrefix(l, "txpower") {
				t.Errorf("power set again: %q", l)
			}
		}
		// more than the original: cut to it
		n = len(h.ctl())
		h.apply(mk(30, false))
		got := h.ctl()[n:]
		if !contains(got, "txpower bwl1 18.5") || !contains(got, "wlan10 apbridge") {
			t.Errorf("clamp or isolation off: %q", got)
		}
		// down again, then auto: the original comes back
		h.apply(mk(10, false))
		n = len(h.ctl())
		h.apply(mk(0, false))
		if got := h.ctl()[n:]; !contains(got, "txpower bwl1 18.5") {
			t.Errorf("auto did not restore the original: %q", got)
		}
	})

	t.Run("driver ACL: block, unblock, allow list, restart", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps := apFourVAPs(t, apTestSSID, apTestSSID) // wlan10 wlan20 wlan11 wlan21
		byName := map[string]*AirPortVAP{}
		for i := range vaps {
			byName[vaps[i].Name] = &vaps[i]
		}
		byName["wlan10"].MACPolicy, byName["wlan10"].MACs = "deny", []string{macA}
		byName["wlan11"].MACPolicy, byName["wlan11"].MACs = "allow", []string{macB}
		h.apply(vaps)
		// stations on the allow-list WLAN: one listed, one not
		if err := os.WriteFile(filepath.Join(h.dir, "state", "assoc", "wlan11"),
			[]byte("assoclist 02:00:00:00:00:0B\nassoclist 02:00:00:00:00:0C\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		n := len(h.ctl())
		lines := h.runACL(vaps, nil)
		got := h.ctl()[n:]
		for _, want := range []string{
			"wlan10 mac:flush", "wlan10 mac:add " + macA, "wlan10 mac:deny", "wlan10 mac:kick " + macA,
			"wlan11 mac:flush", "wlan11 mac:add " + macB, "wlan11 mac:allow", "wlan11 mac:kick " + macC,
			"wlan20 mac:flush", "wlan20 mac:open",
		} {
			if !contains(got, want) {
				t.Errorf("ctl lacks %q: %q", want, got)
			}
		}
		if contains(got, "wlan11 mac:kick "+macB) || containsPrefix(got, "wlan20 mac:kick") {
			t.Errorf("kicked someone who may stay: %q", got)
		}
		if len(lines) != 4 {
			t.Errorf("report %q", lines)
		}
		// nothing changed: nothing is run
		n = len(h.ctl())
		if lines := h.runACL(vaps, nil); len(lines) != 0 || len(h.ctl()) != n {
			t.Errorf("second pass did something: %q %q", lines, h.ctl()[n:])
		}
		// a client is blocked: only the VAPs it changes are touched, and only it is kicked
		n = len(h.ctl())
		h.runACL(vaps, []string{macC})
		got = h.ctl()[n:]
		if !contains(got, "wlan10 mac:kick "+macC) || contains(got, "wlan10 mac:kick "+macA) {
			t.Errorf("block: %q", got)
		}
		if !contains(got, "wlan20 mac:deny") || containsPrefix(got, "wlan11 ") {
			t.Errorf("block touched the wrong VAPs: %q", got)
		}
		// unblocked again
		n = len(h.ctl())
		h.runACL(vaps, nil)
		got = h.ctl()[n:]
		if !contains(got, "wlan20 mac:open") || containsPrefix(got, "wlan20 mac:kick") {
			t.Errorf("unblock: %q", got)
		}
		// the 2.4 GHz radio restarts: its VAPs get their ACL again, the others do not
		if _, err := os.Stat(filepath.Join(h.dir, "run", "wlan10.acl")); err != nil {
			t.Fatal(err)
		}
		changed := apFourVAPs(t, "Example Net 2", apTestSSID)
		changed[0].MACPolicy, changed[0].MACs = "deny", []string{macA}
		h.apply(changed)
		if _, err := os.Stat(filepath.Join(h.dir, "run", "wlan10.acl")); err == nil {
			t.Error("a restarted VAP kept its .acl file")
		}
		if _, err := os.Stat(filepath.Join(h.dir, "run", "wlan20.acl")); err != nil {
			t.Error("a VAP that was not restarted lost its .acl file")
		}
		n = len(h.ctl())
		h.runACL(changed, nil)
		got = h.ctl()[n:]
		if !contains(got, "wlan10 mac:deny") || containsPrefix(got, "wlan20 ") {
			t.Errorf("after the restart: %q", got)
		}
	})
}
