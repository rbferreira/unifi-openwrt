// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

const (
	upSSID = "vwire-0123456789abcdef"
	upPSK  = "placeholder-passphrase-0123456"
)

func upCfg(kv ...string) translate.Cfg {
	c := translate.Cfg{"mesh.status": "enabled", "mesh.essid": upSSID, "mesh.psk": upPSK}
	for i := 0; i < len(kv); i += 2 {
		c[kv[i]] = kv[i+1]
	}
	return c
}

// The standard WPA PMK test vector (IEEE 802.11i, annex H.4).
func TestUplinkPMKVector(t *testing.T) {
	k, err := pbkdf2.Key(sha1.New, "password", []byte("IEEE"), 4096, 32)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(k), "f42c6fc52df0ebef9ebb4b90b38a5f902e83fe1b135a70e23aed762e9710a12e"; got != want {
		t.Fatalf("PMK = %s, want %s", got, want)
	}
	// and through the function: the same vector, passphrase and SSID of the test
	text, _, err := AirPortUplinkConf(upCfg("mesh.essid", "IEEE", "mesh.psk", "password"))
	if err != nil {
		t.Fatal(err)
	}
	want := "ctrl_interface=/var/run/wpa_own\nap_scan=1\nnetwork={\n\tssid=\"IEEE\"\n\tscan_ssid=1\n\tkey_mgmt=WPA-PSK\n" +
		"\tproto=RSN\n\tpairwise=CCMP\n\tgroup=CCMP\n\tpsk=f42c6fc52df0ebef9ebb4b90b38a5f902e83fe1b135a70e23aed762e9710a12e\n}\n"
	if text != want {
		t.Errorf("text:\n%q\nwant:\n%q", text, want)
	}
}

func TestAirPortUplinkConf(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cfg    translate.Cfg
		ok     bool   // text wanted
		note   string // substring of the note
		errHas string // substring of the error
	}{
		{"enabled", upCfg(), true, "", ""},
		{"mesh disabled", upCfg("mesh.status", "disabled"), false, "not enabled", ""},
		{"mesh status missing", translate.Cfg{"mesh.essid": upSSID, "mesh.psk": upPSK}, false, "not enabled", ""},
		{"disabled with nothing else", translate.Cfg{"mesh.status": "disabled"}, false, "not enabled", ""},
		{"empty essid", upCfg("mesh.essid", ""), false, "", "mesh.essid is empty"},
		{"empty psk", upCfg("mesh.psk", ""), false, "", "mesh.psk is empty"},
		{"quote in essid", upCfg("mesh.essid", `a"b`), false, "", "mesh.essid"},
		{"newline in essid", upCfg("mesh.essid", "a\nb"), false, "", "mesh.essid"},
		{"control char in essid", upCfg("mesh.essid", "a\x01b"), false, "", "mesh.essid"},
		{"non-ASCII essid", upCfg("mesh.essid", "caf\u00e9"), false, "", "mesh.essid"},
		{"quote in psk", upCfg("mesh.psk", `abcdefgh"ijk`), false, "", "mesh.psk"},
		{"newline in psk", upCfg("mesh.psk", "abcdefgh\nijk"), false, "", "mesh.psk"},
		{"tab in psk", upCfg("mesh.psk", "abcdefgh\tijk"), false, "", "mesh.psk"},
		{"short psk", upCfg("mesh.psk", "1234567"), false, "", "8 to 63"},
		{"long psk", upCfg("mesh.psk", strings.Repeat("x", 64)), false, "", "8 to 63"},
		{"long essid", upCfg("mesh.essid", strings.Repeat("x", 33)), false, "", "32"},
		{"space in essid is fine", upCfg("mesh.essid", "my backhaul"), true, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, note, err := AirPortUplinkConf(tc.cfg)
			switch {
			case tc.errHas != "":
				if err == nil || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.errHas)
				}
				if text != "" {
					t.Error("text returned with an error")
				}
			case err != nil:
				t.Fatal(err)
			case tc.ok:
				if text == "" || note != "" {
					t.Fatalf("text %q note %q", text, note)
				}
				if !strings.HasSuffix(text, "\n}\n") || !strings.Contains(text, "\tssid=\""+tc.cfg["mesh.essid"]+"\"\n") {
					t.Errorf("text:\n%s", text)
				}
			default:
				if text != "" || !strings.Contains(note, tc.note) {
					t.Errorf("text %q note %q", text, note)
				}
			}
			// the passphrase is in no error and no note
			if err != nil && strings.Contains(err.Error(), tc.cfg["mesh.psk"]) && tc.cfg["mesh.psk"] != "" {
				t.Error("error carries the passphrase")
			}
			if strings.Contains(note, tc.cfg["mesh.psk"]) && tc.cfg["mesh.psk"] != "" {
				t.Error("note carries the passphrase")
			}
		})
	}
}

// The PMK of the placeholder pair is what the file carries, and the passphrase is not in it.
func TestAirPortUplinkConfPlaceholder(t *testing.T) {
	text, _, err := AirPortUplinkConf(upCfg())
	if err != nil {
		t.Fatal(err)
	}
	k, _ := pbkdf2.Key(sha1.New, upPSK, []byte(upSSID), 4096, 32)
	if !strings.Contains(text, "\tpsk="+hex.EncodeToString(k)+"\n") {
		t.Errorf("no PMK in:\n%s", text)
	}
	if strings.Contains(text, upPSK) {
		t.Error("the passphrase is in the file")
	}
}

// The example file and the generated one have the same shape: every line but the psk.
func TestAirPortUplinkConfMatchesExample(t *testing.T) {
	ex, err := os.ReadFile(filepath.Join("..", "..", "contrib", "airport", "wpa.conf.example"))
	if err != nil {
		t.Skip("no example file here")
	}
	text, _, err := AirPortUplinkConf(upCfg())
	if err != nil {
		t.Fatal(err)
	}
	a := strings.Split(strings.ReplaceAll(string(ex), "\r\n", "\n"), "\n")
	b := strings.Split(text, "\n")
	if len(a) != len(b) {
		t.Fatalf("%d lines in the example, %d generated", len(a), len(b))
	}
	for i := range a {
		if strings.HasPrefix(a[i], "\tpsk=") {
			if !strings.HasPrefix(b[i], "\tpsk=") {
				t.Errorf("line %d: %q", i, b[i])
			}
			continue
		}
		if a[i] != b[i] {
			t.Errorf("line %d: example %q, generated %q", i, a[i], b[i])
		}
	}
}

func TestAirPortUplinkScriptText(t *testing.T) {
	text, _, _ := AirPortUplinkConf(upCfg())
	s, err := AirPortUplinkScript(text)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"PATH=/bin:/sbin:/usr/bin:/usr/sbin\numask 077", "cat > $T <<'UOW_EOF_WPA'\n" + text + "UOW_EOF_WPA\n",
		"cp $F $F.prev", "mv $N $F", `tail -n 1 $N`, "uplink credentials updated from the controller (used at the next join)",
		"D=/mnt/Flash/mesh", "R=/mnt/Memory/uowap"} {
		if !strings.Contains(s, w) {
			t.Errorf("script lacks %q", w)
		}
	}
	// the comparison comes before any write to the flash
	if i, j := strings.Index(s, `[ "$(cat $F)" = "$(cat $T)" ]`), strings.Index(s, "cp $F $F.prev"); i < 0 || j < i {
		t.Error("the compare is not before the first write")
	}
	// never touches the uplink
	for _, bad := range []string{"wpa_supplicant", "wlan9", "ifconfig", "brconfig", "kill ", "wpa.pid"} {
		if strings.Contains(s, bad) {
			t.Errorf("script mentions %q", bad)
		}
	}
	// none of the tools the unit lacks
	for _, bad := range []string{"grep", "awk", "| head", "| tr ", "| cut", "cmp ", "touch "} {
		if strings.Contains(s, bad) {
			t.Errorf("script uses %q, which the unit lacks", bad)
		}
	}
	// the text cannot end its own heredoc early
	tricky := text + "UOW_EOF_WPA\n}\n"
	s2, err := AirPortUplinkScript(tricky)
	if err != nil || !strings.Contains(s2, "<<'UOW_EOF_WPA_'\n") {
		t.Errorf("delimiter not lengthened: %v", err)
	}
	for _, bad := range []string{"", "\n", "   \n", "network={\n"} {
		if s, err := AirPortUplinkScript(bad); err == nil {
			t.Errorf("accepted %q:\n%s", bad, s)
		}
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		for _, x := range []string{s, s2} {
			f := filepath.Join(t.TempDir(), "u.sh")
			if err := os.WriteFile(f, []byte(x), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
				t.Errorf("sh -n: %v\n%s", err, out)
			}
		}
	}
}

// uplinkHost runs the script with the unit's two directories swapped for temporary ones.
type uplinkHost struct {
	t       *testing.T
	sh, dir string
}

func newUplinkHost(t *testing.T) *uplinkHost {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host")
	}
	dir := filepath.ToSlash(t.TempDir())
	if strings.ContainsAny(dir, " \t'\"$`\\") {
		t.Skipf("temporary directory %q has characters the script would not survive", dir)
	}
	for _, d := range []string{"mesh", "run"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &uplinkHost{t: t, sh: sh, dir: dir}
}

func (h *uplinkHost) path(rel string) string { return filepath.Join(h.dir, filepath.FromSlash(rel)) }

func (h *uplinkHost) has(rel string) bool {
	_, err := os.Stat(h.path(rel))
	return err == nil
}

func (h *uplinkHost) text(rel string) string {
	b, _ := os.ReadFile(h.path(rel))
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func (h *uplinkHost) put(rel, s string) {
	h.t.Helper()
	if err := os.WriteFile(h.path(rel), []byte(s), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// run builds the script for text and runs it; it returns the report lines.
func (h *uplinkHost) run(text string) []string {
	h.t.Helper()
	s, err := AirPortUplinkScript(text)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, r := range [][2]string{{"PATH=/bin:/sbin:/usr/bin:/usr/sbin", "PATH=$PATH"}, {apUplinkDir, h.dir + "/mesh"}, {apRunDir, h.dir + "/run"}} {
		if !strings.Contains(s, r[0]) {
			h.t.Fatalf("script lacks %q", r[0])
		}
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	h.put("u.sh", s)
	cmd := exec.Command(h.sh, "u.sh")
	cmd.Dir = h.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("script: %v\n%s", err, out)
	}
	return reportLines(out)
}

func TestAirPortUplinkScriptRun(t *testing.T) {
	h := newUplinkHost(t)
	old, _, _ := AirPortUplinkConf(upCfg())
	neu, _, _ := AirPortUplinkConf(upCfg("mesh.psk", "another-placeholder-passphrase-9"))
	done := "mesh: uplink credentials updated from the controller (used at the next join)"

	t.Run("missing file is created", func(t *testing.T) {
		rep := h.run(old)
		if len(rep) != 1 || !strings.Contains(rep[0], "no wpa.conf on the unit") {
			t.Errorf("report %q", rep)
		}
		if h.text("mesh/wpa.conf") != old {
			t.Errorf("wpa.conf:\n%s", h.text("mesh/wpa.conf"))
		}
		if h.has("mesh/wpa.conf.prev") || h.has("mesh/wpa.conf.new") || h.has("run/wpa.want") {
			t.Error("stray files")
		}
	})
	t.Run("equal: no output, no write", func(t *testing.T) {
		// a rewrite would move the modification time, set an hour back
		ago := time.Now().Add(-time.Hour)
		os.Chtimes(h.path("mesh/wpa.conf"), ago, ago)
		before, _ := os.Stat(h.path("mesh/wpa.conf"))
		rep := h.run(old)
		if len(rep) != 0 {
			t.Errorf("report %q", rep)
		}
		after, _ := os.Stat(h.path("mesh/wpa.conf"))
		if !after.ModTime().Equal(before.ModTime()) {
			t.Error("wpa.conf was written")
		}
		if h.has("mesh/wpa.conf.prev") || h.has("mesh/wpa.conf.new") || h.has("run/wpa.want") {
			t.Error("stray files")
		}
	})
	t.Run("different: previous kept, new in place", func(t *testing.T) {
		rep := h.run(neu)
		if !reflect.DeepEqual(rep, []string{done}) {
			t.Errorf("report %q", rep)
		}
		if h.text("mesh/wpa.conf") != neu || h.text("mesh/wpa.conf.prev") != old {
			t.Errorf("wpa.conf:\n%s\nprev:\n%s", h.text("mesh/wpa.conf"), h.text("mesh/wpa.conf.prev"))
		}
		if h.has("mesh/wpa.conf.new") || h.has("run/wpa.want") {
			t.Error("stray files")
		}
	})
	t.Run("equal again: prev untouched", func(t *testing.T) {
		if rep := h.run(neu); len(rep) != 0 {
			t.Errorf("report %q", rep)
		}
		if h.text("mesh/wpa.conf.prev") != old {
			t.Error("prev changed")
		}
	})
	t.Run("a file that differs only in a trailing blank line is the same", func(t *testing.T) {
		// command substitution drops trailing newlines: no flash write for that
		h.put("mesh/wpa.conf", neu+"\n\n")
		if rep := h.run(neu); len(rep) != 0 {
			t.Errorf("report %q", rep)
		}
	})
	t.Run("a hand-edited file is replaced and kept", func(t *testing.T) {
		h.put("mesh/wpa.conf", "network={\n\tssid=\"x\"\n}\n")
		rep := h.run(old)
		if !reflect.DeepEqual(rep, []string{done}) || h.text("mesh/wpa.conf") != old || !strings.Contains(h.text("mesh/wpa.conf.prev"), `ssid="x"`) {
			t.Errorf("report %q\nwpa.conf:\n%s\nprev:\n%s", rep, h.text("mesh/wpa.conf"), h.text("mesh/wpa.conf.prev"))
		}
	})
	t.Run("the directory is created when missing", func(t *testing.T) {
		os.RemoveAll(h.path("mesh"))
		rep := h.run(old)
		if len(rep) != 1 || h.text("mesh/wpa.conf") != old {
			t.Errorf("report %q", rep)
		}
	})
}

// The wanted text is never empty by the time a script exists: Go refuses first.
func TestAirPortUplinkNoScriptForNothing(t *testing.T) {
	for _, c := range []translate.Cfg{
		upCfg("mesh.psk", ""), upCfg("mesh.essid", ""), upCfg("mesh.status", "disabled"), {},
	} {
		text, _, _ := AirPortUplinkConf(c)
		if s, err := AirPortUplinkScript(text); err == nil {
			t.Errorf("a script for %v:\n%s", c, s)
		}
	}
}
