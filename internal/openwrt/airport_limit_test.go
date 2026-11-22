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

func limVAPs() []AirPortVAP {
	return []AirPortVAP{
		{Name: "wlan10", SSID: "Home", Devname: "wifi0ap0"},
		{Name: "wlan11", SSID: "Kids", Devname: "wifi0ap1"},
		{Name: "wlan20", SSID: "Home", Devname: "wifi1ap0"},
		{Name: "wlan21", SSID: "Kids", Devname: "wifi1ap1"},
	}
}

func TestAirPortLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lims  []translate.VAPLimit
		want  []PFLimit
		notes []string // substrings, in order
	}{
		{"none", nil, nil, nil},
		{"download on both bands", []translate.VAPLimit{
			{Devname: "wifi1ap1", DownKbps: 5000}, {Devname: "wifi0ap1", DownKbps: 2000}},
			[]PFLimit{{"wlan11", 2000}, {"wlan21", 5000}}, []string{"shared by its clients"}},
		{"upload only", []translate.VAPLimit{{Devname: "wifi0ap0", UpKbps: 1000}},
			nil, []string{`upload limit ignored on WLAN(s) "Home"`}},
		{"both, one WLAN on two bands", []translate.VAPLimit{
			{Devname: "wifi0ap1", DownKbps: 2000, UpKbps: 500}, {Devname: "wifi1ap1", DownKbps: 2000, UpKbps: 500}},
			[]PFLimit{{"wlan11", 2000}, {"wlan21", 2000}}, []string{"shared by", `WLAN(s) "Kids":`}},
		{"unknown devname", []translate.VAPLimit{{Devname: "wifi0ap7", DownKbps: 100}},
			nil, []string{"wifi0ap7: no VAP"}},
		{"1 Gbit or more is no cap", []translate.VAPLimit{{Devname: "wifi0ap0", DownKbps: 1000000}},
			nil, []string{`"Home" is 1 Gbit/s or more`}},
		{"lowest wins", []translate.VAPLimit{{Devname: "wifi0ap0", DownKbps: 900}, {Devname: "wifi0ap0", DownKbps: 300}},
			[]PFLimit{{"wlan10", 300}}, []string{"shared by"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, notes := AirPortLimits(tc.lims, limVAPs())
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("limits = %v, want %v", got, tc.want)
			}
			if len(notes) != len(tc.notes) {
				t.Fatalf("notes = %q, want %d", notes, len(tc.notes))
			}
			for i, w := range tc.notes {
				if !strings.Contains(notes[i], w) {
					t.Errorf("note %d = %q, want it to contain %q", i, notes[i], w)
				}
			}
		})
	}
}

func TestAirPortLimitsFromConfig(t *testing.T) {
	cfg := translate.ParseCfg(`qos.status=enabled
qos.vap.1.devname=wifi0ap1
qos.vap.1.dwnlink.maxspeed=2000
qos.vap.1.uplink.1.maxspeed=1000
qos.vap.2.devname=wifi1ap1
qos.vap.2.dwnlink.maxspeed=2000
`)
	got, notes := AirPortLimits(translate.QoSLimits(cfg), limVAPs())
	if want := []PFLimit{{"wlan11", 2000}, {"wlan21", 2000}}; !reflect.DeepEqual(got, want) {
		t.Errorf("limits = %v, want %v", got, want)
	}
	if len(notes) != 2 {
		t.Errorf("notes = %q", notes)
	}
}

func TestAirPortLimitsScript(t *testing.T) {
	s, err := AirPortLimitsScript([]PFLimit{{"wlan11", 2000}, {"wlan21", 5000}})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"lim wlan11 2000Kb\nlim wlan21 5000Kb\nif [ $n -eq 0 ]", "pfctl -f $T", "pfctl -e", "pfctl -d", "pfctl -F all",
		`echo "pass all"`, "PATH=/bin:/sbin:/usr/bin:/usr/sbin\numask 077"} {
		if !strings.Contains(s, w) {
			t.Errorf("script lacks %q", w)
		}
	}
	// pfctl -e must come after a successful pfctl -f
	if strings.Index(s, "pfctl -e") < strings.Index(s, "pfctl -f") {
		t.Error("pf is enabled before the ruleset is loaded")
	}
	for _, tc := range []struct {
		name string
		l    []PFLimit
	}{
		{"wlan9", []PFLimit{{"wlan9", 100}}},
		{"firmware VAP", []PFLimit{{"wlan0", 100}}},
		{"injection", []PFLimit{{"wlan11;reboot", 100}}},
		{"zero", []PFLimit{{"wlan11", 0}}},
		{"negative", []PFLimit{{"wlan11", -5}}},
		{"over 1 Gbit", []PFLimit{{"wlan11", 1000000}}},
		{"twice", []PFLimit{{"wlan11", 1}, {"wlan11", 2}}},
	} {
		if s, err := AirPortLimitsScript(tc.l); err == nil {
			t.Errorf("%s: accepted:\n%s", tc.name, s)
		}
	}
	empty, err := AirPortLimitsScript(nil)
	if err != nil || strings.Contains(empty, "lim wlan") {
		t.Errorf("empty script: %v", err)
	}
	for _, x := range []string{s, empty} {
		if strings.Contains(x, "wlan9") {
			t.Error("script mentions wlan9")
		}
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		for _, x := range []string{s, empty} {
			f := filepath.Join(t.TempDir(), "l.sh")
			if err := os.WriteFile(f, []byte(x), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
				t.Errorf("sh -n: %v\n%s", err, out)
			}
		}
	}
}

// The apply script must forget the marker of a VAP it stops or starts.
func TestAirPortApplyForgetsPFMarker(t *testing.T) {
	s := AirPortApplyScript(nil)
	if !strings.Contains(s, "$D/$1.pfq") || !strings.Contains(s, "$D/$v.pfq") {
		t.Error("stop/start do not remove the .pfq marker")
	}
}

const pfStubIfconfig = `case " $(cat $S/up) " in *" $1 "*)
	echo "$1: flags=8843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST> mtu 1500"
	echo "	status: running";;
*) echo "ifconfig: $1: no such interface" >&2; exit 1;;
esac
`

const pfStubPfctl = `echo "pfctl $*" >> $S/log
case "$1" in
-s) if [ -e $S/enabled ]; then echo "Status: Enabled for 0 days 00:01:00"; else echo "Status: Disabled"; fi;;
-f) if [ -e $S/failload ]; then echo "pfctl: syntax error"; echo "second line"; exit 1; fi
	cat "$2" > $S/loaded;;
-e) if [ -e $S/enabled ]; then echo "pfctl: pf already enabled"; exit 1; fi
	[ -e $S/failenable ] && { echo "pfctl: no permission"; exit 1; }
	: > $S/enabled;;
-d) rm -f $S/enabled;;
-F) rm -f $S/loaded;;
esac
exit 0
`

// pfHost runs the limits script against a fake ifconfig and pfctl.
type pfHost struct {
	t       *testing.T
	sh, dir string
	env     []string
}

func newPFHost(t *testing.T) *pfHost {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host")
	}
	dir := t.TempDir()
	root := filepath.ToSlash(dir)
	if strings.ContainsAny(root, " \t'\"$`\\") {
		t.Skipf("temporary directory %q has characters the script would not survive", root)
	}
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{bin, filepath.Join(dir, "s"), filepath.Join(dir, "run")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"ifconfig": pfStubIfconfig, "pfctl": pfStubPfctl} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h := &pfHost{t: t, sh: sh, dir: root}
	h.env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "S="+root+"/s")
	h.up("wlan11", "wlan21")
	if out, err := h.exec("pfctl -s info"); err != nil || !strings.Contains(out, "Disabled") {
		t.Skipf("stub commands do not run under %s here: %q %v", sh, out, err)
	}
	h.flag("log", false)
	return h
}

func (h *pfHost) exec(cmdline string) (string, error) {
	cmd := exec.Command(h.sh, "-c", cmdline)
	cmd.Dir, cmd.Env = h.dir, h.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (h *pfHost) up(vaps ...string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, "s", "up"), []byte(strings.Join(vaps, " ")), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *pfHost) flag(name string, on bool) {
	h.t.Helper()
	f := filepath.Join(h.dir, "s", name)
	if on {
		os.WriteFile(f, nil, 0o644)
	} else {
		os.Remove(f)
	}
}

func (h *pfHost) has(rel string) bool {
	_, err := os.Stat(filepath.Join(h.dir, filepath.FromSlash(rel)))
	return err == nil
}

func (h *pfHost) text(rel string) string {
	b, _ := os.ReadFile(filepath.Join(h.dir, filepath.FromSlash(rel)))
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// run returns the report lines and the pfctl calls this run made.
func (h *pfHost) run(lims []PFLimit) (report []string, pfctl []string) {
	h.t.Helper()
	s, err := AirPortLimitsScript(lims)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, r := range [][2]string{{"PATH=/bin:/sbin:/usr/bin:/usr/sbin", "PATH=$PATH"}, {apRunDir, h.dir + "/run"}} {
		if !strings.Contains(s, r[0]) {
			h.t.Fatalf("script lacks %q", r[0])
		}
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	if err := os.WriteFile(filepath.Join(h.dir, "l.sh"), []byte(s), 0o600); err != nil {
		h.t.Fatal(err)
	}
	cmd := exec.Command(h.sh, "l.sh")
	cmd.Dir, cmd.Env = h.dir, h.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("script: %v\n%s", err, out)
	}
	report = reportLines(out)
	log := strings.Split(strings.TrimSpace(h.text("s/log")), "\n")
	h.flag("log", false)
	for _, l := range log {
		if l != "" {
			pfctl = append(pfctl, l)
		}
	}
	return report, pfctl
}

func TestAirPortLimitsRun(t *testing.T) {
	h := newPFHost(t)
	two := []PFLimit{{"wlan11", 2000}, {"wlan21", 5000}}
	wantRules := "altq on wlan11 hfsc bandwidth 1Gb queue { lim_wlan11 }\n" +
		"queue lim_wlan11 bandwidth 2000Kb hfsc(default upperlimit 2000Kb)\n" +
		"altq on wlan21 hfsc bandwidth 1Gb queue { lim_wlan21 }\n" +
		"queue lim_wlan21 bandwidth 5000Kb hfsc(default upperlimit 5000Kb)\n" +
		"pass all\n"

	t.Run("nothing wanted, nothing of ours: pf untouched", func(t *testing.T) {
		rep, calls := h.run(nil)
		if len(rep) != 0 || len(calls) != 0 {
			t.Errorf("report %q, pfctl %q", rep, calls)
		}
	})
	t.Run("load then enable", func(t *testing.T) {
		rep, calls := h.run(two)
		if !reflect.DeepEqual(rep, []string{"pf: 2 limited VAP(s)"}) {
			t.Errorf("report %q", rep)
		}
		if got := h.text("run/pf.conf"); got != wantRules {
			t.Errorf("pf.conf:\n%s", got)
		}
		if strings.Contains(h.text("run/pf.conf"), "block") || strings.Contains(h.text("s/loaded"), "block") {
			t.Error("the ruleset blocks")
		}
		var f, e = -1, -1
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
		if !h.has("run/wlan11.pfq") || !h.has("run/wlan21.pfq") || h.has("run/pf.new") {
			t.Error("markers or temp file wrong")
		}
	})
	t.Run("again: no change, no load", func(t *testing.T) {
		rep, calls := h.run(two)
		if len(rep) != 0 {
			t.Errorf("report %q", rep)
		}
		for _, c := range calls {
			if strings.HasPrefix(c, "pfctl -f") || c == "pfctl -e" {
				t.Errorf("unneeded call %q", c)
			}
		}
	})
	t.Run("a VAP made again is loaded again", func(t *testing.T) {
		os.Remove(filepath.Join(h.dir, "run", "wlan21.pfq"))
		rep, _ := h.run(two)
		if len(rep) != 1 || !h.has("run/wlan21.pfq") {
			t.Errorf("report %q", rep)
		}
	})
	t.Run("pf turned off by someone: enabled again", func(t *testing.T) {
		h.flag("enabled", false)
		rep, _ := h.run(two)
		if len(rep) != 1 || !h.has("s/enabled") {
			t.Errorf("report %q", rep)
		}
	})
	t.Run("a limit changes", func(t *testing.T) {
		rep, _ := h.run([]PFLimit{{"wlan11", 3000}, {"wlan21", 5000}})
		if len(rep) != 1 || !strings.Contains(h.text("run/pf.conf"), "3000Kb") {
			t.Errorf("report %q", rep)
		}
	})
	t.Run("a capped VAP that does not run is left out", func(t *testing.T) {
		h.up("wlan11")
		rep, _ := h.run(two)
		if !reflect.DeepEqual(rep, []string{"pf: 1 limited VAP(s)"}) || strings.Contains(h.text("run/pf.conf"), "wlan21") {
			t.Errorf("report %q\n%s", rep, h.text("run/pf.conf"))
		}
		h.up("wlan11", "wlan21")
	})
	t.Run("a failed load leaves pf and the file as they were", func(t *testing.T) {
		old := h.text("run/pf.conf")
		h.flag("failload", true)
		rep, calls := h.run(two)
		h.flag("failload", false)
		if len(rep) != 1 || rep[0] != "pf failed: load: pfctl: syntax error" {
			t.Errorf("report %q", rep)
		}
		if h.text("run/pf.conf") != old || h.has("run/pf.new") {
			t.Error("files changed")
		}
		for _, c := range calls {
			if c == "pfctl -e" || c == "pfctl -d" {
				t.Errorf("pf touched after a failed load: %q", calls)
			}
		}
	})
	t.Run("nothing wanted any more: off", func(t *testing.T) {
		rep, calls := h.run(nil)
		if !reflect.DeepEqual(rep, []string{"pf: off"}) {
			t.Errorf("report %q", rep)
		}
		if h.has("run/pf.conf") || h.has("run/wlan11.pfq") || h.has("s/enabled") || h.has("s/loaded") {
			t.Error("not undone")
		}
		if !reflect.DeepEqual(calls[len(calls)-2:], []string{"pfctl -d", "pfctl -F all"}) {
			t.Errorf("calls %q", calls)
		}
	})
	t.Run("a failed first load never enables pf", func(t *testing.T) {
		h.flag("failload", true)
		rep, calls := h.run(two)
		h.flag("failload", false)
		if len(rep) != 1 || !strings.HasPrefix(rep[0], "pf failed: ") || h.has("s/enabled") || h.has("run/pf.conf") {
			t.Errorf("report %q", rep)
		}
		for _, c := range calls {
			if c == "pfctl -e" {
				t.Error("enabled")
			}
		}
	})
	t.Run("enable refused", func(t *testing.T) {
		h.flag("failenable", true)
		rep, _ := h.run(two)
		h.flag("failenable", false)
		if len(rep) != 1 || rep[0] != "pf failed: enable: pfctl: no permission" || h.has("run/wlan11.pfq") {
			t.Errorf("report %q", rep)
		}
		if rep, _ := h.run(two); len(rep) != 1 || rep[0] != "pf: 2 limited VAP(s)" {
			t.Errorf("retry: %q", rep)
		}
	})
}
