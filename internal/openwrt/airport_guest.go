// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// The controller's guest policy on an AirPort, with pf. pf's input hook runs on
// the VAPs (gaps.md, "Correction and final isolation ruleset"), so what a guest
// may reach can be decided on the way in, with no MAC address: the ranges are
// enough. What the controller sends (translate.GuestPolicy) is the restricted
// ranges (ipset guest_restricted), the pre-allowed ones (guest_pre_allow) and the
// captive portal. The portal is not possible (pf cannot match a MAC address, so
// it cannot tell an authorised guest from another), so a guest WLAN with it stays
// off (apNetFor); the ranges become rules, per guest VAP, after the isolation
// lines (the last matching rule wins, and "quick" ends the search):
//
//	pass in quick on VAP inet proto udp from any port 68 to any port 67 tag UOWSTA
//	pass in quick on VAP inet from any to PRE tag UOWSTA        for each pre-allowed
//	block in quick on VAP inet from any to RES                  for each restricted
//	block in quick on VAP inet6 all
//
// The order is the one guestRuleset (guest.go) has on OpenWrt: DHCP, then the
// pre-allowed ranges, then the restricted ones; what none of them matches (the
// internet) falls through to the isolation lines' "pass in on VAP all tag UOWSTA".
// "quick" would skip that tag, so the passes carry it themselves.
//
// DNS: OpenWrt passes port 53 to the LAN's DNS servers, which it reads off the
// device. Nothing here knows them, and the policy does not name them, so guests
// reach a resolver on a restricted address only through a pre-allowed range (the
// controller puts the gateway there by default).
//
// IPv6: guestRuleset drops all of it for guests (ether type ip6), so does this:
// "block in quick ... inet6 all". An IPv6 range in the policy is then redundant;
// it is checked and counted, not written.

// apMaxGuestRanges bounds the ruleset: a range list longer than this is refused.
const apMaxGuestRanges = 64

// apPrivate is the private IPv4 space (RFC 1918).
var apPrivate = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// PFGuest is the guest policy for the VAPs of the guest WLANs: one policy for all.
type PFGuest struct {
	VAPs       []string // wlan10..17, wlan20..27; each also in the isolated list
	Restricted []string // IPv4 prefixes in canonical form
	PreAllow   []string
}

// apGuestRanges checks every range as "a.b.c.d/n" or an IPv6 prefix, nothing
// else (no bare address, no list, no zone, no IPv4-mapped form, no "/0": that
// would be every address) and returns the IPv4 ones in canonical form (host
// bits cleared), and how many IPv6 ones there were.
func apGuestRanges(in []string) (v4 []string, v6 int, err error) {
	if len(in) > apMaxGuestRanges {
		return nil, 0, fmt.Errorf("%d ranges (the limit is %d)", len(in), apMaxGuestRanges)
	}
	seen := map[string]bool{}
	for _, s := range in {
		p, err := netip.ParsePrefix(s)
		switch {
		case err != nil || strings.Count(s, "/") != 1:
			return nil, 0, fmt.Errorf("range %q is not a CIDR prefix", s)
		case p.Addr().Is4In6() || p.Addr().Zone() != "":
			return nil, 0, fmt.Errorf("range %q is not a plain IPv4 or IPv6 prefix", s)
		case p.Bits() == 0:
			return nil, 0, fmt.Errorf("range %q is every address", s)
		case !p.Addr().Is4():
			v6++
			continue
		}
		if m := p.Masked().String(); !seen[m] {
			seen[m] = true
			v4 = append(v4, m)
		}
	}
	return v4, v6, nil
}

// apCoversPrivate says whether some range holds a whole private block (10/8,
// 172.16/12 or 192.168/16): the guests then cannot reach the LAN whatever its
// subnet is. A range inside a block is not enough: the LAN may be elsewhere in it.
func apCoversPrivate(v4 []string) bool {
	for _, s := range v4 {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			continue
		}
		for _, b := range apPrivate {
			if p.Bits() <= b.Bits() && p.Contains(b.Addr()) {
				return true
			}
		}
	}
	return false
}

// apGuestSkip is the reason a guest WLAN gets no VAP, "" = it does. A range that
// cannot be checked stops it everywhere, and on the LAN (no VLAN, no gateway
// filtering in between) the restricted ranges must keep guests off private space.
func apGuestSkip(l translate.WantedWLAN, n APNet) string {
	res, _, err := apGuestRanges(n.Restricted)
	if err != nil {
		return "guest policy: restricted " + err.Error()
	}
	if _, _, err := apGuestRanges(n.PreAllow); err != nil {
		return "guest policy: pre-allowed " + err.Error()
	}
	if l.VLAN == 0 && !apCoversPrivate(res) {
		return "guest network on the LAN: the guest policy has no restricted range that covers private space (10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16), so guests would reach the whole LAN"
	}
	return ""
}

// AirPortGuest is the guest policy for the guest VAPs of the last apply, with
// the notes for the log. No guest VAP: nothing and no notes. Every guest VAP is
// isolated (apNetFor forces it), which is what puts it in the pf ruleset's
// tagging and the filter on its bridge.
func AirPortGuest(vaps []AirPortVAP, pol translate.Guest) (PFGuest, []string, error) {
	var g PFGuest
	for _, v := range vaps {
		if v.Guest {
			g.VAPs = append(g.VAPs, v.Name)
		}
	}
	if len(g.VAPs) == 0 {
		return PFGuest{}, nil, nil
	}
	res, res6, err := apGuestRanges(pol.Restricted)
	if err != nil {
		return PFGuest{}, nil, fmt.Errorf("guest policy: restricted %w", err)
	}
	pre, pre6, err := apGuestRanges(pol.PreAllow)
	if err != nil {
		return PFGuest{}, nil, fmt.Errorf("guest policy: pre-allowed %w", err)
	}
	g.Restricted, g.PreAllow = res, pre
	notes := []string{
		fmt.Sprintf("guest WLAN(s) (%s): pf blocks what they send to the restricted ranges [%s], except the pre-allowed ones [%s]; DHCP always passes and so does the rest (the internet); guests' IPv6 is blocked altogether, as on OpenWrt", strings.Join(g.VAPs, " "), strings.Join(res, " "), strings.Join(pre, " ")),
		"guest WLAN(s): the captive portal and guest authorisation are not supported on this platform (pf cannot match a MAC address); a guest WLAN with the portal on stays off",
	}
	if len(pre) == 0 && apCoversPrivate(res) {
		notes = append(notes, "guest WLAN(s): no range is pre-allowed, so guests reach no DNS server or gateway on a private address (the policy does not name DNS servers)")
	}
	if res6+pre6 > 0 {
		notes = append(notes, fmt.Sprintf("guest policy: %d IPv6 range(s) not written: guests' IPv6 is blocked altogether", res6+pre6))
	}
	return g, notes, nil
}

// check refuses what must not reach the shell: a VAP that is not the daemon's,
// not isolated, or twice; a range that is not a canonical IPv4 prefix.
func (g PFGuest) check(iso []PFIso) error {
	if len(g.VAPs) == 0 {
		return nil
	}
	isolated := map[string]bool{}
	for _, x := range iso {
		isolated[x.VAP] = true
	}
	seen := map[string]bool{}
	for _, v := range g.VAPs {
		switch {
		case !apVAPName.MatchString(v):
			return fmt.Errorf("refusing VAP name %q", v)
		case seen[v]:
			return fmt.Errorf("guest VAP %s twice", v)
		case !isolated[v]:
			return fmt.Errorf("guest VAP %s is not isolated", v)
		}
		seen[v] = true
	}
	for _, list := range [][]string{g.Restricted, g.PreAllow} {
		got, v6, err := apGuestRanges(list)
		if err != nil {
			return err
		}
		if v6 != 0 || len(got) != len(list) {
			return errors.New("guest ranges are IPv4 prefixes, once each")
		}
		for i := range got {
			if got[i] != list[i] {
				return fmt.Errorf("guest range %q is not canonical (%s)", list[i], got[i])
			}
		}
	}
	return nil
}
