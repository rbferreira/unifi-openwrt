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

// The guest policy on an AirPort (airport_guest.go): range checks, the plan
// from the policy, the pf ruleset text and order, and the script run against the
// stub pf of airport_limit_test.go. Placeholder data.

func TestAPGuestRanges(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		v4   []string
		v6   int
		err  string // a part, "" = accepted
	}{
		{"none", nil, nil, 0, ""},
		{"the controller's default", []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4"},
			[]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4"}, 0, ""},
		{"a host route", []string{"192.0.2.1/32"}, []string{"192.0.2.1/32"}, 0, ""},
		{"host bits are cleared", []string{"192.0.2.77/24"}, []string{"192.0.2.0/24"}, 0, ""},
		{"a repeat is kept once", []string{"192.0.2.0/24", "192.0.2.5/24"}, []string{"192.0.2.0/24"}, 0, ""},
		{"IPv6 is counted, not kept", []string{"fc00::/7", "fe80::/10", "10.0.0.0/8"}, []string{"10.0.0.0/8"}, 2, ""},
		{"a bare address", []string{"192.0.2.1"}, nil, 0, "not a CIDR prefix"},
		{"a bare IPv6 address", []string{"2001:db8::1"}, nil, 0, "not a CIDR prefix"},
		{"a range of addresses", []string{"192.0.2.1-192.0.2.9"}, nil, 0, "not a CIDR prefix"},
		{"a mask instead of a length", []string{"192.0.2.0/255.255.255.0"}, nil, 0, "not a CIDR prefix"},
		{"a length that is too big", []string{"192.0.2.0/33"}, nil, 0, "not a CIDR prefix"},
		{"IPv6 length too big", []string{"fc00::/129"}, nil, 0, "not a CIDR prefix"},
		{"a leading zero", []string{"010.0.0.0/8"}, nil, 0, "not a CIDR prefix"},
		{"a leading zero in the length", []string{"10.0.0.0/08"}, nil, 0, "not a CIDR prefix"},
		{"a sign", []string{"10.0.0.0/+8"}, nil, 0, "not a CIDR prefix"},
		{"spaces", []string{" 10.0.0.0/8"}, nil, 0, "not a CIDR prefix"},
		{"a trailing newline", []string{"10.0.0.0/8\n"}, nil, 0, "not a CIDR prefix"},
		{"two in one", []string{"10.0.0.0/8 192.0.2.0/24"}, nil, 0, "not a CIDR prefix"},
		{"a list", []string{"10.0.0.0/8,192.0.2.0/24"}, nil, 0, "not a CIDR prefix"},
		{"a shell word", []string{"10.0.0.0/8;reboot"}, nil, 0, "not a CIDR prefix"},
		{"a pf keyword", []string{"any"}, nil, 0, "not a CIDR prefix"},
		{"a pf table", []string{"<lan>"}, nil, 0, "not a CIDR prefix"},
		{"a negation", []string{"!10.0.0.0/8"}, nil, 0, "not a CIDR prefix"},
		{"empty", []string{""}, nil, 0, "not a CIDR prefix"},
		{"a name", []string{"example.net/24"}, nil, 0, "not a CIDR prefix"},
		{"a zone", []string{"fe80::1%eth0/64"}, nil, 0, "not a CIDR prefix"},
		{"IPv4 in IPv6", []string{"::ffff:192.0.2.0/120"}, nil, 0, "plain IPv4"},
		{"/0", []string{"0.0.0.0/0"}, nil, 0, "every address"},
		{"IPv6 /0", []string{"::/0"}, nil, 0, "every address"},
		{"the good ones before a bad one", []string{"10.0.0.0/8", "nope"}, nil, 0, "not a CIDR prefix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v4, v6, err := apGuestRanges(tc.in)
			if tc.err == "" {
				if err != nil || !reflect.DeepEqual(v4, tc.v4) || v6 != tc.v6 {
					t.Errorf("got %q %d %v, want %q %d", v4, v6, err, tc.v4, tc.v6)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("got %q %d %v, want an error with %q", v4, v6, err, tc.err)
			}
		})
	}
	t.Run("too many", func(t *testing.T) {
		var in []string
		for i := 0; i <= apMaxGuestRanges; i++ {
			in = append(in, "192.0.2.0/24")
		}
		if _, _, err := apGuestRanges(in); err == nil {
			t.Error("accepted")
		}
		if _, _, err := apGuestRanges(in[:apMaxGuestRanges]); err != nil {
			t.Error(err)
		}
	})
}

func TestAPCoversPrivate(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want bool
	}{
		{nil, false},
		{[]string{"10.0.0.0/8"}, true},
		{[]string{"172.16.0.0/12"}, true},
		{[]string{"192.168.0.0/16"}, true},
		{[]string{"192.168.0.0/15"}, true},
		{[]string{"0.0.0.0/1"}, true},
		{[]string{"172.16.0.0/13"}, false}, // half of the block
		{[]string{"10.1.0.0/16"}, false},
		{[]string{"192.168.1.0/24", "192.168.2.0/24"}, false}, // two halves are not computed: stays off
		{[]string{"192.0.2.0/24", "224.0.0.0/4"}, false},
		{[]string{"11.0.0.0/8"}, false},
	} {
		if got := apCoversPrivate(tc.in); got != tc.want {
			t.Errorf("%q: %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestAirPortGuest(t *testing.T) {
	vaps := []AirPortVAP{
		{Name: "wlan10"},
		{Name: "wlan11", Isolate: true, Guest: true},
		{Name: "wlan20"},
		{Name: "wlan21", Isolate: true, Guest: true},
	}
	pol := translate.Guest{Restricted: []string{"10.0.0.0/8", "192.168.5.9/16", "fc00::/7"}, PreAllow: []string{"192.168.1.1/32"}}
	t.Run("the guest VAPs, the ranges, the notes", func(t *testing.T) {
		g, notes, err := AirPortGuest(vaps, pol)
		want := PFGuest{VAPs: []string{"wlan11", "wlan21"}, Restricted: []string{"10.0.0.0/8", "192.168.0.0/16"}, PreAllow: []string{"192.168.1.1/32"}}
		if err != nil || !reflect.DeepEqual(g, want) {
			t.Fatalf("got %+v %v", g, err)
		}
		if len(notes) != 3 ||
			!strings.Contains(notes[0], "wlan11 wlan21") || !strings.Contains(notes[0], "[10.0.0.0/8 192.168.0.0/16]") || !strings.Contains(notes[0], "[192.168.1.1/32]") ||
			!strings.Contains(notes[1], "captive portal and guest authorisation are not supported") ||
			!strings.Contains(notes[2], "1 IPv6 range(s) not written") {
			t.Errorf("notes %q", notes)
		}
	})
	t.Run("nothing pre-allowed, private space restricted: said", func(t *testing.T) {
		_, notes, err := AirPortGuest(vaps, translate.Guest{Restricted: []string{"192.168.0.0/16"}})
		if err != nil || len(notes) != 3 || !strings.Contains(notes[2], "no range is pre-allowed") {
			t.Errorf("%v %q", err, notes)
		}
	})
	t.Run("no guest VAP: nothing", func(t *testing.T) {
		g, notes, err := AirPortGuest(vaps[:1], pol)
		if err != nil || !reflect.DeepEqual(g, PFGuest{}) || notes != nil {
			t.Errorf("%+v %q %v", g, notes, err)
		}
		// an invalid policy is of no concern without a guest VAP
		if _, _, err := AirPortGuest(vaps[:1], translate.Guest{Restricted: []string{"nope"}}); err != nil {
			t.Error(err)
		}
	})
	t.Run("an invalid range is refused", func(t *testing.T) {
		for _, p := range []translate.Guest{
			{Restricted: []string{"10.0.0.0/8", "192.0.2.1"}},
			{Restricted: []string{"10.0.0.0/8"}, PreAllow: []string{"any"}},
		} {
			if g, _, err := AirPortGuest(vaps, p); err == nil {
				t.Errorf("%+v accepted: %+v", p, g)
			}
		}
	})
}

func TestAirPortPFScriptGuestRefuses(t *testing.T) {
	iso := []PFIso{{"wlan11", "bridge0"}}
	ok := PFGuest{VAPs: []string{"wlan11"}, Restricted: []string{"10.0.0.0/8"}, PreAllow: []string{"192.168.1.1/32"}}
	if _, err := AirPortPFScriptGuest(nil, iso, pfAll, ok); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		g    PFGuest
		iso  []PFIso
	}{
		{"firmware VAP", PFGuest{VAPs: []string{"wlan0"}}, []PFIso{{"wlan0", "bridge0"}}},
		{"injection", PFGuest{VAPs: []string{"wlan11;reboot"}}, iso},
		{"not isolated", PFGuest{VAPs: []string{"wlan12"}}, iso},
		{"twice", PFGuest{VAPs: []string{"wlan11", "wlan11"}}, iso},
		{"a bare address", PFGuest{VAPs: []string{"wlan11"}, Restricted: []string{"10.0.0.1"}}, iso},
		{"injection in a range", PFGuest{VAPs: []string{"wlan11"}, Restricted: []string{"10.0.0.0/8; reboot"}}, iso},
		{"pre-allowed injection", PFGuest{VAPs: []string{"wlan11"}, PreAllow: []string{"$(reboot)"}}, iso},
		{"host bits", PFGuest{VAPs: []string{"wlan11"}, Restricted: []string{"10.0.0.1/8"}}, iso},
		{"IPv6", PFGuest{VAPs: []string{"wlan11"}, Restricted: []string{"fc00::/7"}}, iso},
		{"a repeat", PFGuest{VAPs: []string{"wlan11"}, Restricted: []string{"10.0.0.0/8", "10.0.0.0/8"}}, iso},
	} {
		if s, err := AirPortPFScriptGuest(nil, tc.iso, pfAll, tc.g); err == nil {
			t.Errorf("%s: accepted:\n%s", tc.name, s)
		}
	}
}

// guestRules are the lines of one guest VAP: DHCP, the pre-allowed passes, the
// restricted blocks, the IPv6 block, all quick, the passes tagged.
func guestRules(v string, pre, res []string) string {
	s := "pass in quick on " + v + " inet proto udp from any port 68 to any port 67 tag UOWSTA\n"
	for _, r := range pre {
		s += "pass in quick on " + v + " inet from any to " + r + " tag UOWSTA\n"
	}
	for _, r := range res {
		s += "block in quick on " + v + " inet from any to " + r + "\n"
	}
	return s + "block in quick on " + v + " inet6 all\n"
}

// The ruleset for each mix, from the planner's own output: queues, pass all, the
// tagging, the isolation lines, and last the guest lines.
func TestAirPortGuestRuleset(t *testing.T) {
	def := translate.Guest{
		Restricted: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4"},
		PreAllow:   []string{"192.168.1.1/32", "192.168.1.2/32"},
	}
	capped := []translate.VAPLimit{{Devname: "wifi0ap1", DownKbps: 2000}}
	mk := func(n, radio, dev string, guest bool, vlan int) translate.WantedWLAN {
		l := apWLAN(n, radio)
		l.Guest, l.VLAN, l.Devname = guest, vlan, dev
		return l
	}
	ownIso := mk("1", "wifi0", "wifi0ap0", false, 0) // isolated by its own flag, not a guest
	ownIso.Isolate = true
	limRules := "altq on wlan11 hfsc bandwidth 1Gb queue { lim_wlan11 }\n" +
		"queue lim_wlan11 bandwidth 2000Kb hfsc(default upperlimit 2000Kb)\n"

	for _, tc := range []struct {
		name   string
		wlans  []translate.WantedWLAN
		pol    translate.Guest
		lims   []translate.VAPLimit
		up     []string
		rules  string
		report []string
	}{
		{
			// wlan10/wlan20 normal; wlan11/wlan21 the guest WLAN, on the LAN
			"guest on the LAN, both radios, one normal WLAN",
			[]translate.WantedWLAN{mk("1", "wifi0", "wifi0ap0", false, 0), mk("2", "wifi0", "wifi0ap1", true, 0), mk("3", "wifi1", "wifi1ap0", false, 0), mk("4", "wifi1", "wifi1ap1", true, 0)},
			def, nil, []string{"wlan10", "wlan11", "wlan20", "wlan21", "bridge0"},
			"pass all\n" + tagRules("wlan10", "wlan11", "wlan20", "wlan21") + isoBlock("wlan11") + isoBlock("wlan21") +
				guestRules("wlan11", def.PreAllow, def.Restricted) + guestRules("wlan21", def.PreAllow, def.Restricted),
			[]string{"bridge0: ipf on", "pf: 2 isolated VAP(s)"},
		},
		{
			"guest on a VLAN: the same lines, its bridge filtered",
			[]translate.WantedWLAN{mk("1", "wifi0", "wifi0ap0", true, 77), mk("2", "wifi1", "wifi1ap0", true, 77)},
			def, nil, []string{"wlan10", "wlan20", "bridge77"},
			"pass all\n" + tagRules("wlan10", "wlan20") + isoBlock("wlan10") + isoBlock("wlan20") +
				guestRules("wlan10", def.PreAllow, def.Restricted) + guestRules("wlan20", def.PreAllow, def.Restricted),
			[]string{"bridge77: ipf on", "pf: 2 isolated VAP(s)"},
		},
		{
			"guest and a limit on the same VAP: the queue first, the guest lines last",
			[]translate.WantedWLAN{mk("1", "wifi0", "wifi0ap0", false, 0), mk("2", "wifi0", "wifi0ap1", true, 0)},
			translate.Guest{Restricted: []string{"192.168.0.0/16"}}, capped, []string{"wlan10", "wlan11", "bridge0"},
			limRules + "pass all\n" + tagRules("wlan10", "wlan11") + isoBlock("wlan11") + guestRules("wlan11", nil, []string{"192.168.0.0/16"}),
			[]string{"bridge0: ipf on", "pf: 1 limited VAP(s), 1 isolated VAP(s)"},
		},
		{
			"a guest and a non-guest isolated WLAN: the second gets no guest lines",
			[]translate.WantedWLAN{ownIso, mk("2", "wifi0", "wifi0ap1", true, 0)},
			def, nil, []string{"wlan10", "wlan11", "bridge0"},
			"pass all\n" + tagRules("wlan10", "wlan11") + isoBlock("wlan10") + isoBlock("wlan11") + guestRules("wlan11", def.PreAllow, def.Restricted),
			[]string{"bridge0: ipf on", "pf: 2 isolated VAP(s)"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wlans := tc.wlans
			vaps, notes := AirPortVAPsNet(translate.Wanted{Radios: apRadios(), WLANs: wlans}, 40, APNet{UpIf: "wlan9", Restricted: tc.pol.Restricted, PreAllow: tc.pol.PreAllow})
			if len(vaps) != len(wlans) {
				t.Fatalf("vaps %v notes %q", apNames(vaps), notes)
			}
			lims, _ := AirPortLimits(tc.lims, vaps)
			iso, _ := AirPortIsolation(vaps)
			g, _, err := AirPortGuest(vaps, tc.pol)
			if err != nil {
				t.Fatal(err)
			}
			h := newPFHost(t)
			h.up(tc.up...)
			rep, _ := h.runPFG(lims, iso, g)
			if !reflect.DeepEqual(rep, tc.report) {
				t.Errorf("report %q, want %q", rep, tc.report)
			}
			t.Logf("pf.conf:\n%s", h.text("run/pf.conf"))
			if got := h.text("run/pf.conf"); got != tc.rules {
				t.Errorf("pf.conf:\n%s\nwant:\n%s", got, tc.rules)
			}
			for _, v := range g.VAPs {
				if !h.has("run/" + v + ".pfq") {
					t.Errorf("no marker for %s", v)
				}
			}
			// the guest block is the last thing in the file, and every quick line
			// of a guest VAP comes after that VAP's isolation lines
			lines := strings.Split(strings.TrimSpace(h.text("run/pf.conf")), "\n")
			firstQuick := -1
			lastIso := -1
			for i, l := range lines {
				if strings.Contains(l, " quick ") && firstQuick < 0 {
					firstQuick = i
				}
				if strings.HasPrefix(l, "block out") || strings.HasPrefix(l, "pass out") {
					lastIso = i
				}
			}
			if firstQuick < lastIso {
				t.Errorf("a quick line (%d) before the isolation lines end (%d)", firstQuick, lastIso)
			}
		})
	}
}

// The guest VAP that does not run is left out, one made again gets its lines
// back, and with no guest left the lines go.
func TestAirPortGuestRun(t *testing.T) {
	g := PFGuest{VAPs: []string{"wlan11", "wlan21"}, Restricted: []string{"192.168.0.0/16"}, PreAllow: []string{"192.0.2.1/32"}}
	iso := []PFIso{{"wlan11", "bridge0"}, {"wlan21", "bridge0"}}
	h := newPFHost(t)
	h.up("wlan11", "wlan21", "bridge0")
	h.runPFG(nil, iso, g)
	if !strings.Contains(h.text("run/pf.conf"), guestRules("wlan21", g.PreAllow, g.Restricted)) {
		t.Fatalf("pf.conf:\n%s", h.text("run/pf.conf"))
	}
	t.Run("again: nothing", func(t *testing.T) {
		rep, calls := h.runPFG(nil, iso, g)
		for _, c := range calls {
			if strings.HasPrefix(c, "pfctl -f") {
				t.Errorf("loaded again: %q", calls)
			}
		}
		if len(rep) != 0 {
			t.Errorf("report %q", rep)
		}
	})
	t.Run("a policy change loads the ruleset again", func(t *testing.T) {
		g2 := g
		g2.Restricted = []string{"10.0.0.0/8", "192.168.0.0/16"}
		_, calls := h.runPFG(nil, iso, g2)
		loaded := false
		for _, c := range calls {
			loaded = loaded || strings.HasPrefix(c, "pfctl -f")
		}
		if !loaded || !strings.Contains(h.text("run/pf.conf"), "block in quick on wlan11 inet from any to 10.0.0.0/8\n") {
			t.Errorf("calls %q\n%s", calls, h.text("run/pf.conf"))
		}
		h.runPFG(nil, iso, g)
	})
	t.Run("a guest VAP that does not run is left out", func(t *testing.T) {
		h.up("wlan11", "bridge0")
		h.runPFG(nil, iso[:1], PFGuest{VAPs: g.VAPs[:1], Restricted: g.Restricted, PreAllow: g.PreAllow})
		if strings.Contains(h.text("run/pf.conf"), "wlan21") {
			t.Errorf("pf.conf:\n%s", h.text("run/pf.conf"))
		}
		h.up("wlan11", "wlan21", "bridge0")
	})
	t.Run("a recreated guest VAP gets its lines back", func(t *testing.T) {
		os.Remove(filepath.Join(h.dir, "run", "wlan21.pfq"))
		h.runPFG(nil, iso, g)
		if !h.has("run/wlan21.pfq") || !strings.Contains(h.text("run/pf.conf"), "block in quick on wlan21 inet6 all\n") {
			t.Errorf("pf.conf:\n%s", h.text("run/pf.conf"))
		}
	})
	t.Run("a guest policy with no guest VAP is no guest line", func(t *testing.T) {
		h.runPFG(nil, iso, PFGuest{})
		if strings.Contains(h.text("run/pf.conf"), "quick") {
			t.Errorf("pf.conf:\n%s", h.text("run/pf.conf"))
		}
	})
}

func TestAirPortGuestScriptSyntax(t *testing.T) {
	g := PFGuest{VAPs: []string{"wlan11", "wlan21"}, Restricted: []string{"10.0.0.0/8", "224.0.0.0/4"}, PreAllow: []string{"192.0.2.1/32"}}
	iso := []PFIso{{"wlan11", "bridge0"}, {"wlan21", "bridge0"}}
	s, err := AirPortPFScriptGuest([]PFLimit{{VAP: "wlan10", DownKbps: 3000}}, iso, pfAll, g)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"gst wlan11\ngst wlan21\ngp 192.0.2.1/32\ngr 10.0.0.0/8\ngr 224.0.0.0/4\nif [ $n -eq 0 ] && [ $m -eq 0 ]"} {
		if !strings.Contains(s, w) {
			t.Errorf("script lacks %q", w)
		}
	}
	// nothing guest-related without a guest VAP
	if s, _ := AirPortPFScript(nil, iso, pfAll); strings.Contains(s, "\ngst ") || strings.Contains(s, "\ngr ") || strings.Contains(s, "\ngp ") {
		t.Error("guest calls without a guest")
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		f := filepath.Join(t.TempDir(), "g.sh")
		if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("sh -n: %v\n%s", err, out)
		}
	}
}
