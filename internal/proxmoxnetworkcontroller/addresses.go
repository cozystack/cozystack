/*
Copyright 2025 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxmoxnetworkcontroller

import (
	"fmt"
	"net/netip"
	"strings"
)

// addrRange is an inclusive range of addresses of one family.
type addrRange struct{ from, to netip.Addr }

func (r addrRange) contains(a netip.Addr) bool {
	return r.from.Compare(a) <= 0 && a.Compare(r.to) <= 0
}

// parseAddrRange accepts the three spellings both Kube-OVN excludeIps and the
// IPAM provider use: a single address, "first..last" (Kube-OVN) or
// "first-last" (IPAM), and a CIDR.
func parseAddrRange(s string) (addrRange, error) {
	s = strings.TrimSpace(s)
	if p, err := netip.ParsePrefix(s); err == nil {
		p = p.Masked()
		return addrRange{from: p.Addr(), to: lastAddr(p)}, nil
	}
	sep := ""
	switch {
	case strings.Contains(s, ".."):
		sep = ".."
	case strings.Count(s, "-") == 1:
		sep = "-"
	}
	if sep == "" {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return addrRange{}, fmt.Errorf("%q is not an address, range or CIDR", s)
		}
		return addrRange{from: a, to: a}, nil
	}
	lo, hi, _ := strings.Cut(s, sep)
	from, err := netip.ParseAddr(strings.TrimSpace(lo))
	if err != nil {
		return addrRange{}, fmt.Errorf("%q: bad start address", s)
	}
	to, err := netip.ParseAddr(strings.TrimSpace(hi))
	if err != nil {
		return addrRange{}, fmt.Errorf("%q: bad end address", s)
	}
	if from.Is4() != to.Is4() || to.Less(from) {
		return addrRange{}, fmt.Errorf("%q is not an ascending range of one family", s)
	}
	return addrRange{from: from, to: to}, nil
}

func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Addr().AsSlice()
	bits := p.Bits()
	for i := range a {
		for b := 0; b < 8; b++ {
			if i*8+b >= bits {
				a[i] |= 0x80 >> b
			}
		}
	}
	out, _ := netip.AddrFromSlice(a)
	return out
}

// coveredBy reports whether every address of r lies in the union of ranges.
// Ranges are few (a handful of excludeIps), so a walk from the start of r that
// jumps to the end of whichever range covers the current address is enough.
func coveredBy(r addrRange, ranges []addrRange) bool {
	cur := r.from
	for {
		advanced := false
		for _, c := range ranges {
			if c.contains(cur) {
				if r.to.Compare(c.to) <= 0 {
					return true
				}
				cur = c.to.Next()
				if !cur.IsValid() {
					return false
				}
				advanced = true
			}
		}
		if !advanced {
			return false
		}
	}
}

// checkPoolSplit verifies the one rule that keeps two allocators from handing
// out the same address: every pool address must be inside the subnet and inside
// the subnet's excludeIps, which Kube-OVN never allocates. It also checks the
// pool's gateway and prefix against the subnet, because a VM configured with a
// different gateway than the router port would silently have no default route.
func checkPoolSplit(cidr, gateway string, excludeIps []string, pool InClusterIPPoolSpec) (reason, message string) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "SubnetCIDRInvalid", fmt.Sprintf("subnet cidrBlock %q: %v", cidr, err)
	}
	prefix = prefix.Masked()
	subnetRange := addrRange{from: prefix.Addr(), to: lastAddr(prefix)}

	if pool.Prefix != prefix.Bits() {
		return "PoolPrefixMismatch", fmt.Sprintf("pool prefix /%d differs from subnet %s", pool.Prefix, prefix)
	}
	if gateway != "" && pool.Gateway != gateway {
		return "PoolGatewayMismatch", fmt.Sprintf("pool gateway %q differs from subnet gateway %q", pool.Gateway, gateway)
	}

	excluded := make([]addrRange, 0, len(excludeIps))
	for _, e := range excludeIps {
		r, err := parseAddrRange(e)
		if err != nil {
			return "SubnetExcludeIPsInvalid", err.Error()
		}
		excluded = append(excluded, r)
	}
	for _, a := range pool.Addresses {
		r, err := parseAddrRange(a)
		if err != nil {
			return "PoolAddressInvalid", err.Error()
		}
		if !subnetRange.contains(r.from) || !subnetRange.contains(r.to) {
			return "PoolOutsideSubnet", fmt.Sprintf("pool address %q is outside %s", a, prefix)
		}
		if !coveredBy(r, excluded) {
			return "PoolOverlapsSubnet", fmt.Sprintf("pool address %q is not in the subnet's excludeIps, so Kube-OVN may hand it to a pod", a)
		}
	}
	return "", ""
}
