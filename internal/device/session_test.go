// SPDX-License-Identifier: GPL-2.0-or-later

package device

import (
	"encoding/json"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/inform"
)

const newKey = "a066a066a066a066a066a066a066a066"

func newSession(t *testing.T) *Session {
	return &Session{
		S: NewState("http://192.168.1.1:8080/inform"),
		Dev: inform.Device{MAC: net.HardwareAddr{2, 0x6f, 0x77, 0x72, 0x74, 1}, IP: "192.168.1.50",
			Netmask: "255.255.255.0", Gateway: "192.168.1.1", Hostname: "t"},
		Boot:     time.Now(),
		StateDir: t.TempDir(),
	}
}

func handle(t *testing.T, x *Session, reply string) Outcome {
	t.Helper()
	o, err := x.Handle([]byte(reply), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// The adoption sequence seen on a real U7PG2 (open-unifi live log):
// setparam{mgmt_cfg with authkey, use_aes_gcm} -> device switches key + GCM.
func TestAdoptionViaMgmtCfg(t *testing.T) {
	x := newSession(t)
	o := handle(t, x, `{"_type":"setparam","mgmt_cfg":"capability=notif,notif-assoc-stat\nselfrun_guest_mode=pass\ncfgversion=1111222233334444\nled_enabled=true\nstun_url=stun://192.168.1.1:3478/\nmgmt_url=https://192.168.1.1:8443/manage/site/default\nauthkey=`+newKey+`\nuse_aes_gcm=true\nreport_crash=true\n","server_time_in_utc":"1"}`)
	if !x.S.Adopted || x.S.Key != newKey || !x.S.GCM || x.S.CfgVersion != "1111222233334444" {
		t.Fatalf("state after adoption: %+v", x.S)
	}
	if !o.Immediate || !o.Changed {
		t.Errorf("outcome %+v", o)
	}
	js, _ := x.Payload(time.Now())
	var m map[string]any
	json.Unmarshal(js, &m)
	if m["default"] != false || m["state"] != 4.0 || m["cfgversion"] != "1111222233334444" || m["x_aes_gcm"] != true {
		t.Errorf("adopted payload: default=%v state=%v cfgversion=%v gcm=%v", m["default"], m["state"], m["cfgversion"], m["x_aes_gcm"])
	}
	if x.Cipher() != inform.GCM {
		t.Error("cipher not GCM after use_aes_gcm=true")
	}
}

// A later mgmt_cfg must not replace an adopted key (stuck-adopt bug).
func TestAuthkeyOnlyFromDefault(t *testing.T) {
	x := newSession(t)
	handle(t, x, `{"_type":"setparam","mgmt_cfg":"authkey=`+newKey+`\n"}`)
	handle(t, x, `{"_type":"setparam","mgmt_cfg":"authkey=ffffffffffffffffffffffffffffffff\n"}`)
	if x.S.Key != newKey {
		t.Fatalf("key replaced: %s", x.S.Key)
	}
}

func TestFullProvisioningSavesButDoesNotLogSystemCfg(t *testing.T) {
	x := newSession(t)
	handle(t, x, `{"_type":"setparam","mgmt_cfg":"authkey=`+newKey+`\n"}`)
	o := handle(t, x, `{"_type":"setparam","cfgversion":"abcdabcdabcdabcd","system_cfg":"aaa.1.ssid=HomeWiFi\naaa.1.wpa_psk=SECRETSECRET\nradio.1.phyname=wifi0\n","blocked_sta":"","mgmt_cfg":"cfgversion=abcdabcdabcdabcd\n"}`)
	if x.S.CfgVersion != "abcdabcdabcdabcd" {
		t.Errorf("cfgversion %s", x.S.CfgVersion)
	}
	for _, e := range o.Events {
		if strings.Contains(e, "SECRET") || strings.Contains(e, "HomeWiFi") {
			t.Errorf("event leaks system_cfg values: %s", e)
		}
	}
}

func TestUpgradeIsNeverApplied(t *testing.T) {
	x := newSession(t)
	before := x.Boot
	time.Sleep(time.Millisecond)
	o := handle(t, x, `{"_type":"upgrade","version":"6.8.9.99999","url":"http://192.168.1.1:8080/dl/firmware/x.bin","sha256sum":"00"}`)
	if x.S.Version != "6.8.9.99999" || !x.Boot.After(before) || !o.Immediate {
		t.Fatalf("upgrade handling: version=%s boot moved=%v", x.S.Version, x.Boot.After(before))
	}
}

func TestSetdefaultAndCmds(t *testing.T) {
	x := newSession(t)
	handle(t, x, `{"_type":"setparam","mgmt_cfg":"authkey=`+newKey+`\nuse_aes_gcm=true\n"}`)
	handle(t, x, `{"_type":"cmd","cmd":"setdefault"}`)
	if x.S.Adopted || x.S.Key != inform.DefaultKeyHex || x.S.GCM || x.S.CfgVersion != "0" {
		t.Fatalf("after setdefault: %+v", x.S)
	}
	handle(t, x, `{"_type":"cmd","cmd":"set-adopt","key":"`+newKey+`","uri":"http://192.168.1.1:8080/inform"}`)
	if !x.S.Adopted || x.S.Key != newKey {
		t.Fatalf("after set-adopt: %+v", x.S)
	}
	handle(t, x, `{"_type":"setdefault"}`)
	if x.S.Adopted {
		t.Fatal("top-level setdefault ignored")
	}
}

func TestStateRoundTrip(t *testing.T) {
	x := newSession(t)
	p := x.StateDir + "/state.json"
	x.S.Key = newKey
	x.S.InformAddr = map[string]string{"unifi.home.example": "192.168.0.14"}
	if err := x.S.Save(p); err != nil {
		t.Fatal(err)
	}
	s, ok, err := Load(p)
	if err != nil || !ok || s.Key != newKey || s.InformAddr["unifi.home.example"] != "192.168.0.14" {
		t.Fatalf("load: %+v %v %v", s, ok, err)
	}
	// a state file from before inform_addr
	os.WriteFile(p, []byte(`{"key":"`+newKey+`","adopted":true,"inform_url":"http://192.168.1.1:8080/inform"}`), 0o600)
	s, ok, err = Load(p)
	if err != nil || !ok || !s.Adopted || s.InformAddr != nil {
		t.Fatalf("old state: %+v %v %v", s, ok, err)
	}
}

func TestApplyAdoptAndSSHPort(t *testing.T) {
	x := newSession(t)
	x.SSHPort = 2222
	x.ApplyAdopt(AdoptRequest{URL: "http://192.168.1.1:8080/inform", Key: newKey})
	if !x.S.Adopted || x.S.Key != newKey {
		t.Fatalf("%+v", x.S)
	}
	js, _ := x.Payload(time.Now())
	var m map[string]any
	json.Unmarshal(js, &m)
	if m["sshd_port"] != 2222.0 {
		t.Errorf("sshd_port %v", m["sshd_port"])
	}
}

func TestSetAdoptParse(t *testing.T) {
	s := &SSHAdopt{Requests: make(chan AdoptRequest, 1)}
	s.command("/usr/bin/syswrapper.sh set-adopt http://192.168.1.1:8080/inform "+newKey, "192.168.1.1")
	r := <-s.Requests
	if r.URL != "http://192.168.1.1:8080/inform" || r.Key != newKey || r.Peer != "192.168.1.1" {
		t.Fatalf("%+v", r)
	}
}

// set-adopt to a host name ("Override inform host"): the SSH peer is the
// controller, kept as that name's address for an AP that cannot resolve
// it (OpenWrt's dnsmasq drops answers with private addresses).
func TestApplyAdoptLearnsInformAddr(t *testing.T) {
	x := newSession(t)
	x.ApplyAdopt(AdoptRequest{URL: "http://unifi.home.example:8080/inform", Key: newKey, Peer: "192.168.0.14"})
	if a := x.S.InformAddr["unifi.home.example"]; a != "192.168.0.14" {
		t.Errorf("learned %q, want the SSH peer", a)
	}
	// SSH forwarded through the AP: the peer is the AP itself, so the
	// address the previous inform URL reached is taken instead.
	x = newSession(t)
	x.ApplyAdopt(AdoptRequest{URL: "http://unifi.test:8080/inform", Key: newKey, Peer: x.Dev.IP})
	if a := x.S.InformAddr["unifi.test"]; a != "192.168.1.1" {
		t.Errorf("learned %q, want the previous inform address", a)
	}
	x = newSession(t)
	x.ApplyAdopt(AdoptRequest{URL: "http://192.168.0.14:8080/inform", Key: newKey, Peer: "192.168.0.14"})
	if x.S.InformAddr != nil {
		t.Errorf("learned %v for an IP literal", x.S.InformAddr)
	}
}

// mgmt_cfg moves the inform URL to a name: the controller has just
// answered at the previous address, so that is the name's address.
func TestInformURLNameFromMgmtCfgLearnsAddr(t *testing.T) {
	x := newSession(t)
	handle(t, x, `{"_type":"setparam","mgmt_cfg":"cfgversion=1\ninform_url=http://unifi.test:8080/inform\n"}`)
	if x.S.InformURL != "http://unifi.test:8080/inform" || x.S.InformAddr["unifi.test"] != "192.168.1.1" {
		t.Fatalf("inform_url %q, learned %v", x.S.InformURL, x.S.InformAddr)
	}
}

// "Override inform host": the controller repeats the setparam until the
// device reports the new address.
func TestInformURLFromMgmtCfg(t *testing.T) {
	x := newSession(t)
	handle(t, x, `{"_type":"setparam","mgmt_cfg":"authkey=`+newKey+`\ncfgversion=1\n"}`)
	o := handle(t, x, `{"_type":"setparam","mgmt_cfg":"cfgversion=1\ninform_url=http://192.168.1.9:8080/inform\n"}`)
	if x.S.InformURL != "http://192.168.1.9:8080/inform" || !o.Changed || !o.Immediate {
		t.Fatalf("inform_url %q changed=%v immediate=%v", x.S.InformURL, o.Changed, o.Immediate)
	}
	js, err := x.Payload(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(js, &m)
	if m["inform_url"] != x.S.InformURL || m["inform_ip"] != "192.168.1.9" {
		t.Errorf("reported inform_url %v inform_ip %v", m["inform_url"], m["inform_ip"])
	}
	for _, bad := range []string{"ftp://192.168.1.9/x", "192.168.1.9:8080", "http://"} {
		handle(t, x, `{"_type":"setparam","mgmt_cfg":"cfgversion=1\ninform_url=`+bad+`\n"}`)
		if x.S.InformURL != "http://192.168.1.9:8080/inform" {
			t.Errorf("malformed inform_url %q taken", bad)
		}
	}
}

// A setparam the controller keeps repeating unchanged must not keep the
// device informing twice a second for ever.
func TestRepeatedSetparamStopsHurrying(t *testing.T) {
	x := newSession(t)
	same := `{"_type":"setparam","mgmt_cfg":"cfgversion=7\nsomething_new=1\n"}`
	var got []bool
	for i := 0; i < 6; i++ {
		got = append(got, handle(t, x, same).Immediate)
	}
	if want := []bool{true, true, true, false, false, false}; !slices.Equal(got, want) {
		t.Errorf("immediate %v, want %v", got, want)
	}
	if !handle(t, x, `{"_type":"setparam","mgmt_cfg":"cfgversion=8\n"}`).Immediate {
		t.Error("a new setparam must be answered right away")
	}
}

// The console that adopts is the one we inform to, so its address is
// allowed without configuration; everyone else is still refused.
func TestSSHAdoptAllowed(t *testing.T) {
	s := &SSHAdopt{
		AllowFrom: []string{"192.168.1.1"},
		AllowFunc: func(h string) bool { return h == "192.168.0.14" },
	}
	for host, want := range map[string]bool{
		"192.168.1.1":  true,
		"192.168.0.14": true,
		"192.168.0.99": false,
	} {
		if got := s.allowed(host); got != want {
			t.Errorf("allowed(%s) = %v, want %v", host, got, want)
		}
	}
	if (&SSHAdopt{}).allowed("192.168.0.14") {
		t.Error("empty allow list accepted a host")
	}
}

func TestServerTime(t *testing.T) {
	for v, want := range map[string]int64{`"1700000000000"`: 1700000000, `1700000000000`: 1700000000, `""`: 0, `"x"`: 0} {
		x := &Session{}
		o, err := x.Handle([]byte(`{"_type":"noop","interval":10,"server_time_in_utc":`+v+`}`), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if got := o.ServerTime; want == 0 && !got.IsZero() || want != 0 && got.Unix() != want {
			t.Errorf("server_time_in_utc %s: %v", v, got)
		}
	}
	if o, _ := (&Session{}).Handle([]byte(`{"_type":"noop"}`), time.Now()); !o.ServerTime.IsZero() {
		t.Errorf("no server time sent: %v", o.ServerTime)
	}
}

func TestRefusedCommands(t *testing.T) {
	const token = "VGhpcyBpcyBhIG9uZS10aW1lIHRva2VuIGZvciBhbiB1cGxvYWQ="
	for cmd, want := range map[string]string{
		"get-support-file":  "not supported",
		"build-ssh-session": "not supported",
		"close-ssh-session": "not supported",
		"run":               "refused",
	} {
		x := &Session{}
		o, err := x.Handle([]byte(`{"_type":"cmd","cmd":"`+cmd+`","token":"`+token+`","password":"p4ss","shellcmd":"reboot","upload_url":"https://192.0.2.1:28082/x"}`), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		all := strings.Join(o.Events, "\n")
		if !strings.Contains(all, want) || len(o.Actions) != 0 {
			t.Errorf("%s: events %q, actions %v", cmd, o.Events, o.Actions)
		}
		if strings.Contains(all, token) || strings.Contains(all, "p4ss") {
			t.Errorf("%s: a secret is in the log: %q", cmd, all)
		}
	}
}

func TestRawReplyHidesSecrets(t *testing.T) {
	const token = "VGhpcyBpcyBhIG9uZS10aW1lIHRva2VuIGZvciBhbiB1cGxvYWQ="
	x := &Session{}
	o, err := x.Handle([]byte(`{"_type":"cmd","cmd":"something-new","token":"`+token+`","pinned_pubkey":"sha256//abc","note":"kept"}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(o.Events, "\n")
	if !strings.Contains(all, "raw reply") || !strings.Contains(all, "kept") {
		t.Fatalf("no raw reply for an unknown cmd: %q", all)
	}
	if strings.Contains(all, token) || strings.Contains(all, "sha256//abc") {
		t.Errorf("a secret is in the raw reply: %q", all)
	}
}
