// SPDX-License-Identifier: GPL-2.0-or-later

package inform

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func testDevice() Device {
	return Device{
		MAC:       testMAC,
		IP:        "192.168.1.50",
		Netmask:   "255.255.255.0",
		Gateway:   "192.168.1.1",
		Hostname:  "unifi-openwrt-test",
		InformURL: "http://192.168.1.1:8080/inform",
		Uptime:    42,
		Time:      1794500000,
	}
}

// TestU7PG2PendingInformOffline is the M0 "done when": build a U7PG2
// pending inform, encode it with the default key, decode it, check fields.
func TestU7PG2PendingInformOffline(t *testing.T) {
	js, err := PendingInform(testDevice())
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := Encode(testMAC, DefaultKeyHex, CBC, js, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, out, err := Decode(pkt, DefaultKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	if h.Flags != 0x0003 {
		t.Errorf("pending inform flags %#04x, want 0x0003 (as the real U7PG2)", h.Flags)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["mac"] != h.MAC.String() {
		t.Errorf("payload mac %v != header mac %s", m["mac"], h.MAC)
	}
	for _, k := range requiredPendingFields {
		if _, ok := m[k]; !ok {
			t.Errorf("missing field %q", k)
		}
	}
	want := map[string]any{"model": "U7PG2", "type": "uap", "version": "6.8.2.15592",
		"state": 1.0, "default": true, "fw_caps": 3.0, "inform_url": "http://192.168.1.1:8080/inform"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	// udapi_caps without udapi_version makes the controller drop all caps.
	if _, caps := m["udapi_caps"]; caps {
		if _, ver := m["udapi_version"]; !ver {
			t.Error("udapi_caps without udapi_version")
		}
	}
	radios, _ := m["radio_table"].([]any)
	if len(radios) != 2 {
		t.Fatalf("radio_table has %d radios, want 2", len(radios))
	}
	t.Logf("pending inform: %d bytes JSON, %d bytes on the wire", len(js), len(pkt))
}

// The controller rejects an inform whose inform_ip is not an address, so
// a name in the inform URL ("Override inform host") is looked up.
func TestU7PG2HostnameInformURL(t *testing.T) {
	old := lookupIP
	defer func() { lookupIP = old }()
	calls := 0
	lookupIP = func(h string) ([]net.IP, error) {
		calls++
		if h == "unifi.test" {
			return []net.IP{net.ParseIP("fd00::1"), net.ParseIP("192.0.2.7")}, nil
		}
		return nil, errors.New("no such host")
	}
	d := testDevice()
	d.InformURL = "http://unifi.test:8080/inform"
	for i := 0; i < 2; i++ {
		m, err := U7PG2Fields(d)
		if err != nil {
			t.Fatal(err)
		}
		if m["inform_ip"] != "192.0.2.7" || m["inform_url"] != d.InformURL {
			t.Errorf("inform_ip %v, inform_url %v", m["inform_ip"], m["inform_url"])
		}
	}
	if calls != 1 {
		t.Errorf("%d lookups, want 1 (cached)", calls)
	}
	for _, u := range []string{"http://nowhere.test:8080/inform", "http://[fd00::1]:8080/inform", "http://"} {
		d.InformURL = u
		if _, err := PendingInform(d); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
}

// requiredPendingFields: docs/internals.md (every inform, pending or adopted).
var requiredPendingFields = []string{
	"mac", "serial", "model", "model_display", "type", "version", "ip", "hostname",
	"inform_url", "uptime", "time", "cfgversion", "x_authkey", "default",
	"_default_key", "state", "fw_caps", "isolated", "locating", "selfrun_beacon",
	"radio_table",
}

func TestInformIPExported(t *testing.T) {
	if ip, err := InformIP("http://192.168.0.14:8080/inform"); err != nil || ip != "192.168.0.14" {
		t.Fatalf("%q %v", ip, err)
	}
}

// stubLookup answers lookups from m (host -> IPv4) and fails the rest,
// with no cached, learned or warned-about hosts before or after the test.
func stubLookup(t *testing.T, m map[string]string) {
	old := lookupIP
	reset := func() {
		ipMu.Lock()
		defer ipMu.Unlock()
		ipCache, learned, warned, failedAt = map[string]ipEntry{}, map[string]string{}, map[string]bool{}, map[string]time.Time{}
	}
	reset()
	lookupIP = func(h string) ([]net.IP, error) {
		if ip, ok := m[h]; ok {
			return []net.IP{net.ParseIP(ip)}, nil
		}
		return nil, errors.New("no such host")
	}
	t.Cleanup(func() { lookupIP = old; reset() })
}

// A name the AP cannot resolve (OpenWrt's dnsmasq drops private answers)
// falls back to the controller address learned at adoption, logged once
// with the DNS fix. A successful lookup still wins; a literal is a literal.
func TestInformIPLearnedAddr(t *testing.T) {
	stubLookup(t, map[string]string{"ok.test": "192.0.2.7"})
	SetInformAddr("unifi.home.example", "192.168.0.14")
	SetInformAddr("ok.test", "192.168.0.14")
	SetInformAddr("192.0.2.1", "192.168.0.14")
	var buf bytes.Buffer
	w := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(w)
	for i := 0; i < 2; i++ {
		if ip, err := InformIP("http://unifi.home.example:8080/inform"); err != nil || ip != "192.168.0.14" {
			t.Fatalf("learned address: %q %v", ip, err)
		}
	}
	if n := strings.Count(buf.String(), "does not resolve"); n != 1 {
		t.Errorf("fallback logged %d times, want once:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "rebind_domain='home.example'") {
		t.Errorf("log lacks the dnsmasq fix: %s", buf.String())
	}
	for u, want := range map[string]string{
		"http://ok.test:8080/inform":   "192.0.2.7",
		"http://192.0.2.1:8080/inform": "192.0.2.1",
	} {
		if ip, err := InformIP(u); err != nil || ip != want {
			t.Errorf("%s: %q %v, want %s", u, ip, err, want)
		}
	}
	if _, err := InformIP("http://nowhere.test:8080/inform"); err == nil {
		t.Error("a name with no lookup and no learned address accepted")
	}
}

// The inform POST reaches the learned address, with the name as Host.
func TestDialContextLearnedAddr(t *testing.T) {
	var host string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { host = r.Host }))
	defer srv.Close()
	stubLookup(t, nil)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	SetInformAddr("ctrl.test", "127.0.0.1")
	c := &http.Client{Transport: &http.Transport{DialContext: DialContext}}
	resp, err := c.Post("http://ctrl.test:"+port+"/inform", "application/x-binary", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if host != "ctrl.test:"+port {
		t.Errorf("Host %q, want ctrl.test:%s", host, port)
	}
}

// With an address to fall back on, a failed lookup is not repeated for a
// minute; without one it is, so a name that starts to resolve is used at once.
func TestInformIPFailedLookupRemembered(t *testing.T) {
	stubLookup(t, nil)
	n := 0
	lookupIP = func(string) ([]net.IP, error) { n++; return nil, errors.New("timeout") }
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	SetInformAddr("ctrl.test", "192.0.2.9")
	for i := 0; i < 5; i++ {
		if ip, err := InformIP("http://ctrl.test:8080/inform"); err != nil || ip != "192.0.2.9" {
			t.Fatalf("%q %v", ip, err)
		}
	}
	if n != 1 {
		t.Errorf("%d lookups in a minute, want 1", n)
	}
	ipMu.Lock()
	failedAt["ctrl.test"] = time.Now().Add(-2 * time.Minute)
	ipMu.Unlock()
	InformIP("http://ctrl.test:8080/inform")
	if n != 2 {
		t.Errorf("%d lookups after a minute, want 2", n)
	}
	n = 0
	for i := 0; i < 3; i++ {
		if _, err := InformIP("http://none.test:8080/inform"); err == nil {
			t.Fatal("a name with no address accepted")
		}
	}
	if n != 3 {
		t.Errorf("%d lookups for a name with no fallback, want 3", n)
	}
}
