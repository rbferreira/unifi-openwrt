// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// The network of a WLAN (airport_net.go): the planner, the script text, and the
// script run against the stub device of airport_apply_test.go. Placeholder data.

func TestAPNetFor(t *testing.T) {
	vlan := func(id int) func(*translate.WantedWLAN) {
		return func(l *translate.WantedWLAN) { l.VLAN = id }
	}
	guest := func(id int) func(*translate.WantedWLAN) {
		return func(l *translate.WantedWLAN) { l.VLAN, l.Guest = id, true }
	}
	tests := []struct {
		name    string
		mod     func(*translate.WantedWLAN)
		net     APNet
		vlan    int
		isolate bool
		skip    string // a part of the reason, "" = a VAP
	}{
		{"LAN", func(*translate.WantedWLAN) {}, APNet{}, 0, false, ""},
		{"LAN with an uplink", func(*translate.WantedWLAN) {}, APNet{UpIf: "wlan9"}, 0, false, ""},
		{"VLAN 77", vlan(77), APNet{UpIf: "wlan9"}, 77, false, ""},
		{"VLAN 2", vlan(2), APNet{UpIf: "wlan9"}, 2, false, ""},
		{"VLAN 4094", vlan(4094), APNet{UpIf: "wlan9"}, 4094, false, ""},
		{"VLAN 1", vlan(1), APNet{UpIf: "wlan9"}, 0, false, "VLAN 1 is not a usable id"},
		{"VLAN 4095", vlan(4095), APNet{UpIf: "wlan9"}, 0, false, "VLAN 4095 is not a usable id"},
		{"VLAN -5", vlan(-5), APNet{UpIf: "wlan9"}, 0, false, "not a usable id"},
		{"VLAN without a wireless uplink", vlan(77), APNet{}, 0, false, "needs the mesh uplink"},
		{"uplink named like a daemon VAP", vlan(77), APNet{UpIf: "wlan10"}, 0, false, "not usable"},
		{"uplink that is no interface", vlan(77), APNet{UpIf: "wlan9; reboot"}, 0, false, "not usable"},
		{"uplink that is not a wlan", vlan(77), APNet{UpIf: "bridge0"}, 0, false, "not usable"},
		{"guest on a VLAN", guest(77), APNet{UpIf: "wlan9"}, 77, true, ""},
		{"guest on a VLAN, wired", guest(77), APNet{}, 0, false, "needs the mesh uplink"},
		{"guest on a VLAN, invalid id", guest(1), APNet{UpIf: "wlan9"}, 0, false, "not a usable id"},
		{"guest on a VLAN with the portal", guest(77), APNet{UpIf: "wlan9", Portal: true}, 0, false, "captive portal"},
		{"guest on the LAN", guest(0), APNet{UpIf: "wlan9"}, 0, false, "guest network on the LAN"},
		{"guest on the LAN, wired", guest(0), APNet{}, 0, false, "guest network on the LAN"},
		{"guest on the LAN with the portal", guest(0), APNet{UpIf: "wlan9", Portal: true}, 0, false, "captive portal"},
		// the portal is only about guests
		{"VLAN with the portal on", vlan(77), APNet{UpIf: "wlan9", Portal: true}, 77, false, ""},
		{"LAN with the portal on", func(*translate.WantedWLAN) {}, APNet{Portal: true}, 0, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := apWLAN("1", "wifi0")
			tc.mod(&l)
			vlan, isolate, skip := apNetFor(l, tc.net)
			if vlan != tc.vlan || isolate != tc.isolate {
				t.Errorf("vlan %d isolate %v, want %d %v", vlan, isolate, tc.vlan, tc.isolate)
			}
			if tc.skip == "" && skip != "" || tc.skip != "" && !strings.Contains(skip, tc.skip) {
				t.Errorf("skip %q, want %q", skip, tc.skip)
			}
		})
	}
}

func TestAirPortVAPsNet(t *testing.T) {
	up := APNet{UpIf: "wlan9"}
	mk := func(n, radio string, mod func(*translate.WantedWLAN)) translate.WantedWLAN {
		l := apWLAN(n, radio)
		if mod != nil {
			mod(&l)
		}
		return l
	}
	on := func(id int) func(*translate.WantedWLAN) { return func(l *translate.WantedWLAN) { l.VLAN = id } }
	guest := func(id int) func(*translate.WantedWLAN) {
		return func(l *translate.WantedWLAN) { l.VLAN, l.Guest = id, true }
	}

	t.Run("LAN and VLAN 77 on each radio, one bridge for the VLAN", func(t *testing.T) {
		w := translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{
			mk("1", "wifi0", nil), mk("2", "wifi1", nil), mk("3", "wifi0", on(77)), mk("4", "wifi1", on(77)),
		}}
		vaps, notes := AirPortVAPsNet(w, 40, up)
		if !reflect.DeepEqual(apNames(vaps), []string{"wlan10", "wlan20", "wlan11", "wlan21"}) { // in the order of the WLANs
			t.Fatalf("names %v notes %q", apNames(vaps), notes)
		}
		for i, want := range []struct {
			vlan   int
			bridge string
			upif   string
		}{{0, "bridge0", ""}, {0, "bridge0", ""}, {77, "bridge77", "wlan9"}, {77, "bridge77", "wlan9"}} {
			v := vaps[i]
			if v.VLAN != want.vlan || v.Bridge() != want.bridge || v.UpIf != want.upif {
				t.Errorf("%s: vlan %d bridge %s upif %q", v.Name, v.VLAN, v.Bridge(), v.UpIf)
			}
			if v.Isolate {
				t.Errorf("%s: isolation forced on a plain WLAN", v.Name)
			}
		}
		if len(notes) != 0 {
			t.Errorf("notes %q", notes)
		}
		if err := checkAirPortVAPs(vaps); err != nil {
			t.Error(err)
		}
		if got := vaps[2].String(); !strings.HasSuffix(got, ", VLAN 77") {
			t.Errorf("String %q", got)
		}
		if got := vaps[0].String(); strings.Contains(got, "VLAN") {
			t.Errorf("String %q", got)
		}
	})

	t.Run("VLAN without a wireless uplink stays off, the rest runs", func(t *testing.T) {
		w := translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{
			mk("1", "wifi0", on(77)), mk("2", "wifi0", nil), mk("3", "wifi1", on(77)),
		}}
		vaps, notes := AirPortVAPsNet(w, 0, APNet{})
		if !reflect.DeepEqual(apNames(vaps), []string{"wlan10"}) { // the skipped WLAN takes no slot
			t.Fatalf("names %v", apNames(vaps))
		}
		if len(notes) != 2 || !apNotesHave(notes, "wireless.1") || !apNotesHave(notes, "VLAN 77") || !apNotesHave(notes, "no VAP") || !apNotesHave(notes, "wired uplink") {
			t.Errorf("notes %q", notes)
		}
	})

	t.Run("invalid ids", func(t *testing.T) {
		for _, id := range []int{1, 4095, 70000, -2} {
			w := translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{mk("1", "wifi0", on(id))}}
			vaps, notes := AirPortVAPsNet(w, 40, up)
			if len(vaps) != 0 || !apNotesHave(notes, "not a usable id") {
				t.Errorf("VLAN %d: vaps %v notes %q", id, apNames(vaps), notes)
			}
		}
	})

	t.Run("guest variants", func(t *testing.T) {
		w := translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{
			mk("1", "wifi0", guest(30)), mk("2", "wifi1", guest(30)), // runs, isolated, shares bridge30
			mk("3", "wifi0", guest(0)),  // on the LAN: off
			mk("4", "wifi0", nil),       // the LAN WLAN, takes the second slot
			mk("5", "wifi1", guest(31)), // runs
		}}
		vaps, notes := AirPortVAPsNet(w, 40, up)
		if !reflect.DeepEqual(apNames(vaps), []string{"wlan10", "wlan20", "wlan11", "wlan21"}) {
			t.Fatalf("names %v notes %q", apNames(vaps), notes)
		}
		for i, want := range []struct {
			vlan    int
			isolate bool
		}{{30, true}, {30, true}, {0, false}, {31, true}} {
			if vaps[i].VLAN != want.vlan || vaps[i].Isolate != want.isolate {
				t.Errorf("%s: vlan %d isolate %v", vaps[i].Name, vaps[i].VLAN, vaps[i].Isolate)
			}
		}
		if !apNotesHave(notes, "wireless.3") || !apNotesHave(notes, "guest network on the LAN") {
			t.Errorf("notes %q", notes)
		}
		// the VLAN guest note comes once, with the count
		n := 0
		for _, x := range notes {
			if strings.Contains(x, "guest WLAN(s) on a VLAN") {
				n++
				if !strings.Contains(x, "3 guest") || !strings.Contains(x, "gateway") {
					t.Errorf("note %q", x)
				}
			}
		}
		if n != 1 {
			t.Errorf("%d guest notes: %q", n, notes)
		}
	})

	t.Run("guest with the portal is off, on a VLAN or not", func(t *testing.T) {
		w := translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{
			mk("1", "wifi0", guest(30)), mk("2", "wifi0", guest(0)), mk("3", "wifi0", on(30)), mk("4", "wifi0", nil),
		}}
		vaps, notes := AirPortVAPsNet(w, 40, APNet{UpIf: "wlan9", Portal: true})
		if !reflect.DeepEqual(apNames(vaps), []string{"wlan10", "wlan11"}) || vaps[0].VLAN != 30 || vaps[1].VLAN != 0 {
			t.Fatalf("vaps %v", vaps)
		}
		if len(notes) != 2 || !apNotesHave(notes, "wireless.1") || !apNotesHave(notes, "wireless.2") || !apNotesHave(notes, "captive portal") {
			t.Errorf("notes %q", notes)
		}
	})

	t.Run("a guest that forces isolation keeps an isolated WLAN isolated", func(t *testing.T) {
		l := mk("1", "wifi0", guest(30))
		l.Isolate = true
		vaps, _ := AirPortVAPsNet(translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{l}}, 40, up)
		if len(vaps) != 1 || !vaps[0].Isolate {
			t.Errorf("%+v", vaps)
		}
	})

	t.Run("the wrapper has no uplink to offer", func(t *testing.T) {
		w := translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{mk("1", "wifi0", on(77))}}
		if vaps, _ := AirPortVAPs(w, 40); len(vaps) != 0 {
			t.Errorf("%v", apNames(vaps))
		}
	})
}

func TestCheckAirPortVAPsNet(t *testing.T) {
	v := func(vlan int, upif string) []AirPortVAP {
		return []AirPortVAP{{Name: "wlan10", Parent: "bwl1", Head: "a=b\n", Tail: "c=d\n", VLAN: vlan, UpIf: upif}}
	}
	for _, tc := range []struct {
		name string
		vaps []AirPortVAP
		bad  bool
	}{
		{"LAN", v(0, ""), false},
		{"VLAN 77", v(77, "wlan9"), false},
		{"VLAN 2", v(2, "wlan9"), false},
		{"VLAN 4094", v(4094, "wlan9"), false},
		{"VLAN 1", v(1, "wlan9"), true},
		{"VLAN 0 is the LAN, with an uplink named", v(0, "wlan9"), true},
		{"VLAN 4095", v(4095, "wlan9"), true},
		{"VLAN -1", v(-1, "wlan9"), true},
		{"no uplink", v(77, ""), true},
		{"daemon's own VAP as uplink", v(77, "wlan11"), true},
		{"uplink with a shell in it", v(77, "wlan9; reboot"), true},
		{"uplink not a wlan", v(77, "bridge0"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkAirPortVAPs(tc.vaps); (err != nil) != tc.bad {
				t.Errorf("err %v, bad %v", err, tc.bad)
			}
		})
	}
}

func TestSnapshotUplinkIface(t *testing.T) {
	s, err := ParseAirPort([]byte(airportSample), time.Unix(1000200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.UplinkIface(); got != "wlan9" {
		t.Errorf("UplinkIface = %q", got)
	}
	// no uplink: wired
	for i := range s.Radios {
		var keep []Radio
		for _, v := range s.Radios[i].VAPs {
			if v.Mode != "Client" {
				keep = append(keep, v)
			}
		}
		s.Radios[i].VAPs = keep
	}
	if got := s.UplinkIface(); got != "" {
		t.Errorf("wired: UplinkIface = %q", got)
	}
	// a name that is not an interface of the net80211 kind is not passed on
	s.Radios[0].VAPs = []Radio{{Iface: "wlan9; x", Mode: "Client"}}
	if got := s.UplinkIface(); got != "" {
		t.Errorf("odd name: UplinkIface = %q", got)
	}
	s.Radios[0].VAPs = []Radio{{Iface: "wlan10", Mode: "Client"}}
	if got := s.UplinkIface(); got != "" {
		t.Errorf("a daemon-range name: UplinkIface = %q", got)
	}
}

func TestPlanAirPortVLAN(t *testing.T) {
	snap, err := ParseAirPort([]byte(airportSample), time.Unix(1000200, 0))
	if err != nil {
		t.Fatal(err)
	}
	base := `radio.1.phyname=wifi0
radio.1.ieee_mode=11nght20
radio.1.channel=6
wireless.1.ssid=Example Net
wireless.1.parent=wifi0
wireless.1.devname=ath0
aaa.1.wpa=2
aaa.1.wpa.psk=correct horse 1
wireless.2.ssid=Example IoT
wireless.2.parent=wifi0
wireless.2.devname=ath1
aaa.2.wpa=2
aaa.2.wpa.psk=correct horse 1
vlan.1.devname=eth0
vlan.1.id=77
bridge.2.devname=br0.77
bridge.2.port.1.devname=ath1
bridge.2.port.3.devname=eth0.77
`
	now := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	p := PlanAirPort(translate.ParseCfg(base), snap, now, nil, nil)
	if p.UpIf != "wlan9" || len(p.VAPs) != 2 || p.VAPs[1].VLAN != 77 || p.VAPs[1].UpIf != "wlan9" || p.VAPs[0].VLAN != 0 {
		t.Fatalf("plan: upif %q vaps %+v notes %q", p.UpIf, p.VAPs, p.Notes)
	}
	// a guest on the VLAN, with and without the controller's portal
	guest := base + "aaa.2.is_guest=true\n"
	p = PlanAirPort(translate.ParseCfg(guest), snap, now, nil, nil)
	if len(p.VAPs) != 2 || !p.VAPs[1].Isolate || p.VAPs[1].VLAN != 77 {
		t.Fatalf("guest plan: %+v notes %q", p.VAPs, p.Notes)
	}
	p = PlanAirPort(translate.ParseCfg(guest+"redirector.status=enabled\nredirector.url=https://192.0.2.10:8843/guest/s/default/\n"), snap, now, nil, nil)
	if len(p.VAPs) != 1 || !apNotesHave(p.Notes, "captive portal") {
		t.Fatalf("portal plan: %+v notes %q", p.VAPs, p.Notes)
	}
	// a wired unit: the VLAN WLAN is off, and says why
	wired := *snap
	wired.Radios = append([]Radio(nil), snap.Radios...)
	for i := range wired.Radios {
		var keep []Radio
		for _, v := range wired.Radios[i].VAPs {
			if v.Mode != "Client" {
				keep = append(keep, v)
			}
		}
		wired.Radios[i].VAPs = keep
	}
	p = PlanAirPort(translate.ParseCfg(base), &wired, now, nil, nil)
	if p.UpIf != "" || len(p.VAPs) != 1 || !apNotesHave(p.Notes, "needs the mesh uplink") {
		t.Fatalf("wired plan: upif %q vaps %+v notes %q", p.UpIf, p.VAPs, p.Notes)
	}
}

// ---- the script text ----

// apVLANVAPs: a LAN WLAN and a VLAN 77 WLAN on each radio, uplink wlan9.
func apVLANVAPs(t *testing.T) []AirPortVAP {
	t.Helper()
	l := func(n, radio string, vlan int) translate.WantedWLAN {
		w := apWLAN(n, radio)
		w.VLAN = vlan
		return w
	}
	w := translate.Wanted{Radios: apRadios(), WLANs: []translate.WantedWLAN{
		l("1", "wifi0", 0), l("2", "wifi1", 0), l("3", "wifi0", 77), l("4", "wifi1", 77),
	}}
	vaps, notes := AirPortVAPsNet(w, 40, APNet{UpIf: "wlan9"})
	if len(vaps) != 4 {
		t.Fatalf("vaps %v notes %q", apNames(vaps), notes)
	}
	return vaps
}

func TestAirPortNetCalls(t *testing.T) {
	vaps := apVLANVAPs(t)
	want := "net wlan10 bridge0\nnet wlan20 bridge0\nnet wlan11 bridge77 77 wlan9\nnet wlan21 bridge77 77 wlan9\nnetgc 77\n"
	if got := apNetCalls(vaps); got != want {
		t.Errorf("calls:\n%s\nwant:\n%s", got, want)
	}
	if got := apNetCalls(nil); got != "netgc\n" {
		t.Errorf("no VAPs: %q", got)
	}
	// two VLANs, ids sorted, each once
	two := append([]AirPortVAP(nil), vaps...)
	two[0].VLAN, two[0].UpIf = 9, "wlan9" // wlan10
	if got := apNetCalls(two); !strings.HasSuffix(got, "netgc 9 77\n") {
		t.Errorf("two VLANs: %q", got)
	}
	// the apply script has them after the radios and before the isolation flags
	s := AirPortApplyScript(vaps)
	r, n, i := strings.LastIndex(s, "\nradio "), strings.Index(s, "\nnet wlan10 "), strings.Index(s, "\niso wlan10 ")
	if !(r > 0 && r < n && n < i) {
		t.Errorf("order: radio %d net %d iso %d", r, n, i)
	}
	// start() leaves the bridge to net
	if strings.Contains(s, "brconfig bridge0 add") {
		t.Error("start still adds the VAP to bridge0 itself")
	}
}

var apNetLineRE = regexp.MustCompile(`(?m)^net (wlan[0-9]+) (bridge[0-9]+)(?: ([0-9]+) (wlan[0-9]+))?$`)

// The uplink's name is in the script only as the last word of a net line (where
// the function hands it to vlanif and to nothing else); every other wlan word is
// a VAP in the daemon's ranges. No ifconfig/brconfig line names it.
func TestAirPortScriptNamesTheUplinkOnlyAsVlanifParent(t *testing.T) {
	vaps := apVLANVAPs(t)
	for name, s := range map[string]string{"apply": AirPortApplyScript(vaps), "net only": AirPortNetScript(vaps)} {
		lines := strings.Split(s, "\n")
		for _, ln := range lines {
			if !strings.Contains(ln, "wlan9") {
				continue
			}
			m := apNetLineRE.FindStringSubmatch(ln)
			if m == nil || m[4] != "wlan9" || strings.Count(ln, "wlan9") != 1 {
				t.Errorf("%s: wlan9 outside a net line: %q", name, ln)
			}
		}
		if got := strings.Count(s, "wlan9"); got != 2 {
			t.Errorf("%s: wlan9 appears %d times, want 2 (one per VLAN VAP)", name, got)
		}
		// the helpers: net reads the parameter twice (is the uplink there, hand it to
		// mkvlan), and mkvlan gives it to ifconfig only as vlanif's argument
		if got := strings.Count(apNetFuncs, "$4"); got != 2 {
			t.Errorf("%s: net functions read $4 %d times", name, got)
		}
		if got := strings.Count(apNetFuncs, "vlanif"); got != 1 || !strings.Contains(apNetFuncs, "vlan $1 vlanif $2 ") {
			t.Errorf("%s: vlanif appears %d times", name, got)
		}
		// other wlan words: the daemon's VAPs, the ranges, ifconfig's keywords
		re := regexp.MustCompile(`wlan[0-9a-z\[\]|-]*`)
		for _, w := range re.FindAllString(s, -1) {
			switch w {
			case "wlan1[0-7]", "wlan2[0-7]", "wlandev", "wlanmode", "wlan9", "wlan10", "wlan11", "wlan20", "wlan21":
			default:
				t.Errorf("%s: unexpected wlan token %q", name, w)
			}
		}
	}
	// without a VLAN the old rule stands: the script never mentions the uplink
	for _, s := range []string{AirPortApplyScript(apTwoVAPs(t, apTestPass)), AirPortNetScript(apTwoVAPs(t, apTestPass)), AirPortApplyScript(nil)} {
		if strings.Contains(s, "wlan9") {
			t.Error("a script without VLANs mentions wlan9")
		}
	}
}

// Only vlanN and bridgeN (N a number) are ever created or destroyed, and only
// ones in the daemon's list; bridge0 is never destroyed.
func TestAirPortNetFuncsOnlyTouchTheirOwn(t *testing.T) {
	s := apNetFuncs
	for _, bad := range []string{"bwl", "wlan", "ifconfig wlan", "brconfig bridge0"} {
		if strings.Contains(s, bad) {
			t.Errorf("net functions mention %q", bad)
		}
	}
	// every create/destroy line is on a variable that is checked in the same
	// function, and destroy is only called with the daemon's own names
	for _, ln := range strings.Split(s, "\n") {
		if tl := strings.TrimSpace(ln); strings.Contains(ln, "ifconfig") && strings.Contains(ln, " destroy") && !strings.HasPrefix(tl, "#") && !strings.HasPrefix(tl, "echo") {
			ok := strings.Contains(ln, "ifconfig $vl destroy") || strings.Contains(ln, "ifconfig $n destroy")
			if !ok {
				t.Errorf("unexpected destroy: %q", ln)
			}
		}
	}
	// netgc refuses ids outside 2..4094 and anything that is not vlanN/bridgeN
	for _, want := range []string{"[ $id -ge 2 ] && [ $id -le 4094 ] || continue", "case $id in\n\t\t\t*[!0-9]*) continue;;", "$pass[0-9]*) id=${n#$pass};;"} {
		if !strings.Contains(s, want) {
			t.Errorf("netgc lacks %q", want)
		}
	}
	// mkvlan destroys a vlan only when it is the daemon's own
	i, j := strings.Index(s, "owned $vl || {"), strings.Index(s, "ifconfig $vl destroy")
	if i < 0 || j < 0 || i > j {
		t.Error("mkvlan must check ownership before it destroys")
	}
}

func TestAirPortNetScriptSyntax(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host: cannot run sh -n")
	}
	for name, s := range map[string]string{
		"apply, LAN and VLAN": AirPortApplyScript(apVLANVAPs(t)),
		"net only":            AirPortNetScript(apVLANVAPs(t)),
		"net only, no VAPs":   AirPortNetScript(nil),
		"apply, no VAPs":      AirPortApplyScript(nil),
	} {
		t.Run(name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "net.sh")
			if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
				t.Errorf("sh -n: %v\n%s", err, out)
			}
		})
	}
}

// ---- the script against the stub device ----

func (h *apStubHost) members(br string) []string {
	h.t.Helper()
	var out []string
	for _, m := range h.read("state/br/" + br) {
		if m != "" {
			out = append(out, m)
		}
	}
	return out
}

func (h *apStubHost) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(h.dir, filepath.FromSlash(rel)))
	return err == nil
}

func (h *apStubHost) write(rel, text string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, filepath.FromSlash(rel)), []byte(text), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// apFirmware is bridge0 as the device has it: what the daemon must leave alone.
var apFirmware = []string{"bcmeth1", "wlan0", "wlan9"}

func TestAirPortNetAgainstStubs(t *testing.T) {
	started := func(names ...string) []string {
		var out []string
		for _, n := range names {
			out = append(out, n+" started")
		}
		return out
	}

	t.Run("VLAN on both radios: one bridge, then nothing to do", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps := apVLANVAPs(t) // wlan10 and wlan20 on the LAN, wlan11 and wlan21 on VLAN 77
		got := h.apply(vaps)
		want := append(started("wlan10", "wlan11", "wlan20", "wlan21"), "vlan77 created", "bridge77 created")
		// the report: the radios' lines first, then the net step's
		apWant(t, "first run", got, want)
		apWant(t, "bridge0", h.members("bridge0"), []string{"bcmeth1", "wlan0", "wlan9", "wlan10", "wlan20"})
		apWant(t, "bridge77", h.members("bridge77"), []string{"vlan77", "wlan11", "wlan21"})
		apWant(t, "vlan77", h.read("state/vlan/vlan77"), []string{"77 wlan9"})
		apWant(t, "nets", h.read("run/nets"), []string{"vlan77", "bridge77"})
		if !h.exists("state/up/vlan77") || !h.exists("state/up/bridge77") {
			t.Error("vlan77 and bridge77 must be up")
		}
		// the uplink is only named as vlanif's parent: nothing else was done to it
		for _, l := range h.read("state/log") {
			if strings.Contains(l, "wlan9") && l != "vlan vlan77 77 wlan9" {
				t.Errorf("the uplink was touched: %q", l)
			}
		}
		n := len(h.read("state/log"))
		apWant(t, "second run", h.apply(vaps), []string{"wlan10 same", "wlan11 same", "wlan20 same", "wlan21 same"})
		apWant(t, "nothing touched by the second run", h.logFrom(n), nil)
		// the keep check prints nothing and touches nothing
		apWant(t, "net check", h.applyNets(vaps), nil)
		apWant(t, "nothing touched by the net check", h.logFrom(n), nil)
	})

	t.Run("a mesh uplink made again: vlan77 loses its parent and is repaired", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps := apVLANVAPs(t)
		h.apply(vaps)
		h.write("state/vlan/vlan77", "") // the parent went away with the uplink
		apWant(t, "repair", h.applyNets(vaps), []string{"vlan77 repaired (parent wlan9)"})
		apWant(t, "vlan77", h.read("state/vlan/vlan77"), []string{"77 wlan9"})
		apWant(t, "bridge77", h.members("bridge77"), []string{"wlan11", "wlan21", "vlan77"}) // vlan77 was made again and added last
		apWant(t, "bridge0 untouched", h.members("bridge0"), []string{"bcmeth1", "wlan0", "wlan9", "wlan10", "wlan20"})
		if !h.exists("state/up/vlan77") {
			t.Error("vlan77 must be up again")
		}
		apWant(t, "nets", h.read("run/nets"), []string{"vlan77", "bridge77"})
		apWant(t, "repaired once", h.applyNets(vaps), nil)
		// another parent: the uplink is now wlan8
		h.write("state/vap/wlan8", "bwl0\n")
		h.write("state/vlan/vlan77", "77 wlan8\n")
		moved := h.applyNets(vaps)
		apWant(t, "another parent", moved, []string{"vlan77 repaired (parent wlan9)"})
	})

	t.Run("the uplink is gone: the VLAN VAPs are in no bridge, the LAN's stay", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps := apVLANVAPs(t)
		h.apply(vaps)
		if err := os.Remove(filepath.Join(h.dir, "state", "vap", "wlan9")); err != nil {
			t.Fatal(err)
		}
		// a VLAN VAP that sits in bridge0 (it was on the LAN before) must come out
		h.write("state/br/bridge0", "bcmeth1\nwlan0\nwlan10\nwlan20\nwlan11\n")
		h.write("state/br/bridge77", "vlan77\nwlan21\n")
		n := len(h.read("state/log"))
		apWant(t, "no uplink", h.applyNets(vaps), nil)
		apWant(t, "bridge0", h.members("bridge0"), []string{"bcmeth1", "wlan0", "wlan10", "wlan20"})
		apWant(t, "bridge77", h.members("bridge77"), []string{"vlan77"})
		for _, l := range h.logFrom(n) {
			if strings.Contains(l, "create") || strings.Contains(l, "vlan vlan77") {
				t.Errorf("a vlan was made on a missing uplink: %q", l)
			}
		}
	})

	t.Run("the VLAN goes away: the daemon's vlan and bridge are destroyed, nothing else", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps := apVLANVAPs(t)
		h.apply(vaps)
		// things that are not the daemon's: another vlan and bridge on the unit
		h.write("state/vlan/vlan88", "88 wlan9\n")
		h.write("state/br/bridge88", "vlan88\n")
		// the WLAN that was on VLAN 77 is now on the LAN
		lan := append([]AirPortVAP(nil), vaps...)
		lan[2].VLAN, lan[2].UpIf = 0, ""
		lan[3].VLAN, lan[3].UpIf = 0, ""
		got := h.apply(lan)
		apWant(t, "report", got, []string{"wlan10 same", "wlan11 same", "wlan20 same", "wlan21 same", "wlan11 bridged bridge0", "wlan21 bridged bridge0", "bridge77 removed", "vlan77 removed"})
		apWant(t, "bridge0", h.members("bridge0"), []string{"bcmeth1", "wlan0", "wlan9", "wlan10", "wlan20", "wlan11", "wlan21"})
		if h.exists("state/br/bridge77") || h.exists("state/vlan/vlan77") {
			t.Error("bridge77 and vlan77 must be gone")
		}
		if !h.exists("state/br/bridge88") || !h.exists("state/vlan/vlan88") {
			t.Error("another bridge and vlan were destroyed")
		}
		apWant(t, "nets empty", nonEmpty(h.read("run/nets")), nil)
		// and nothing more to do
		n := len(h.read("state/log"))
		apWant(t, "again", h.apply(lan), []string{"wlan10 same", "wlan11 same", "wlan20 same", "wlan21 same"})
		apWant(t, "nothing touched", h.logFrom(n), nil)
		// no VAPs at all
		h.apply(nil)
		apWant(t, "bridge0 after all removed", h.members("bridge0"), apFirmware)
		for _, l := range h.read("state/log") {
			if strings.Contains(l, "bridge0 delete") && !strings.Contains(l, "wlan1") && !strings.Contains(l, "wlan2") {
				t.Errorf("a firmware member was taken out of bridge0: %q", l)
			}
		}
	})

	t.Run("the daemon's list is what it may destroy", func(t *testing.T) {
		h := newAPStubHost(t)
		// a list with a made-up and a dangerous entry: neither is touched
		if err := os.MkdirAll(filepath.Join(h.dir, "run"), 0o755); err != nil {
			t.Fatal(err)
		}
		h.write("run/nets", "bridge0\nvlan1\nbridge1\nvlan4095\nwlan9\nbridge99x\nvlan88\n")
		h.write("state/vlan/vlan88", "88 wlan9\n")
		h.write("state/br/bridge88", "vlan88\n")
		h.write("state/vlan/vlan5", "5 wlan9\n") // not in the list
		out := h.apply(nil)
		apWant(t, "report", out, []string{"vlan88 removed"})
		for _, keep := range []string{"state/br/bridge0", "state/vap/wlan9", "state/br/bridge88", "state/vlan/vlan5"} {
			if !h.exists(keep) {
				t.Errorf("%s was destroyed", keep)
			}
		}
		apWant(t, "bridge0", h.members("bridge0"), apFirmware)
	})

	t.Run("a vlan that is there and is not the daemon's is not taken over", func(t *testing.T) {
		h := newAPStubHost(t)
		h.write("state/vlan/vlan77", "77 wlan8\n") // someone else's, on another parent
		h.write("state/vap/wlan8", "bwl0\n")
		vaps := apVLANVAPs(t)
		out := h.apply([]AirPortVAP{vaps[0], vaps[2]}) // wlan10 (LAN), wlan11 (VLAN)
		apWant(t, "report", out, []string{"wlan10 started", "wlan11 started", "vlan77 exists and is not the daemon's", "wlan11 failed: vlan77 on the uplink"})
		if got := h.vapsOnDevice(); strings.Contains(got, "wlan11") {
			t.Errorf("the VAP stayed on the air: %q", got)
		}
		apWant(t, "vlan77 untouched", h.read("state/vlan/vlan77"), []string{"77 wlan8"})
		apWant(t, "bridge0", h.members("bridge0"), []string{"bcmeth1", "wlan0", "wlan9", "wlan10"})
	})

	t.Run("a step that fails stops the VAP, and the next apply tries again", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps := apVLANVAPs(t)
		h.write("state/fail/brup", "")
		out := h.apply(vaps)
		if !contains(out, "wlan11 failed: brconfig bridge77 up") || !contains(out, "wlan21 failed: brconfig bridge77 up") {
			t.Fatalf("report %q", out)
		}
		if got := h.vapsOnDevice(); got != "wlan0 wlan10 wlan20 wlan9" {
			t.Errorf("VAPs on the air: %q", got)
		}
		for _, b := range []string{"bridge0"} {
			if m := h.members(b); contains(m, "wlan11") || contains(m, "wlan21") {
				t.Errorf("%s holds a VLAN VAP: %v", b, m)
			}
		}
		if err := os.Remove(filepath.Join(h.dir, "state", "fail", "brup")); err != nil {
			t.Fatal(err)
		}
		out = h.apply(vaps)
		if !contains(out, "wlan11 started") || !contains(out, "wlan21 started") || len(out) != 4 {
			t.Errorf("second report %q", out)
		}
		apWant(t, "bridge77", h.members("bridge77"), []string{"vlan77", "wlan11", "wlan21"})
	})

	t.Run("vlanif refused: nothing left half-made", func(t *testing.T) {
		h := newAPStubHost(t)
		h.write("state/fail/vlanif", "")
		vaps := apVLANVAPs(t)
		out := h.apply([]AirPortVAP{vaps[0], vaps[2]})
		if !contains(out, "wlan11 failed: vlan77 on the uplink") {
			t.Fatalf("report %q", out)
		}
		if h.exists("state/vlan/vlan77") || h.exists("state/br/bridge77") {
			t.Error("a half-made vlan was left")
		}
		apWant(t, "nets", nonEmpty(h.read("run/nets")), nil)
		if got := h.vapsOnDevice(); strings.Contains(got, "wlan11") {
			t.Errorf("the VAP stayed on the air: %q", got)
		}
	})

	t.Run("a WLAN that moves from the LAN to a VLAN leaves the LAN bridge", func(t *testing.T) {
		h := newAPStubHost(t)
		lan := apVLANVAPs(t)
		lan[2].VLAN, lan[2].UpIf = 0, ""
		lan[3].VLAN, lan[3].UpIf = 0, ""
		h.apply(lan)
		apWant(t, "on the LAN", h.members("bridge0"), []string{"bcmeth1", "wlan0", "wlan9", "wlan10", "wlan20", "wlan11", "wlan21"})
		out := h.apply(apVLANVAPs(t))
		apWant(t, "moved", out, []string{"wlan10 same", "wlan11 same", "wlan20 same", "wlan21 same", "vlan77 created", "bridge77 created", "wlan11 bridged bridge77", "wlan21 bridged bridge77"})
		apWant(t, "bridge0", h.members("bridge0"), []string{"bcmeth1", "wlan0", "wlan9", "wlan10", "wlan20"})
		apWant(t, "bridge77", h.members("bridge77"), []string{"vlan77", "wlan11", "wlan21"})
	})

	t.Run("a restart of the radio puts the VAP back in its bridge", func(t *testing.T) {
		h := newAPStubHost(t)
		vaps := apVLANVAPs(t)
		h.apply(vaps)
		changed := append([]AirPortVAP(nil), vaps...)
		changed[0].Head = strings.Replace(vaps[0].Head, "ssid=Example Net", "ssid=Example Net 2", 1)
		out := h.apply(changed)
		apWant(t, "2.4 GHz restarted", out, []string{"wlan10 started", "wlan11 started", "wlan20 same", "wlan21 same"})
		apWant(t, "bridge77", h.members("bridge77"), []string{"vlan77", "wlan21", "wlan11"})
		if m := h.members("bridge0"); !contains(m, "wlan10") || contains(m, "wlan11") {
			t.Errorf("bridge0 %v", m)
		}
	})
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
