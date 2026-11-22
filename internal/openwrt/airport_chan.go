// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// An automatic channel that chooses. The controller says "auto"; hostapd's own
// acs on this unit only keeps the channel the radio is on. With the neighbour
// scan the daemon already has (airport_scan.go) it picks the channel with the
// least interference among a short list of clean ones, and then leaves it
// alone: a mediocre channel that stays beats a good one that moves, because
// every move drops the radio's clients.
//
// Where it applies: 2.4 GHz (1, 6, 11) when the controller says auto; 5 GHz
// (36 40 44 48 149 153 157 161, no DFS) when it says auto and there is no mesh
// uplink on that radio. With the uplink the 5 GHz radio follows the uplink's
// channel, as before.

// Candidate channels.
var (
	AirPortChans24 = []int{1, 6, 11}
	AirPortChans5  = []int{36, 40, 44, 48, 149, 153, 157, 161}
)

// Timing and margins. A chosen channel is held for chanHold; only an idle radio
// is reconsidered; a move needs the new channel to be clearly better.
const (
	chanHold      = 4 * time.Hour
	chanMaxAge    = 2 * time.Hour // scan results older than this are not trusted
	chanMinGain   = 3.0           // cost units: more than one weak neighbour
	chanMinFactor = 0.7           // new cost at most this share of the current one
)

// ChannelCost is the interference a radio on channel ch (20, 40 or 80 MHz wide)
// would have from the networks in nb, which must not include our own:
//
//	cost = sum over neighbours of  overlap * weight
//	weight  = 1 + 10^((rssi+90)/20)        rssi clamped to -100..-30 dBm
//	overlap = 2.4 GHz: 1 - d/((w+wn)/2+5) for centre distance d MHz, cut at 0
//	          (channels 1, 6 and 11 do not overlap at 20 MHz: d = 25)
//	          5 GHz:   1 when the two bands of spectrum intersect, else 0
//
// The 1 in the weight is the count (a crowded channel is bad even when every
// neighbour is weak); the power term makes one strong neighbour count as much as
// many far ones (a 20 dB stronger network weighs 10 times more). Amplitude and
// not power, so that a single very strong network does not hide everything else.
func ChannelCost(ch, width int, nb []BSS) float64 {
	if width != 40 && width != 80 {
		width = 20
	}
	var cost float64
	for _, b := range nb {
		if (ch <= 14) != (b.Channel <= 14) || b.Channel <= 0 {
			continue
		}
		wn := b.Width
		if wn < 20 {
			wn = 20
		}
		ours, theirs := chanCentre(ch), b.CenterFreq
		if ch > 14 {
			ours = chanCentre5(ch, width)
		}
		if theirs == 0 || wn <= 20 {
			theirs = chanCentre(b.Channel)
			if b.Channel > 14 {
				theirs = 5000 + 5*b.Channel
			}
		}
		d := math.Abs(float64(ours - theirs))
		var overlap float64
		if ch <= 14 {
			// our own width on 2.4 GHz stays 20 MHz
			overlap = math.Max(0, 1-d/(float64(20+wn)/2+5))
		} else if d < float64(width+wn)/2 {
			overlap = 1
		}
		if overlap == 0 {
			continue
		}
		s := math.Min(-30, math.Max(-100, float64(b.Signal)))
		cost += overlap * (1 + math.Pow(10, (s+90)/20))
	}
	return cost
}

// chanCentre is the centre frequency of a 2.4 GHz channel number.
func chanCentre(ch int) int {
	if ch == 14 {
		return 2484
	}
	return 2407 + 5*ch
}

// chanCentre5 is the centre frequency of the 5 GHz block of this width (20, 40
// or 80 MHz) that holds channel ch. Blocks are aligned from channel 36 (UNII-1
// and 2) and from 149 (UNII-3).
func chanCentre5(ch, width int) int {
	base := 36
	if ch >= 100 {
		base = 149
	}
	step := width / 20 * 4 // channel numbers per block
	lo := base + (ch-base)/step*step
	return 5000 + 5*(lo+(step-4)/2)
}

// ChooseChannel picks, among cands, the channel with the lowest cost for nb.
// cur is the channel the radio is on now (0 or not a candidate: no preference).
// On a tie the current channel stays, else the first of cands wins. The radio
// only moves when the best channel is clearly better than the current one:
// cost lower by chanMinGain and at most chanMinFactor of it. It returns the
// choice and every candidate's cost.
func ChooseChannel(cands []int, width int, nb []BSS, cur int) (int, map[int]float64) {
	costs := map[int]float64{}
	best := 0
	for _, c := range cands {
		costs[c] = ChannelCost(c, width, nb)
		if best == 0 || costs[c] < costs[best]-1e-9 {
			best = c
		}
	}
	if cc, ok := costs[cur]; ok && best != cur {
		if costs[best] > cc-1e-9 || cc-costs[best] < chanMinGain || costs[best] > chanMinFactor*cc {
			best = cur
		}
	}
	return best, costs
}

// costText is the costs in channel order, for the log.
func costText(costs map[int]float64) string {
	var chs []int
	for c := range costs {
		chs = append(chs, c)
	}
	sort.Ints(chs)
	var p []string
	for _, c := range chs {
		p = append(p, fmt.Sprintf("%d:%.1f", c, costs[c]))
	}
	return strings.Join(p, " ")
}

// ScanSource gives the neighbour scan of a band ("ng", "na") and when it was
// taken; nothing when there is none.
type ScanSource func(band string) ([]BSS, time.Time)

// autoChan is what is remembered for one band.
type autoChan struct {
	ch      int
	at      time.Time // when the channel was chosen
	checked time.Time // when it was last weighed again
}

// AutoChannels remembers the automatic channel chosen for each band, so that a
// configuration push (which applies again) does not move the radio.
type AutoChannels struct {
	mu sync.Mutex
	m  map[string]autoChan // "2g" / "5g"
}

// autoRadio is a radio the daemon chooses for: automatic in the controller's
// config, with at least one WLAN wanted on it, and free to move.
type autoRadio struct {
	name, band string
	cands      []int
	width      int
	phy        string
}

func autoRadios(w translate.Wanted, uplink5 int) []autoRadio {
	used := map[string]bool{}
	for _, l := range w.WLANs {
		if !l.Disabled {
			used[l.Radio] = true
		}
	}
	var out []autoRadio
	for _, r := range w.Radios {
		if r.Disabled || r.Channel != 0 || !used[r.Name] {
			continue
		}
		switch {
		case r.Band == "2g":
			out = append(out, autoRadio{r.Name, "2g", AirPortChans24, 20, "bwl1"})
		case r.Band == "5g" && uplink5 == 0:
			wd := r.Width
			if wd != 20 && wd != 40 {
				wd = 80 // as airPortVAP: 80 or not said
			}
			out = append(out, autoRadio{r.Name, "5g", AirPortChans5, wd, "bwl0"})
		}
	}
	return out
}

// neighbours is the scan of the band without our own networks.
func neighbours(src ScanSource, snap *Snapshot, band string, now time.Time) ([]BSS, bool) {
	bss, at := src(map[string]string{"2g": "ng", "5g": "na"}[band])
	if len(bss) == 0 || at.IsZero() || now.Sub(at) > chanMaxAge {
		return nil, false
	}
	own := map[string]bool{}
	if snap != nil {
		for _, r := range snap.Radios {
			own[r.BSSID] = true
			for _, v := range r.VAPs {
				own[v.BSSID] = true
			}
		}
	}
	var out []BSS
	for _, b := range bss {
		if !own[b.BSSID] {
			out = append(out, b)
		}
	}
	return out, true
}

// radioState reads the channel a radio is on and whether it has clients.
func radioState(snap *Snapshot, phy string) (channel int, clients bool) {
	if snap == nil {
		return 0, false
	}
	for _, r := range snap.Radios {
		if r.Phy != phy {
			continue
		}
		channel = r.Channel
		for _, v := range r.VAPs {
			if v.Mode == "Master" && len(v.Stations) > 0 {
				clients = true
			}
			if channel == 0 {
				channel = v.Channel
			}
		}
	}
	return channel, clients
}

// Apply puts the remembered automatic channel on the automatic radios of w. A
// band with no memory is chosen now, when the scan has results; without them
// the radio keeps the channel it has (Channel stays 0 = automatic). The notes
// say what was done, for the log.
func (a *AutoChannels) Apply(w translate.Wanted, snap *Snapshot, src ScanSource, uplink5 int, now time.Time) (translate.Wanted, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		a.m = map[string]autoChan{}
	}
	auto := map[string]bool{}
	var notes []string
	radios := append([]translate.WantedRadio(nil), w.Radios...)
	for _, ar := range autoRadios(w, uplink5) {
		auto[ar.band] = true
		mem, have := a.m[ar.band]
		if !have {
			nb, ok := neighbours(src, snap, ar.band, now)
			if !ok {
				notes = append(notes, fmt.Sprintf("radio %s: automatic channel: no scan results yet, the radio keeps its channel", ar.name))
				continue
			}
			cur, _ := radioState(snap, ar.phy)
			ch, costs := ChooseChannel(ar.cands, ar.width, nb, cur)
			mem = autoChan{ch: ch, at: now, checked: now}
			a.m[ar.band] = mem
			notes = append(notes, fmt.Sprintf("radio %s: automatic channel: chose %d from the neighbour scan (%d networks; cost %s; was on %d)", ar.name, ch, len(nb), costText(costs), cur))
		} else {
			notes = append(notes, fmt.Sprintf("radio %s: automatic channel: staying on %d, chosen %s ago", ar.name, mem.ch, now.Sub(mem.at).Round(time.Minute)))
		}
		for i := range radios {
			if radios[i].Name == ar.name {
				radios[i].Channel = mem.ch
			}
		}
	}
	for _, band := range []string{"2g", "5g"} {
		if !auto[band] {
			delete(a.m, band) // fixed by the controller, or not ours to choose: a later "auto" starts over
		}
	}
	w.Radios = radios
	return w, notes
}

// Reconsider weighs the automatic channels again and reports what changed (a
// text for the log; "" = nothing). It moves a radio only when that radio has no
// client, its channel has been held for chanHold and the scan is fresh, and a
// radio that has no choice yet and now has scan results gets its first one. The
// caller applies again when it returns a text.
func (a *AutoChannels) Reconsider(w translate.Wanted, snap *Snapshot, src ScanSource, uplink5 int, now time.Time) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		a.m = map[string]autoChan{}
	}
	var did []string
	for _, ar := range autoRadios(w, uplink5) {
		cur, clients := radioState(snap, ar.phy)
		if clients {
			continue
		}
		mem, have := a.m[ar.band]
		if have && (now.Sub(mem.at) < chanHold || now.Sub(mem.checked) < chanHold) {
			continue
		}
		nb, ok := neighbours(src, snap, ar.band, now)
		if !ok {
			continue
		}
		if !have {
			ch, costs := ChooseChannel(ar.cands, ar.width, nb, cur)
			a.m[ar.band] = autoChan{ch: ch, at: now, checked: now}
			did = append(did, fmt.Sprintf("radio %s: automatic channel: chose %d from the neighbour scan (cost %s; was on %d)", ar.name, ch, costText(costs), cur))
			continue
		}
		ch, costs := ChooseChannel(ar.cands, ar.width, nb, mem.ch)
		mem.checked = now
		if ch != mem.ch {
			did = append(did, fmt.Sprintf("radio %s: automatic channel: moving from %d to %d (cost %s)", ar.name, mem.ch, ch, costText(costs)))
			mem.ch, mem.at = ch, now
		}
		a.m[ar.band] = mem
	}
	return strings.Join(did, "; ")
}
