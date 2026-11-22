// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

func TestRFC3164(t *testing.T) {
	ts := time.Date(2026, 11, 5, 9, 4, 3, 0, time.Local)
	got := rfc3164(6, ts, "ap-airport", "", "uow[12]", "hello\tworld\r\nsecond")
	if got != "<30>Nov  5 09:04:03 ap-airport uow[12]: hello world  second" {
		t.Errorf("%q", got)
	}
	if got := rfc3164(3, ts, "A B", "U7PG2,020000000001,6.8.2: ", "hostapd", "wlan10: failed"); got != "<27>Nov  5 09:04:03 A-B U7PG2,020000000001,6.8.2: hostapd: wlan10: failed" {
		t.Errorf("%q", got)
	}
	if got := rfc3164(6, ts, "", "", "t", "m"); !strings.Contains(got, " - t: m") {
		t.Errorf("empty host: %q", got)
	}
	long := rfc3164(6, ts, "h", "", "t", strings.Repeat("é", 2000))
	if len(long) > syslogMaxLine || len(long) < syslogMaxLine-1 {
		t.Errorf("long line is %d bytes", len(long))
	}
	if strings.ContainsRune(long, '�') {
		t.Error("cut in the middle of a character")
	}
}

func TestSyslogSeverity(t *testing.T) {
	for msg, want := range map[string]int{"APPLY FAILED: x": 3, "wlan10: authentication error": 3, "scan 2g: 7 networks": 6, "": 6} {
		if got := syslogSeverity(msg); got != want {
			t.Errorf("%q: %d, want %d", msg, got, want)
		}
	}
}

func TestLogTap(t *testing.T) {
	var buf bytes.Buffer
	tap := &logTap{w: &buf}
	tap.Write([]byte("2026/11/21 10:00:00 first\n"))
	if q, _ := tap.take(); len(q) != 0 {
		t.Fatalf("lines kept while off: %+v", q)
	}
	tap.set(true)
	tap.Write([]byte("2026/11/21 10:00:01 second\n2026/11/21 10:00:02 third line\n"))
	tap.Write([]byte("no timestamp here\n\n"))
	q, dropped := tap.take()
	if len(q) != 3 || q[0].msg != "second" || q[1].msg != "third line" || q[2].msg != "no timestamp here" || dropped != 0 {
		t.Fatalf("%+v dropped %d", q, dropped)
	}
	if !strings.Contains(buf.String(), "first") || !strings.Contains(buf.String(), "second") {
		t.Errorf("the log itself lost lines: %q", buf.String())
	}
	// bounded: the oldest go
	for i := 0; i < syslogTapMax+10; i++ {
		tap.Write([]byte("2026/11/21 10:00:00 x\n"))
	}
	q, dropped = tap.take()
	if len(q) != syslogTapMax || dropped != 10 {
		t.Errorf("%d kept, %d dropped", len(q), dropped)
	}
	tap.set(false)
	tap.Write([]byte("2026/11/21 10:00:00 y\n"))
	if q, _ := tap.take(); len(q) != 0 {
		t.Error("kept after off")
	}
}

func TestSteeredSSIDsAndSetup(t *testing.T) {
	cfgText := `bandsteering.status=enabled
bandsteering.mode=prefer_5g
bandsteering.1.status=enabled
bandsteering.1.vap.1.devname=ath0
bandsteering.1.vap.2.devname=ath1
wireless.1.id=aa
wireless.1.ssid=Example Net
wireless.1.parent=wifi0
wireless.1.devname=ath0
wireless.2.id=aa
wireless.2.ssid=Example Net
wireless.2.parent=wifi1
wireless.2.devname=ath1
`
	cfg := translate.ParseCfg(cfgText)
	notes := airportSteerSetup(cfg, cfgText)
	if len(airportSteerSSIDs) != 1 || !airportSteerSSIDs["Example Net"] || len(notes) != 1 || !strings.Contains(notes[0], "prefer_5g") {
		t.Fatalf("%v %q", airportSteerSSIDs, notes)
	}
	// "equal" is load balancing: no steering, said so
	eq := strings.Replace(cfgText, "mode=prefer_5g", "mode=equal", 1)
	notes = airportSteerSetup(translate.ParseCfg(eq), eq)
	if airportSteerSSIDs != nil || len(notes) != 1 || !strings.Contains(notes[0], "not done") {
		t.Errorf("equal: %v %q", airportSteerSSIDs, notes)
	}
	// off: nothing, and nothing said
	off := strings.Replace(cfgText, "status=enabled\nbandsteering.mode", "status=disabled\nbandsteering.mode", 1)
	notes = airportSteerSetup(translate.ParseCfg(off), off)
	if airportSteerSSIDs != nil || notes != nil {
		t.Errorf("off: %v %q", airportSteerSSIDs, notes)
	}
}
