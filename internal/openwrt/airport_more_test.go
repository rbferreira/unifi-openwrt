// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// Ports: every PHY of acpd.system.show is a port, in its order, with its link.
func TestAirPortAllPorts(t *testing.T) {
	phy := "=== phy\n" +
		"\t \t PHY (0x20A480E0) index 0\n\t \t \t Link State - Current  (link state is Down)\n\t \t \t Link State - Previous (link state is Down)\n" +
		"\t \t PHY (0x20A48110) index 1\n\t \t \t Link State - Current  (link state 1000Mbps Full Duplex)\n" +
		"\t \t PHY (0x20A48120) index 2\n\t \t \t Link State - Current  (link state is Down)\n" +
		"\t \t PHY (0x20A48130) index 3\n\t \t \t Link State - Current  (link state 100Mbps Half Duplex)\n"
	i := strings.Index(airportSample, "=== phy\n")
	j := strings.Index(airportSample, "=== netstat")
	s, err := ParseAirPort([]byte(airportSample[:i]+phy+airportSample[j:]), time.Unix(1000200, 0))
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		name string
		up   bool
		spd  int
		full bool
	}
	var got []row
	for _, p := range s.Ports {
		got = append(got, row{p.Name, p.Up, p.Speed, p.FullDuplex})
	}
	want := []row{{"wan", false, 0, false}, {"lan1", true, 1000, true}, {"lan2", false, 0, false}, {"lan3", true, 100, false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ports %+v, want %+v", got, want)
	}
	// counters only on the first port with link: there are none per port
	if s.Ports[1].RxBytes == 0 || s.Ports[3].RxBytes != 0 || s.Ports[0].RxBytes != 0 {
		t.Errorf("counters: %+v", s.Ports)
	}
	rows, _ := s.Fields(nil, CfgInfo{}, nil)["port_table"].([]map[string]any)
	if len(rows) != 4 {
		t.Fatalf("port_table: %d rows", len(rows))
	}
	names := []string{"WAN", "LAN 1", "LAN 2", "LAN 3"}
	for k, r := range rows {
		if r["port_idx"] != k+1 || r["name"] != names[k] || r["ifname"] != want[k].name || r["up"] != want[k].up || r["speed"] != want[k].spd {
			t.Errorf("row %d: %+v", k, r)
		}
	}
	// the uplink guess is the one the other platforms use (the gateway's port, else
	// the last port with link that is a "lan"); ports without link are never it
	if rows[0]["is_uplink"] == true || rows[2]["is_uplink"] == true {
		t.Errorf("uplink flag on a port without link: %v %v", rows[0]["is_uplink"], rows[2]["is_uplink"])
	}
}

func TestParseAirPortPHYs(t *testing.T) {
	if got := parseAirPortPHYs(""); got != nil {
		t.Errorf("empty: %+v", got)
	}
	// no "Current" line: a port without link, not a lost port
	got := parseAirPortPHYs("PHY (0xAB) index 0\nsomething else\nPHY (0xCD) index 5\nLink State - Current (link state 10Mbps Full Duplex)\n")
	if len(got) != 2 || got[0].up || got[1].name != "lan5" || !got[1].up || got[1].speed != 10 {
		t.Errorf("%+v", got)
	}
}

func TestAirPortTemperatureRows(t *testing.T) {
	text := strings.Replace(airportSample, "model=syAM=TimeCapsule8,119\n", "model=syAM=TimeCapsule8,119\n", 1) + "=== env\n" + apEnvSample
	s, err := ParseAirPort([]byte(text), time.Unix(1000200, 0))
	if err != nil {
		t.Fatal(err)
	}
	f := s.Fields(nil, CfgInfo{}, nil)
	rows, _ := f["temperatures"].([]map[string]any)
	if len(rows) != len(s.Thermal.Temps) || len(rows) == 0 {
		t.Fatalf("temperatures: %+v", f["temperatures"])
	}
	for _, r := range rows {
		if r["name"] == "" || r["type"] != "board" {
			t.Errorf("row %+v", r)
		}
		if _, ok := r["value"].(float64); !ok {
			t.Errorf("value %T", r["value"])
		}
	}
	if f["general_temperature"] == nil || f["fan_level"] == nil {
		t.Error("the scalar form must stay")
	}
}

func TestAirPortSystemNotes(t *testing.T) {
	cfg := translate.ParseCfg("system.timezone=<-03>3\nntpclient.status=enabled\nntpclient.1.server=0.ntp.example\nntpclient.2.server=1.ntp.example\n")
	n := AirPortSystemNotes(cfg)
	if len(n) != 2 || !strings.Contains(n[0], "<-03>3") || !strings.Contains(n[1], "0.ntp.example, 1.ntp.example") {
		t.Errorf("%q", n)
	}
	if n := AirPortSystemNotes(translate.ParseCfg("radio.1.phyname=wifi0\n")); n != nil {
		t.Errorf("nothing asked: %q", n)
	}
}

// ---- schedules

func schedWanted(tz string, scheds ...*translate.Schedule) translate.Wanted {
	w := translate.Wanted{Timezone: tz, Radios: apRadios()}
	for i, s := range scheds {
		l := apWLAN(string(rune('1'+i)), "wifi0")
		l.Schedule = s
		w.WLANs = append(w.WLANs, l)
	}
	return w
}

func TestAirPortSchedule(t *testing.T) {
	// off on weekdays 22:00-24:00 and 00:00-06:00 (the OpenWrt reading would differ; see WLANOn)
	night := &translate.Schedule{Windows: map[string][]string{
		"mon": {"0000-0600", "2200-2400"}, "tue": {"0000-0600", "2200-2400"}}}
	// 2026-11-17 is a Tuesday. 01:00 in UTC-3 is 04:00 UTC; 12:00 in UTC-3 is 15:00 UTC.
	asleep := time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC)
	awake := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)

	w, notes, key := AirPortSchedule(schedWanted("<-03>3", night, nil), asleep)
	if len(w.WLANs) != 1 || w.WLANs[0].N != "2" || key != "1=false" {
		t.Fatalf("asleep: %+v key %q", w.WLANs, key)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "outside its schedule") {
		t.Errorf("notes: %q", notes)
	}
	w, _, key2 := AirPortSchedule(schedWanted("<-03>3", night, nil), awake)
	if len(w.WLANs) != 2 || key2 != "1=true" || key2 == key {
		t.Fatalf("awake: %d WLANs key %q", len(w.WLANs), key2)
	}
	// the same instant in the controller's zone, not the daemon's: UTC-3 says
	// 12:00 (on); the same schedule read in UTC would say 15:00 (on too), so
	// pick a zone that flips the answer: UTC+5 turns 04:00 UTC into 09:00 (on)
	w, _, _ = AirPortSchedule(schedWanted("<+05>-5", night, nil), asleep)
	if len(w.WLANs) != 2 {
		t.Errorf("zone ignored: %d WLANs", len(w.WLANs))
	}
}

func TestAirPortScheduleNoZone(t *testing.T) {
	night := &translate.Schedule{OnDuring: true, Windows: map[string][]string{}} // would be off always
	for _, tz := range []string{"", "Nowhere"} {
		w, notes, key := AirPortSchedule(schedWanted(tz, night), time.Now())
		if len(w.WLANs) != 1 || key != "" || len(notes) != 1 || !strings.Contains(notes[0], "not enforced") {
			t.Errorf("tz %q: %d WLANs, key %q, notes %q", tz, len(w.WLANs), key, notes)
		}
	}
	// no schedule at all: nothing said, nothing changed
	w, notes, key := AirPortSchedule(schedWanted("", nil), time.Now())
	if len(w.WLANs) != 1 || notes != nil || key != "" {
		t.Errorf("unscheduled: %+v %q %q", w.WLANs, notes, key)
	}
	// a disabled WLAN is not looked at
	d := schedWanted("<-03>3", &translate.Schedule{OnDuring: true, Windows: map[string][]string{}})
	d.WLANs[0].Disabled = true
	if _, _, key := AirPortSchedule(d, time.Now()); key != "" {
		t.Errorf("disabled WLAN in the key: %q", key)
	}
}

// ---- automatic channel

func nbr(ch, signal int) BSS {
	return BSS{BSSID: "02:00:00:00:00:" + string(rune('a'+ch%20)) + "0", Channel: ch, Signal: signal, Width: 20}
}

func TestChannelCost(t *testing.T) {
	strong := []BSS{nbr(1, -40)}
	if c1, c6, c11 := ChannelCost(1, 20, strong), ChannelCost(6, 20, strong), ChannelCost(11, 20, strong); !(c1 > 5 && c6 == 0 && c11 == 0) {
		t.Errorf("a neighbour on 1: costs %v %v %v", c1, c6, c11)
	}
	// channel 3 overlaps 1 partly, 6 more than 1, and not 11
	if a, b := ChannelCost(1, 20, []BSS{nbr(3, -50)}), ChannelCost(6, 20, []BSS{nbr(3, -50)}); !(a > 0 && b > 0 && ChannelCost(11, 20, []BSS{nbr(3, -50)}) == 0) {
		t.Errorf("a neighbour on 3: %v %v", a, b)
	}
	// stronger is worse, more is worse, 40 MHz reaches further
	if !(ChannelCost(6, 20, []BSS{nbr(6, -40)}) > ChannelCost(6, 20, []BSS{nbr(6, -80)})) {
		t.Error("strength")
	}
	if !(ChannelCost(6, 20, []BSS{nbr(6, -80), nbr(6, -80)}) > ChannelCost(6, 20, []BSS{nbr(6, -80)})) {
		t.Error("count")
	}
	wide := BSS{Channel: 6, Signal: -50, Width: 40, CenterFreq: 2447} // 6 + 2 = 8: spans 2427..2467
	if !(ChannelCost(11, 20, []BSS{wide}) > 0 && ChannelCost(1, 20, []BSS{wide}) == 0) {
		t.Errorf("40 MHz neighbour: %v %v", ChannelCost(11, 20, []BSS{wide}), ChannelCost(1, 20, []BSS{wide}))
	}
	// a 5 GHz neighbour does not count on 2.4 GHz and the other way round
	if ChannelCost(6, 20, []BSS{{Channel: 36, Signal: -40, Width: 20}}) != 0 || ChannelCost(36, 20, []BSS{nbr(6, -40)}) != 0 {
		t.Error("bands mixed")
	}
	// a signal of 0 (unknown) is clamped to -30, not dropped
	if ChannelCost(6, 20, []BSS{nbr(6, 0)}) <= ChannelCost(6, 20, []BSS{nbr(6, -60)}) {
		t.Error("clamp")
	}
}

func TestChannelCost5GHz(t *testing.T) {
	n80 := BSS{Channel: 36, Signal: -50, Width: 80, CenterFreq: 5210} // 36..48
	for ch, hit := range map[int]bool{36: true, 40: true, 44: true, 48: true, 149: false, 157: false} {
		if got := ChannelCost(ch, 20, []BSS{n80}) > 0; got != hit {
			t.Errorf("80 MHz neighbour on 36, our 20 MHz on %d: overlap %v, want %v", ch, got, hit)
		}
	}
	n20 := BSS{Channel: 44, Signal: -50, Width: 20}
	if ChannelCost(40, 20, []BSS{n20}) != 0 || ChannelCost(44, 20, []BSS{n20}) == 0 {
		t.Error("20 MHz neighbour: adjacent channel must not count, same channel must")
	}
	// our 80 MHz block on 149..161 meets a neighbour on 157; on 36..48 it does not
	n157 := BSS{Channel: 157, Signal: -50, Width: 20}
	if ChannelCost(149, 80, []BSS{n157}) == 0 || ChannelCost(36, 80, []BSS{n157}) != 0 {
		t.Error("80 MHz block of ours")
	}
	// our 40 MHz block 44+48 meets 48 but not 40
	if ChannelCost(44, 40, []BSS{{Channel: 48, Signal: -50, Width: 20}}) == 0 || ChannelCost(44, 40, []BSS{{Channel: 40, Signal: -50, Width: 20}}) != 0 {
		t.Error("40 MHz block of ours")
	}
}

func TestChooseChannel(t *testing.T) {
	busy1 := []BSS{nbr(1, -45), nbr(1, -55), nbr(2, -60)}
	for _, c := range []struct {
		name  string
		nb    []BSS
		cur   int
		want  int
		cands []int
	}{
		{"nothing around: stay", nil, 6, 6, AirPortChans24},
		{"nothing around, no current: first", nil, 0, 1, AirPortChans24},
		{"nothing around, current not a candidate: first", nil, 3, 1, AirPortChans24},
		{"busy current moves to the clean one", busy1, 1, 11, AirPortChans24},
		{"busy 6 and 1: 11 wins", append(append([]BSS{}, busy1...), nbr(6, -50), nbr(6, -50)), 6, 11, AirPortChans24},
		{"a little better is not enough", []BSS{nbr(1, -85)}, 1, 1, AirPortChans24},
		{"not clearly better: stays", []BSS{nbr(1, -70), nbr(6, -70), nbr(11, -72)}, 6, 6, AirPortChans24},
		{"current not a candidate takes the best", []BSS{nbr(1, -40), nbr(11, -40)}, 3, 6, AirPortChans24},
		{"5 GHz: avoid the busy block", []BSS{{Channel: 36, Signal: -50, Width: 80, CenterFreq: 5210}}, 36, 149, AirPortChans5},
	} {
		got, costs := ChooseChannel(c.cands, 20, c.nb, c.cur)
		if got != c.want {
			t.Errorf("%s: chose %d, want %d (costs %v)", c.name, got, c.want, costs)
		}
	}
}

func chanSource(bss map[string][]BSS, at time.Time) ScanSource {
	return func(band string) ([]BSS, time.Time) { return bss[band], at }
}

func chanSnap(clients bool, ch24 int) *Snapshot {
	vap := Radio{Iface: "wlan10", Mode: "Master", BSSID: "02:aa:00:00:00:10", Channel: ch24, Band: "ng", Phy: "bwl1"}
	if clients {
		vap.Stations = []Station{{MAC: "02:00:00:00:00:99"}}
	}
	return &Snapshot{Radios: []Radio{{Phy: "bwl1", Band: "ng", Channel: ch24, VAPs: []Radio{vap}}, {Phy: "bwl0", Band: "na", Channel: 36}}}
}

func chanWanted(ch2, ch5, width5 int) translate.Wanted {
	w := translate.Wanted{Radios: []translate.WantedRadio{{Name: "wifi0", Band: "2g", Channel: ch2}, {Name: "wifi1", Band: "5g", Channel: ch5, Width: width5}}}
	w.WLANs = []translate.WantedWLAN{apWLAN("1", "wifi0"), apWLAN("2", "wifi1")}
	return w
}

func TestAutoChannelsApply(t *testing.T) {
	var a AutoChannels
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	scan := map[string][]BSS{"ng": {nbr(1, -45), nbr(1, -50), nbr(2, -55)}, "na": {{BSSID: "02:00:00:00:00:77", Channel: 36, Signal: -50, Width: 80, CenterFreq: 5210}}}
	src := chanSource(scan, now)

	// own networks do not count against us
	own := chanSnap(false, 1)
	scan["ng"] = append(scan["ng"], BSS{BSSID: "02:aa:00:00:00:10", Channel: 6, Signal: -20, Width: 20})

	w, notes := a.Apply(chanWanted(0, 0, 0), own, src, 0, now)
	if w.Radios[0].Channel != 11 || w.Radios[1].Channel != 149 {
		t.Fatalf("channels %d %d (notes %q)", w.Radios[0].Channel, w.Radios[1].Channel, notes)
	}
	// remembered: a push later, with a scan that would now say otherwise, does not move it
	scan["ng"] = []BSS{nbr(6, -30), nbr(6, -35), nbr(6, -40)}
	w, notes = a.Apply(chanWanted(0, 0, 0), own, src, 0, now.Add(time.Minute))
	if w.Radios[0].Channel != 11 || !strings.Contains(strings.Join(notes, " "), "staying on 11") {
		t.Fatalf("not stable: %d %q", w.Radios[0].Channel, notes)
	}
	// the controller fixes a channel: it wins and the memory is dropped
	w, _ = a.Apply(chanWanted(11, 0, 0), own, src, 0, now.Add(2*time.Minute))
	if w.Radios[0].Channel != 11 || len(a.m) != 1 {
		t.Fatalf("fixed channel: %d, memory %v", w.Radios[0].Channel, a.m)
	}
	// 5 GHz with the mesh uplink on it: not ours
	var b AutoChannels
	w, _ = b.Apply(chanWanted(0, 0, 0), own, src, 44, now)
	if w.Radios[1].Channel != 0 || len(b.m) != 1 {
		t.Errorf("5 GHz with uplink: %d, memory %v", w.Radios[1].Channel, b.m)
	}
	// no scan results: the radio keeps its channel (0 = automatic) and nothing is remembered
	var c AutoChannels
	w, notes = c.Apply(chanWanted(0, 0, 0), own, chanSource(nil, time.Time{}), 0, now)
	if w.Radios[0].Channel != 0 || len(c.m) != 0 || !strings.Contains(strings.Join(notes, " "), "no scan results") {
		t.Errorf("no scan: %d %v %q", w.Radios[0].Channel, c.m, notes)
	}
	// a scan older than two hours is not trusted either
	w, _ = c.Apply(chanWanted(0, 0, 0), own, chanSource(scan, now.Add(-3*time.Hour)), 0, now)
	if w.Radios[0].Channel != 0 {
		t.Errorf("stale scan used: %d", w.Radios[0].Channel)
	}
	// a radio with no WLAN on it is not chosen for
	var d AutoChannels
	few := chanWanted(0, 0, 0)
	few.WLANs = few.WLANs[1:]
	w, _ = d.Apply(few, own, src, 0, now)
	if w.Radios[0].Channel != 0 {
		t.Errorf("radio without a WLAN: %d", w.Radios[0].Channel)
	}
	// the input is not changed
	in := chanWanted(0, 0, 0)
	var e AutoChannels
	e.Apply(in, own, chanSource(map[string][]BSS{"ng": {nbr(6, -40)}}, now), 0, now)
	if in.Radios[0].Channel != 0 {
		t.Error("Apply changed its argument")
	}
}

func TestAutoChannelsReconsider(t *testing.T) {
	var a AutoChannels
	t0 := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	good := map[string][]BSS{"ng": {nbr(1, -45)}}
	src := func(at time.Time) ScanSource { return chanSource(good, at) }
	w := chanWanted(0, 0, 0)
	idle := chanSnap(false, 1)

	// no memory, scan arrives later, radio idle: first choice
	if msg := a.Reconsider(w, idle, src(t0), 0, t0); !strings.Contains(msg, "chose 6") {
		t.Fatalf("first choice: %q", msg)
	}
	// the memory says 6 now, and nothing moves within the hold time
	good["ng"] = []BSS{nbr(6, -40), nbr(6, -45), nbr(6, -50)}
	if msg := a.Reconsider(w, idle, src(t0.Add(3*time.Hour)), 0, t0.Add(3*time.Hour)); msg != "" {
		t.Errorf("moved inside the hold time: %q", msg)
	}
	// after the hold, with clients: not now
	late := t0.Add(5 * time.Hour)
	if msg := a.Reconsider(w, chanSnap(true, 6), src(late), 0, late); msg != "" {
		t.Errorf("moved with a client on the radio: %q", msg)
	}
	// idle: it moves, and only once
	msg := a.Reconsider(w, idle, src(late), 0, late)
	if !strings.Contains(msg, "moving from 6 to") || a.m["2g"].ch == 6 {
		t.Fatalf("no move: %q %+v", msg, a.m)
	}
	if msg := a.Reconsider(w, idle, src(late), 0, late.Add(time.Minute)); msg != "" {
		t.Errorf("moved twice: %q", msg)
	}
	// weighed again and kept: the next weighing is a hold away, not a minute
	good["ng"] = nil
	later := late.Add(5 * time.Hour)
	good["ng"] = []BSS{nbr(a.m["2g"].ch, -90)}
	if msg := a.Reconsider(w, idle, src(later), 0, later); msg != "" || !a.m["2g"].checked.Equal(later) {
		t.Errorf("kept: %q %+v", msg, a.m["2g"])
	}
	// a stale scan is not used
	var b AutoChannels
	if msg := b.Reconsider(w, idle, src(t0.Add(-5*time.Hour)), 0, t0); msg != "" || len(b.m) != 0 {
		t.Errorf("stale: %q %v", msg, b.m)
	}
}

// ---- steering

func steerClient(mac, vap, ssid, band string, sig int, conn int64) SteerClient {
	return SteerClient{MAC: mac, VAP: vap, SSID: ssid, Band: band, Signal: sig, Connected: conn}
}

func TestSteerNext(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	steered := map[string]bool{"Example Net": true}
	has5 := map[string]bool{"Example Net": true}
	known := func() *SteerMemory { // seen on 5 GHz a while ago
		m := &SteerMemory{}
		m.Next(now.Add(-time.Hour), nil, nil, []SteerClient{steerClient("02:00:00:00:00:01", "wlan20", "Example Net", "na", -50, 600)})
		return m
	}
	c24 := steerClient("02:00:00:00:00:01", "wlan10", "Example Net", "ng", -55, 5)

	for _, c := range []struct {
		name string
		mem  *SteerMemory
		st   map[string]bool
		h5   map[string]bool
		cl   SteerClient
		want bool
	}{
		{"known dual-band client, fresh, good signal", known(), steered, has5, c24, true},
		{"never seen on 5 GHz", &SteerMemory{}, steered, has5, c24, false},
		{"steering off", known(), nil, has5, c24, false},
		{"other SSID", known(), map[string]bool{"Other": true}, has5, c24, false},
		{"SSID not on 5 GHz", known(), steered, nil, c24, false},
		{"weak 2.4 signal", known(), steered, has5, steerClient(c24.MAC, "wlan10", "Example Net", "ng", -72, 5), false},
		{"unknown signal", known(), steered, has5, steerClient(c24.MAC, "wlan10", "Example Net", "ng", 0, 5), false},
		{"old session is not cut", known(), steered, has5, steerClient(c24.MAC, "wlan10", "Example Net", "ng", -55, 600), false},
		{"already on 5 GHz", known(), steered, has5, steerClient(c24.MAC, "wlan20", "Example Net", "na", -55, 5), false},
	} {
		got := c.mem.Next(now, c.st, c.h5, []SteerClient{c.cl})
		if (len(got) == 1) != c.want {
			t.Errorf("%s: %+v", c.name, got)
		}
		if len(got) == 1 && (got[0].MAC != c24.MAC || got[0].VAP != "wlan10" || got[0].Deny != steerDeny) {
			t.Errorf("%s: act %+v", c.name, got[0])
		}
	}

	// the 5 GHz sighting must be recent
	m := known()
	if got := m.Next(now.Add(30*time.Hour), steered, has5, []SteerClient{c24}); len(got) != 0 {
		t.Errorf("a 30 hour old sighting counted: %+v", got)
	}
}

func TestSteerLimits(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	steered, has5 := map[string]bool{"Example Net": true}, map[string]bool{"Example Net": true}
	m := &SteerMemory{}
	m.Next(now.Add(-time.Hour), nil, nil, []SteerClient{steerClient("02:00:00:00:00:01", "wlan20", "Example Net", "na", -50, 600)})
	c := steerClient("02:00:00:00:00:01", "wlan10", "Example Net", "ng", -55, 5)
	try := func(at time.Time) bool { return len(m.Next(at, steered, has5, []SteerClient{c})) == 1 }

	if !try(now) {
		t.Fatal("first try")
	}
	if try(now.Add(5 * time.Second)) {
		t.Error("tried again while the deny window was on")
	}
	if try(now.Add(30 * time.Second)) {
		t.Error("tried again inside the gap")
	}
	if !try(now.Add(3 * time.Minute)) {
		t.Error("second try after the gap")
	}
	if !try(now.Add(6 * time.Minute)) {
		t.Error("third try")
	}
	if try(now.Add(10 * time.Minute)) {
		t.Error("a fourth try: the cap is three per window")
	}
	// the window passes
	if !try(now.Add(7 * time.Hour)) {
		t.Error("no try after the window")
	}
	// ... and a sighting on 5 GHz clears the count
	m2 := &SteerMemory{}
	m2.Next(now.Add(-time.Hour), nil, nil, []SteerClient{steerClient(c.MAC, "wlan20", "Example Net", "na", -50, 600)})
	for i := 0; i < 3; i++ {
		m2.Next(now.Add(time.Duration(i)*3*time.Minute), steered, has5, []SteerClient{c})
	}
	m2.Next(now.Add(10*time.Minute), steered, has5, []SteerClient{steerClient(c.MAC, "wlan20", "Example Net", "na", -50, 20)})
	if got := m2.Next(now.Add(13*time.Minute), steered, has5, []SteerClient{c}); len(got) != 1 {
		t.Errorf("the count was not cleared by a sighting on 5 GHz: %+v", got)
	}
}

func TestSteerOnePerPassFreshestFirst(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	steered, has5 := map[string]bool{"Example Net": true}, map[string]bool{"Example Net": true}
	m := &SteerMemory{}
	var seen []SteerClient
	for _, mac := range []string{"02:00:00:00:00:01", "02:00:00:00:00:02"} {
		seen = append(seen, steerClient(mac, "wlan20", "Example Net", "na", -50, 600))
	}
	m.Next(now.Add(-time.Hour), nil, nil, seen)
	got := m.Next(now, steered, has5, []SteerClient{
		steerClient("02:00:00:00:00:01", "wlan10", "Example Net", "ng", -55, 20),
		steerClient("02:00:00:00:00:02", "wlan11", "Example Net", "ng", -55, 3),
	})
	if len(got) != 1 || got[0].MAC != "02:00:00:00:00:02" {
		t.Errorf("%+v", got)
	}
}

func TestSteerScript(t *testing.T) {
	s, err := airPortSteerScript("wlan10", "02:AA:00:00:00:01", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"v=wlan10; x=02:aa:00:00:00:01; n=10", "mac:add $x", "mac:deny", "mac:kick $x", "sleep $n", "mac:del $x", "mac:open", "</dev/null &"} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	for _, bad := range []string{"wlan9", "chanim", "mac:allow"} {
		if strings.Contains(s, bad) {
			t.Errorf("script has %q", bad)
		}
	}
	for _, c := range []struct {
		vap, mac string
		secs     int
	}{{"wlan9", "02:aa:00:00:00:01", 10}, {"wlan20", "02:aa:00:00:00:01", 10}, {"wlan10; reboot", "02:aa:00:00:00:01", 10},
		{"wlan10", "02:aa:00:00:00:0", 10}, {"wlan10", "02:aa:00:00:00:01;x", 10}, {"wlan10", "02:aa:00:00:00:01", 0}, {"wlan10", "02:aa:00:00:00:01", 3600}} {
		if _, err := airPortSteerScript(c.vap, c.mac, c.secs); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	checkSh(t, "steer", s)
}

func TestSnapshotSteerClients(t *testing.T) {
	s := &Snapshot{Radios: []Radio{
		{Phy: "bwl1", VAPs: []Radio{{Iface: "wlan10", Mode: "Master", Band: "ng", SSID: "Example Net", Stations: []Station{{MAC: "02:00:00:00:00:01", Signal: -50, Connected: 4}}}}},
		{Phy: "bwl0", VAPs: []Radio{
			{Iface: "wlan20", Mode: "Master", Band: "na", SSID: "Example Net", Stations: []Station{{MAC: "02:00:00:00:00:02", Signal: -60, Connected: 400}}},
			{Iface: "wlan9", Mode: "Client", Band: "na", SSID: "Mesh", Stations: []Station{{MAC: "02:00:00:00:00:03"}}},
		}},
	}}
	cl, ssid5 := s.SteerClients()
	if len(cl) != 2 || !ssid5["Example Net"] || ssid5["Mesh"] || cl[0].VAP != "wlan10" || cl[1].Band != "na" {
		t.Errorf("%+v %v", cl, ssid5)
	}
}

// ---- hostapd logs

func TestLogTail(t *testing.T) {
	var tail LogTail
	s := tail.Script()
	if strings.Contains(s, "o_wlan") {
		t.Errorf("offsets before any reading: %s", s)
	}
	// first reading: only sizes, nothing replayed
	out := "\n=== wlan10 500 -\n\n=== wlan20 100 -\n"
	if got := tail.Take([]byte(out)); got != nil {
		t.Errorf("history replayed: %+v", got)
	}
	if tail.off["wlan10"] != 500 || tail.off["wlan20"] != 100 {
		t.Fatalf("offsets %v", tail.off)
	}
	if s := tail.Script(); !strings.Contains(s, "o_wlan10=500\n") || !strings.Contains(s, "o_wlan20=100\n") {
		t.Errorf("script: %s", s)
	}
	// new lines, the last one cut in the middle: it waits
	out = "\n=== wlan10 540 500\nwlan10: STA 02:00:00:00:00:01 associated\nwlan10: STA 02:00:00:00:00:01 disass\n=== wlan20 100 100\n"
	got := tail.Take([]byte(out))
	if len(got) != 1 || got[0].VAP != "wlan10" || got[0].Text != "wlan10: STA 02:00:00:00:00:01 associated" {
		t.Fatalf("lines %+v", got)
	}
	if tail.off["wlan10"] != 500+int64(len("wlan10: STA 02:00:00:00:00:01 associated\n")) || tail.off["wlan20"] != 100 {
		t.Errorf("offsets %v", tail.off)
	}
	// the log was made again (shorter than the offset): read from the start
	out = "\n=== wlan10 30 0\nstarted\n"
	got = tail.Take([]byte(out))
	if len(got) != 1 || got[0].Text != "started" || tail.off["wlan10"] != 8 {
		t.Errorf("restart: %+v %v", got, tail.off)
	}
	// a log that vanished is forgotten, and starts at its end when it is back
	tail.Take([]byte("\n=== wlan10 8 8\n"))
	if _, has := tail.off["wlan20"]; has {
		t.Errorf("a gone log is remembered: %v", tail.off)
	}
	// a header that is not ours is data, a name outside the daemon's range is ignored
	got = tail.Take([]byte("\n=== wlan9 5 -\n\n=== wlan10 40 8\nx\n=== not a header\nline two\n"))
	if len(got) != 3 || got[0].Text != "x" || got[1].Text != "=== not a header" || tail.off["wlan9"] != 0 {
		t.Errorf("%+v %v", got, tail.off)
	}
	// carriage returns and blank lines are dropped
	got = tail.Take([]byte("\n=== wlan10 40 20\n\r\nok\r\n\n"))
	if len(got) != 1 || got[0].Text != "ok" {
		t.Errorf("%+v", got)
	}
}

func TestLogScript(t *testing.T) {
	var tail LogTail
	tail.off = map[string]int64{"wlan10": 12, "wlan17": 0, "wlan9": 5, "x; reboot": 1}
	s := tail.Script()
	if strings.Contains(s, "wlan9") || strings.Contains(s, "reboot") || strings.Contains(s, "chanim") {
		t.Errorf("script: %s", s)
	}
	if !strings.Contains(s, "o_wlan10=12\n") || !strings.Contains(s, "o_wlan17=0\n") {
		t.Errorf("offsets: %s", s)
	}
	if !strings.Contains(apLogHead, "count="+apLogBlocks+" ") || apLogBlocks != "32" || apLogMax != 32 {
		t.Error("the block count of the script and the limit of the reader differ")
	}
	checkSh(t, "logs", s)
}

// TestLogScriptRuns runs the log script against real files when the host has a
// POSIX sh and a tail that takes -c +N (it is the device's tail's form).
func TestLogScriptRuns(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{"wlan10.log": "old line\nnew one\nand more\n", "wlan20.log": "x\n", "wlan30.log": "ignored\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.Replace((&LogTail{off: map[string]int64{"wlan10": 9}}).Script(), "D="+apRunDir, "D="+filepath.ToSlash(dir), 1)
	script = strings.Replace(script, apPATH, "", 1)
	cmd := exec.Command(sh)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("the host's sh or tail cannot run it: %v", err)
	}
	tail := LogTail{off: map[string]int64{"wlan10": 9}}
	got := tail.Take(out)
	if len(got) != 2 || got[0].Text != "new one" || got[1].Text != "and more" || tail.off["wlan20"] != 2 {
		t.Errorf("out %q -> %+v %v", out, got, tail.off)
	}
}

func checkSh(t *testing.T, name, script string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this host: cannot run sh -n")
	}
	f := filepath.Join(t.TempDir(), name+".sh")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
		t.Errorf("sh -n %s: %v\n%s", name, err, out)
	}
}
