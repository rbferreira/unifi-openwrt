// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAirPortIsolation(t *testing.T) {
	vaps := []AirPortVAP{
		{Name: "wlan10", Isolate: false},
		{Name: "wlan12", Isolate: true, VLAN: 77, UpIf: "wlan5"},
		{Name: "wlan21", Isolate: true, VLAN: 77, UpIf: "wlan5"},
		{Name: "wlan22", Isolate: true},
	}
	got, notes := AirPortIsolation(vaps)
	want := []PFIso{{"wlan12", "bridge77"}, {"wlan21", "bridge77"}, {"wlan22", "bridge0"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "across radios (pf on the bridge); hosts on the wired side stay reachable") {
		t.Errorf("notes %q", notes)
	}
	if got, notes := AirPortIsolation(vaps[:1]); got != nil || notes != nil {
		t.Errorf("nothing isolated: %+v %q", got, notes)
	}
}

func TestAirPortBridgeName(t *testing.T) {
	for b, ok := range map[string]bool{
		"bridge0": true, "bridge2": true, "bridge77": true, "bridge4094": true,
		"bridge1": false, "bridge4095": false, "bridge": false, "bridge01": false, "bridge00": false,
		"bridge7a": false, "bridge7;reboot": false, "bridge-1": false, "vlan77": false, "wlan5": false, "": false,
		"bridge99999": false,
	} {
		if apBridgeName(b) != ok {
			t.Errorf("apBridgeName(%q) = %v", b, !ok)
		}
	}
}

func TestAirPortPFScriptRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		iso  []PFIso
	}{
		{"firmware VAP", []PFIso{{"wlan0", "bridge0"}}},
		{"injection", []PFIso{{"wlan12;reboot", "bridge0"}}},
		{"bridge injection", []PFIso{{"wlan12", "bridge0;reboot"}}},
		{"bridge1", []PFIso{{"wlan12", "bridge1"}}},
		{"uplink as bridge", []PFIso{{"wlan12", "wlan5"}}},
		{"twice", []PFIso{{"wlan12", "bridge0"}, {"wlan12", "bridge0"}}},
	} {
		if s, err := AirPortPFScript(nil, tc.iso); err == nil {
			t.Errorf("%s: accepted:\n%s", tc.name, s)
		}
	}
}

func TestAirPortPFScriptShape(t *testing.T) {
	iso := []PFIso{{"wlan12", "bridge77"}, {"wlan21", "bridge77"}, {"wlan22", "bridge0"}}
	s, err := AirPortPFScript([]PFLimit{{"wlan11", 2000}}, iso)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"lim wlan11 2000Kb\nblk wlan12 bridge77\nblk wlan21 bridge77\nblk wlan22 bridge0\nif [ $n -eq 0 ] && [ $m -eq 0 ]",
		"brconfig $b ipf", "brconfig $b -ipf"} {
		if !strings.Contains(s, w) {
			t.Errorf("script lacks %q", w)
		}
	}
	// "pass all" is written before the blocks (the last matching rule wins)
	if a, b := strings.Index(s, `echo "pass all"`), strings.Index(s, `echo "block out on $v all"`); a < 0 || b < a {
		t.Error("block lines are not after pass all")
	}
	// only the daemon's VAPs, its own bridges and no uplink
	for _, bad := range []string{"wlan0", "wlan3", "wlan5", "wlan9", "vlan", "wlan1?.ipf", "reboot"} {
		if strings.Contains(s, bad) {
			t.Errorf("script mentions %q", bad)
		}
	}
	// every interface the script names is a daemon VAP or a bridge it was told of
	for _, f := range strings.Fields(s) {
		if strings.HasPrefix(f, "wlan") && !apVAPName.MatchString(strings.Trim(f, "?;\"")) && !strings.HasPrefix(f, "wlan1?") && !strings.HasPrefix(f, "wlan2?") {
			t.Errorf("script names %q", f)
		}
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		f := filepath.Join(t.TempDir(), "i.sh")
		if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("sh -n: %v\n%s", err, out)
		}
	}
}

// The ruleset for each mix: queues, pass all, then the blocks of the running
// isolated VAPs, in that order.
func TestAirPortPFRuleset(t *testing.T) {
	lim := []PFLimit{{"wlan11", 2000}}
	iso := []PFIso{{"wlan12", "bridge77"}, {"wlan21", "bridge77"}}
	limRules := "altq on wlan11 hfsc bandwidth 1Gb queue { lim_wlan11 }\n" +
		"queue lim_wlan11 bandwidth 2000Kb hfsc(default upperlimit 2000Kb)\n"
	isoRules := "block out on wlan12 all\nblock out on wlan21 all\n"
	for _, tc := range []struct {
		name   string
		lims   []PFLimit
		iso    []PFIso
		ours   bool // the unit already has a ruleset of ours
		rules  string
		report []string
		off    bool
	}{
		{"limits only", lim, nil, false, limRules + "pass all\n", []string{"pf: 1 limited VAP(s)"}, false},
		{"isolation only", nil, iso, false, "pass all\n" + isoRules,
			[]string{"bridge77: ipf on", "pf: 2 isolated VAP(s)"}, false},
		{"both", lim, iso, false, limRules + "pass all\n" + isoRules,
			[]string{"bridge77: ipf on", "pf: 1 limited VAP(s), 2 isolated VAP(s)"}, false},
		{"neither, our file there", nil, nil, true, "", []string{"pf: off"}, true},
		{"neither, no file", nil, nil, false, "", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPFHost(t)
			h.up("wlan11", "wlan12", "wlan21", "bridge77")
			if tc.ours {
				h.runPF(lim, iso)
			}
			h.flag("log", false)
			rep, calls := h.runPF(tc.lims, tc.iso)
			if tc.ours {
				// the pf line is last; the bridge line comes first
				rep = rep[len(rep)-1:]
				if !tc.off {
					t.Fatal("bad case")
				}
			}
			if !reflect.DeepEqual(rep, tc.report) && !(len(rep) == 0 && len(tc.report) == 0) {
				t.Errorf("report %q, want %q", rep, tc.report)
			}
			switch {
			case tc.off:
				if h.has("run/pf.conf") || h.has("s/enabled") || h.has("run/wlan12.pfq") {
					t.Error("pf not turned off")
				}
				if !reflect.DeepEqual(calls[len(calls)-2:], []string{"pfctl -d", "pfctl -F all"}) {
					t.Errorf("calls %q", calls)
				}
			case tc.rules == "":
				if len(calls) != 0 || h.has("run/pf.conf") {
					t.Errorf("pf touched: %q", calls)
				}
			default:
				if got := h.text("run/pf.conf"); got != tc.rules {
					t.Errorf("pf.conf:\n%s", got)
				}
				if !h.has("s/enabled") {
					t.Error("pf not enabled")
				}
				for _, v := range tc.iso {
					if !h.has("run/" + v.VAP + ".pfq") {
						t.Errorf("no marker for %s", v.VAP)
					}
				}
			}
		})
	}
}

// pf must come on, with the ruleset loaded first, for isolation alone; and only
// the VAPs that run get their lines.
func TestAirPortPFIsolationRun(t *testing.T) {
	h := newPFHost(t)
	h.up("wlan12", "wlan21", "bridge77")
	iso := []PFIso{{"wlan12", "bridge77"}, {"wlan21", "bridge77"}}
	rep, calls := h.runPF(nil, iso)
	if !reflect.DeepEqual(rep, []string{"bridge77: ipf on", "pf: 2 isolated VAP(s)"}) {
		t.Errorf("report %q", rep)
	}
	f, e := -1, -1
	for i, c := range calls {
		switch c {
		case "pfctl -f " + h.dir + "/run/pf.new":
			f = i
		case "pfctl -e":
			e = i
		}
	}
	if f < 0 || e < f {
		t.Errorf("load/enable order: %q", calls)
	}
	if !h.has("run/bridge77.ipf") || !h.has("s/ipf.bridge77") {
		t.Error("bridge flag or marker missing")
	}

	t.Run("again: nothing", func(t *testing.T) {
		h.flag("log", false)
		rep, calls := h.runPF(nil, iso)
		if len(rep) != 0 {
			t.Errorf("report %q", rep)
		}
		for _, c := range calls {
			if strings.HasPrefix(c, "pfctl -f") || c == "pfctl -e" {
				t.Errorf("unneeded call %q", c)
			}
		}
	})
	t.Run("a recreated isolated VAP gets its rule back", func(t *testing.T) {
		os.Remove(filepath.Join(h.dir, "run", "wlan21.pfq"))
		rep, calls := h.runPF(nil, iso)
		if !reflect.DeepEqual(rep, []string{"pf: 2 isolated VAP(s)"}) || !h.has("run/wlan21.pfq") {
			t.Errorf("report %q", rep)
		}
		loaded := false
		for _, c := range calls {
			loaded = loaded || strings.HasPrefix(c, "pfctl -f")
		}
		if !loaded {
			t.Error("ruleset not loaded again")
		}
	})
	t.Run("pf turned off by someone: enabled again", func(t *testing.T) {
		h.flag("enabled", false)
		rep, _ := h.runPF(nil, iso)
		if len(rep) != 1 || !h.has("s/enabled") {
			t.Errorf("report %q", rep)
		}
	})
	t.Run("an isolated VAP that does not run is left out", func(t *testing.T) {
		h.up("wlan12", "bridge77")
		rep, _ := h.runPF(nil, iso)
		if !reflect.DeepEqual(rep, []string{"pf: 1 isolated VAP(s)"}) || strings.Contains(h.text("run/pf.conf"), "wlan21") {
			t.Errorf("report %q\n%s", rep, h.text("run/pf.conf"))
		}
		h.up("wlan12", "wlan21", "bridge77")
	})
	t.Run("a failed load: no pf change, no bridge change", func(t *testing.T) {
		old := h.text("run/pf.conf")
		h.flag("failload", true)
		rep, _ := h.runPF(nil, iso)
		h.flag("failload", false)
		if len(rep) != 1 || !strings.HasPrefix(rep[0], "pf failed: load") || h.text("run/pf.conf") != old {
			t.Errorf("report %q", rep)
		}
	})
	t.Run("none left: bridge and pf off", func(t *testing.T) {
		rep, _ := h.runPF(nil, nil)
		if !reflect.DeepEqual(rep, []string{"bridge77: ipf off", "pf: off"}) {
			t.Errorf("report %q", rep)
		}
		if h.has("s/ipf.bridge77") || h.has("run/bridge77.ipf") || h.has("run/pf.conf") {
			t.Error("not undone")
		}
	})
}

// The bridge flag: set on the wanted bridges, cleared only where the daemon set it.
func TestAirPortBridgeIPF(t *testing.T) {
	two := []PFIso{{"wlan12", "bridge77"}, {"wlan22", "bridge77"}}
	brCalls := func(h *pfHost) []string {
		l := strings.Split(strings.TrimSpace(h.text("s/brlog")), "\n")
		h.flag("brlog", false)
		var out []string
		for _, c := range l {
			if strings.HasSuffix(c, " ipf") || strings.HasSuffix(c, " -ipf") {
				out = append(out, c)
			}
		}
		return out
	}
	t.Run("the same VLAN on two radios: set once", func(t *testing.T) {
		h := newPFHost(t)
		h.up("wlan12", "wlan22", "bridge77")
		rep, _ := h.runPF(nil, two)
		if !reflect.DeepEqual(rep, []string{"bridge77: ipf on", "pf: 2 isolated VAP(s)"}) {
			t.Errorf("report %q", rep)
		}
		if got := brCalls(h); !reflect.DeepEqual(got, []string{"brconfig bridge77 ipf"}) {
			t.Errorf("brconfig calls %q", got)
		}
		h.runPF(nil, two)
		if got := brCalls(h); len(got) != 0 {
			t.Errorf("second run called %q", got)
		}
	})
	t.Run("one radio's VAP gone: the other keeps the flag", func(t *testing.T) {
		h := newPFHost(t)
		h.up("wlan12", "wlan22", "bridge77")
		h.runPF(nil, two)
		brCalls(h)
		h.up("wlan12", "bridge77")
		h.runPF(nil, two[:1])
		if !h.has("s/ipf.bridge77") || !h.has("run/bridge77.ipf") {
			t.Error("flag lost")
		}
	})
	t.Run("bridge0 and a bridgeN, each its own", func(t *testing.T) {
		h := newPFHost(t)
		h.up("wlan12", "wlan22", "bridge0", "bridge77")
		iso := []PFIso{{"wlan12", "bridge0"}, {"wlan22", "bridge77"}}
		rep, _ := h.runPF(nil, iso)
		if !reflect.DeepEqual(rep, []string{"bridge0: ipf on", "bridge77: ipf on", "pf: 2 isolated VAP(s)"}) {
			t.Errorf("report %q", rep)
		}
		brCalls(h)
		h.up("wlan12", "bridge0", "bridge77") // wlan22 gone
		rep, _ = h.runPF(nil, iso[:1])
		if !reflect.DeepEqual(rep[:1], []string{"bridge77: ipf off"}) || !h.has("s/ipf.bridge0") || h.has("s/ipf.bridge77") || h.has("run/bridge77.ipf") {
			t.Errorf("report %q", rep)
		}
	})
	t.Run("a flag that was on before is never cleared", func(t *testing.T) {
		h := newPFHost(t)
		h.up("wlan12", "bridge0")
		h.flag("ipf.bridge0", true)
		iso := []PFIso{{"wlan12", "bridge0"}}
		rep, _ := h.runPF(nil, iso)
		if !reflect.DeepEqual(rep, []string{"pf: 1 isolated VAP(s)"}) || h.has("run/bridge0.ipf") {
			t.Errorf("report %q", rep)
		}
		rep, _ = h.runPF(nil, nil)
		if !reflect.DeepEqual(rep, []string{"pf: off"}) || !h.has("s/ipf.bridge0") {
			t.Errorf("report %q", rep)
		}
		for _, c := range brCalls(h) {
			if strings.HasSuffix(c, "-ipf") {
				t.Errorf("cleared a flag that was not ours: %q", c)
			}
		}
	})
	t.Run("a bridge that does not exist is skipped without noise, marker dropped", func(t *testing.T) {
		h := newPFHost(t)
		h.up("wlan12", "bridge77")
		h.runPF(nil, two[:1])
		h.up("wlan12") // bridge77 destroyed
		h.flag("ipf.bridge77", false)
		rep, _ := h.runPF(nil, nil)
		if !reflect.DeepEqual(rep, []string{"pf: off"}) || h.has("run/bridge77.ipf") {
			t.Errorf("report %q", rep)
		}
		h.up()
		rep, _ = h.runPF(nil, two[:1]) // VAP gone with it: nothing runs
		if len(rep) != 0 {
			t.Errorf("report %q", rep)
		}
	})
	t.Run("brconfig refuses: reported, retried, marker kept on clear", func(t *testing.T) {
		h := newPFHost(t)
		h.up("wlan12", "bridge77")
		h.flag("failipf", true)
		rep, _ := h.runPF(nil, two[:1])
		if len(rep) != 2 || rep[0] != "bridge77 failed: brconfig ipf" || h.has("run/bridge77.ipf") {
			t.Errorf("report %q", rep)
		}
		h.flag("failipf", false)
		rep, _ = h.runPF(nil, two[:1])
		if !reflect.DeepEqual(rep, []string{"bridge77: ipf on"}) || !h.has("s/ipf.bridge77") {
			t.Errorf("retry: %q", rep)
		}
		h.flag("failipf", true)
		rep, _ = h.runPF(nil, nil)
		if len(rep) < 1 || rep[0] != "bridge77 failed: brconfig -ipf" || !h.has("run/bridge77.ipf") {
			t.Errorf("clear refused: %q", rep)
		}
	})
	t.Run("a marker with a made-up name is not trusted", func(t *testing.T) {
		h := newPFHost(t)
		h.up("bridge77")
		os.WriteFile(filepath.Join(h.dir, "run", "bridgeX.ipf"), nil, 0o644)
		h.runPF(nil, nil)
		for _, c := range brCalls(h) {
			t.Errorf("brconfig called: %q", c)
		}
		if h.has("run/bridgeX.ipf") {
			t.Error("marker kept")
		}
	})
}

// The apply script already forgets .pfq when it stops or starts a VAP; an
// isolated VAP uses that same marker, so a recreated one gets its block again.
func TestAirPortIsolationMarkerIsPFQ(t *testing.T) {
	s, err := AirPortPFScript(nil, []PFIso{{"wlan12", "bridge0"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "for v in $w $wi; do [ -e $D/$v.pfq ] || need=1; done") ||
		!strings.Contains(s, "for v in $w $wi; do : > $D/$v.pfq; done") {
		t.Error("isolated VAPs do not use the .pfq marker")
	}
}
